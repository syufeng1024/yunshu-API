package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/redisx"
)

// Login lockout thresholds are package constants, not configuration. The 45
// configuration keys are frozen and a lockout window is not a per-deployment
// business knob: raising it through t_config_item would let a compromised
// account extend its own brute-force budget.
const (
	loginAccountFailLimit  = 5
	loginAccountLockWindow = 15 * time.Minute
	loginIPFailLimit       = 20
	loginIPLockWindow      = 10 * time.Minute
)

// loginLockDimension names which failure budget produced the lock. The two
// dimensions answer with different codes: only the account lock blocks a
// correct password, so only it may claim login_locked.
type loginLockDimension int

const (
	lockDimensionAccount loginLockDimension = iota
	lockDimensionIP
)

// loginLock is the combined verdict of the account and IP dimensions. The
// longest remaining window wins so the client is told the time after which
// both dimensions have cleared.
type loginLock struct {
	retryAfter time.Duration
	limit      int64
	dimension  loginLockDimension
}

func (l loginLock) locked() bool { return l.retryAfter > 0 }

// loginLockState reads K28/K29. A Redis error is returned so the caller can
// fail closed: without the counters an attacker has an unlimited password
// budget, which is worse than a 503 for the duration of the outage.
func (s *Service) loginLockState(ctx context.Context, ident Identifier, ip string) (loginLock, error) {
	rdb := s.Sessions.Redis
	if rdb == nil {
		return loginLock{}, errors.New("auth: session manager has no redis")
	}
	type dimension struct {
		key   string
		limit int64
		dim   loginLockDimension
	}
	dims := []dimension{{
		key:   redisx.LoginAccountFailKey(ident.Value),
		limit: loginAccountFailLimit,
		dim:   lockDimensionAccount,
	}}
	if ip != "" {
		dims = append(dims, dimension{
			key:   redisx.LoginIPFailKey(ip),
			limit: loginIPFailLimit,
			dim:   lockDimensionIP,
		})
	}
	keys := make([]string, len(dims))
	for i, d := range dims {
		keys[i] = d.key
	}

	cctx, cancel := rdb.CommandContext(ctx)
	defer cancel()
	values, err := rdb.Raw().MGet(cctx, keys...).Result()
	if err != nil {
		return loginLock{}, err
	}

	best := loginLock{}
	for i, d := range dims {
		n, ok := counterValue(values[i])
		if !ok || n < d.limit {
			continue
		}
		ttl, err := rdb.Raw().TTL(cctx, d.key).Result()
		if err != nil {
			return loginLock{}, err
		}
		// ttl <= 0 means the counter expired (or, pathologically, has no TTL).
		// Treating it as unlocked is correct for the expired case; the no-TTL
		// case cannot be produced by recordLoginFailure, which always sets one.
		if ttl <= 0 {
			continue
		}
		if ttl > best.retryAfter {
			best = loginLock{retryAfter: ttl, limit: d.limit, dimension: d.dim}
		}
	}
	return best, nil
}

// recordLoginFailure increments the account and IP counters. It is best-effort
// by design: the password was already wrong, and turning a counting failure
// into a different error would tell the caller something useful about Redis.
// The failure is logged instead.
func (s *Service) recordLoginFailure(ctx context.Context, ident Identifier, ip string) {
	rdb := s.Sessions.Redis
	if rdb == nil {
		return
	}
	type dimension struct {
		key    string
		window time.Duration
		limit  int64
	}
	dims := []dimension{{
		key:    redisx.LoginAccountFailKey(ident.Value),
		window: loginAccountLockWindow,
		limit:  loginAccountFailLimit,
	}}
	if ip != "" {
		dims = append(dims, dimension{
			key:    redisx.LoginIPFailKey(ip),
			window: loginIPLockWindow,
			limit:  loginIPFailLimit,
		})
	}

	cctx, cancel := rdb.CommandContext(ctx)
	defer cancel()
	for _, d := range dims {
		n, err := rdb.Raw().Incr(cctx, d.key).Result()
		if err != nil {
			s.warn(ctx, "login_fail_count_failed: "+err.Error())
			continue
		}
		// The window starts at the first failure, but crossing the threshold
		// restarts it: otherwise a fifth failure at minute 14 would buy only
		// one minute of lockout.
		if n == 1 || n >= d.limit {
			if err := rdb.Raw().Expire(cctx, d.key, d.window).Err(); err != nil {
				s.warn(ctx, "login_fail_expire_failed: "+err.Error())
				// An INCR without a TTL would lock the account until Redis
				// restarts or a successful login clears it — and a successful
				// login is exactly what the lock blocks. Drop the counter
				// rather than create a permanent lock; enforcement resumes on
				// the next failure.
				_ = rdb.Raw().Del(cctx, d.key).Err()
			}
		}
	}
}

// clearAccountFailures runs on a successful login. Only K28 is deleted: the IP
// counter covers every account behind that address, so one correct password
// must not reset the budget for the other identifiers an attacker is probing.
func (s *Service) clearAccountFailures(ctx context.Context, ident Identifier) {
	rdb := s.Sessions.Redis
	if rdb == nil {
		return
	}
	cctx, cancel := rdb.CommandContext(ctx)
	defer cancel()
	if err := rdb.Raw().Del(cctx, redisx.LoginAccountFailKey(ident.Value)).Err(); err != nil {
		s.warn(ctx, "login_fail_clear_failed: "+err.Error())
	}
}

// lockError maps the lockout onto the code of the dimension that fired. The
// account dimension uses login_locked: the account is blocked, and retrying
// with the correct password fails until the window ends. The IP dimension
// keeps rpm_limit_exceeded, because that refusal is about the address sending
// too many attempts — telling the caller to wait is the only useful action,
// and the account itself is not locked. Sharing one code made a locked-out
// user believe that slowing down would help.
func (s *Service) lockError(lock loginLock) *apierr.APIError {
	// Round up with integer duration arithmetic; there is no floating-point
	// step even for this non-money value (the repository-wide rule keeps a
	// future amount calculation from copying a float idiom).
	secs := int((lock.retryAfter + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	var e *apierr.APIError
	if lock.dimension == lockDimensionIP {
		e = apierr.Newf(apierr.CodeRPMLimitExceeded, lock.limit, secs)
		e.Message = fmt.Sprintf("该 IP 登录失败次数过多，请 %d 秒后重试", secs)
	} else {
		e = apierr.Newf(apierr.CodeLoginLocked, secs)
	}
	// limit is the failure budget of the dimension that fired (5 per account,
	// 20 per IP); remaining is 0 while the lock is in force.
	e = e.WithLimit(float64(lock.limit)).WithRemaining(0).WithRetryAfter(secs)
	// WithRetryAfter renders reset_at from time.Now; overwrite it with the
	// injected clock so the response and the test that drives the clock agree.
	e = e.WithResetAt(s.now().Now().Add(time.Duration(secs) * time.Second))
	if lock.dimension == lockDimensionIP {
		// Set after WithRetryAfter: that call re-renders the spec hint.
		e = e.WithHint("按 {retry} 秒等待后重试；如非本人操作请检查网络环境")
	}
	return e
}

// clientIP reuses the audit package's parsing (no X-Forwarded-For: no trusted
// proxy list has been defined). An empty address is not counted.
func clientIP(r *http.Request) string {
	raw := audit.IPBytes(r)
	if len(raw) == 0 {
		return ""
	}
	addr, ok := netip.AddrFromSlice(raw)
	if !ok {
		return ""
	}
	return addr.Unmap().String()
}

func counterValue(v any) (int64, bool) {
	switch x := v.(type) {
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n, err == nil
	case []byte:
		n, err := strconv.ParseInt(strings.TrimSpace(string(x)), 10, 64)
		return n, err == nil
	default:
		return 0, false
	}
}

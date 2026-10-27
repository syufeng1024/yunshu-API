package fund

import (
	"context"
	"errors"
	"strconv"
	"strings"

	goredis "github.com/redis/go-redis/v9"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/redisx"
)

// Idempotency-Key scopes for the money writes of this segment. Each scope is a
// different string, so a key reused across two endpoints cannot replay the
// other endpoint's row.
const (
	idemScopeRefund  = "console_refund"
	idemScopeInvoice = "console_invoice"
	idemScopeQuota   = "console_quota_apply"
)

// reserveIdem claims an Idempotency-Key for the idem.duplicate_window_seconds
// window. It mirrors createOrder's semantics:
//
//   - first caller: reserves the key as "pending" and returns finalize/release;
//   - duplicate inside the window: replay=true plus the row id the first
//     caller stored, or request_in_flight when that caller has not finished;
//   - Redis unavailable: logs a WARN and lets the request proceed, because the
//     database unique key is the authority and refusing money writes during a
//     cache outage would be worse than the duplicate risk.
func (s *Service) reserveIdem(
	ctx context.Context,
	uid int64,
	scope, idemKey string,
) (id int64, replay bool, finalize func(int64), release func(), apiErr *apierr.APIError) {
	noop := func(int64) {}
	if idemKey == "" || s.Redis == nil {
		return 0, false, noop, func() {}, nil
	}
	hash := redisx.IdemHash(scope, 0, idemKey)
	key := redisx.IdemKey(uid, hash)
	window := s.idemWindowDuration(ctx)

	cctx, cancel := s.Redis.CommandContext(ctx)
	ok, err := s.Redis.Raw().SetNX(cctx, key, idemPending, window).Result()
	cancel()
	if err != nil {
		s.logWarn(ctx, "fund_idem_reserve_failed: "+err.Error())
		return 0, false, noop, func() {}, nil
	}
	if ok {
		finalize = func(rowID int64) {
			cctx, cancel := s.Redis.CommandContext(ctx)
			_ = s.Redis.Raw().Set(cctx, key, strconv.FormatInt(rowID, 10), window).Err()
			cancel()
		}
		release = func() {
			cctx, cancel := s.Redis.CommandContext(ctx)
			_ = s.Redis.Raw().Del(cctx, key).Err()
			cancel()
		}
		return 0, false, finalize, release, nil
	}

	cctx, cancel = s.Redis.CommandContext(ctx)
	value, err := s.Redis.Raw().Get(cctx, key).Result()
	cancel()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			// The window expired between SET NX and GET.
			return 0, false, noop, func() {}, apierr.New(apierr.CodeRequestInFlight)
		}
		s.logWarn(ctx, "fund_idem_read_failed: "+err.Error())
		return 0, false, noop, func() {}, apierr.New(apierr.CodeRequestInFlight)
	}
	value = strings.TrimSpace(value)
	if value == "" || value == idemPending {
		return 0, false, noop, func() {}, apierr.New(apierr.CodeRequestInFlight)
	}
	rowID, perr := strconv.ParseInt(value, 10, 64)
	if perr != nil {
		s.logWarn(ctx, "fund_idem_value_invalid: "+value)
		return 0, false, noop, func() {}, apierr.New(apierr.CodeRequestInFlight)
	}
	return rowID, true, noop, func() {}, nil
}

// sanitizeIdemKey validates the header/body key length. Empty is allowed and
// means "no deduplication"; an over-long key is a body error.
func sanitizeIdemKey(headerValue, bodyValue string) (string, *apierr.APIError) {
	key := strings.TrimSpace(headerValue)
	if key == "" {
		key = strings.TrimSpace(bodyValue)
	}
	if len(key) > 128 {
		return "", mw.InvalidField("idempotency_key")
	}
	return key, nil
}

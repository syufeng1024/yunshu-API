package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/redisx"
)

// Election defaults (TDS §4.6 / K25).
//
// A live owner renews every 20s and therefore always has at least 70s of lease
// left. Takeover is allowed when less than 30s remain: that point is reached
// 60s after the owner's last successful renewal, which is the "失联 60s" rule.
// The heartbeat key has a 60s TTL and only answers "is the recorded owner
// alive"; the lease in MySQL remains the authority.
const (
	DefaultLeaseTTL       = 90 * time.Second
	DefaultRenewEvery     = 20 * time.Second
	DefaultTakeoverMargin = 30 * time.Second
	DefaultHeartbeatTTL   = 60 * time.Second
)

// Election owns t_job_lock leases and the K25 heartbeat.
type Election struct {
	DB         *sql.DB
	Redis      *redisx.Client
	InstanceID string
	Logger     *obslog.Logger
	Clock      clock.Clock

	LeaseTTL       time.Duration
	RenewEvery     time.Duration
	TakeoverMargin time.Duration
	HeartbeatTTL   time.Duration
}

// NewElection builds an election client. InstanceID must be unique per process.
func NewElection(db *sql.DB, rdb *redisx.Client, instanceID string, logger *obslog.Logger, clk clock.Clock) *Election {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Election{
		DB:             db,
		Redis:          rdb,
		InstanceID:     instanceID,
		Logger:         logger,
		Clock:          clk,
		LeaseTTL:       DefaultLeaseTTL,
		RenewEvery:     DefaultRenewEvery,
		TakeoverMargin: DefaultTakeoverMargin,
		HeartbeatTTL:   DefaultHeartbeatTTL,
	}
}

// Acquire tries to take the job lease. It returns true only when this instance
// owns the row after the statement.
func (e *Election) Acquire(ctx context.Context, job string) (bool, error) {
	if e == nil || e.DB == nil {
		return false, errors.New("jobs: election requires a database")
	}
	if job == "" {
		return false, errors.New("jobs: acquire requires a job name")
	}
	now := e.now()
	leaseUntil := now.Add(e.leaseTTL())

	// First acquisition: INSERT IGNORE returns 1 when it created the row.
	res, err := e.DB.ExecContext(ctx,
		`INSERT IGNORE INTO t_job_lock
		   (job_name, owner_id, lease_until, released_at, created_at, updated_at)
		 VALUES (?, ?, ?, NULL, ?, ?)`,
		job, e.InstanceID, leaseUntil, now, now,
	)
	if err != nil {
		return false, fmt.Errorf("jobs: insert lock %s: %w", job, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 1 {
		return true, nil
	}

	// Takeover: the row is released, or the current lease is close enough to
	// expiry that the owner has been silent for at least RenewEvery×3.
	res, err = e.DB.ExecContext(ctx,
		`UPDATE t_job_lock
		    SET owner_id = ?, lease_until = ?, released_at = NULL, updated_at = ?
		  WHERE job_name = ?
		    AND (released_at IS NOT NULL OR lease_until < ?)`,
		e.InstanceID, leaseUntil, now, job, now.Add(e.takeoverMargin()),
	)
	if err != nil {
		return false, fmt.Errorf("jobs: takeover lock %s: %w", job, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobs: takeover rows %s: %w", job, err)
	}
	return n == 1, nil
}

// Renew extends a lease this instance owns.
func (e *Election) Renew(ctx context.Context, job string) (bool, error) {
	if e == nil || e.DB == nil {
		return false, errors.New("jobs: election requires a database")
	}
	now := e.now()
	res, err := e.DB.ExecContext(ctx,
		`UPDATE t_job_lock
		    SET lease_until = ?, updated_at = ?
		  WHERE job_name = ? AND owner_id = ? AND released_at IS NULL`,
		now.Add(e.leaseTTL()), now, job, e.InstanceID,
	)
	if err != nil {
		return false, fmt.Errorf("jobs: renew lock %s: %w", job, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("jobs: renew rows %s: %w", job, err)
	}
	return n == 1, nil
}

// Release writes released_at for a lease this instance owns. It is called on
// graceful shutdown; a crash leaves the lease to expire instead.
func (e *Election) Release(ctx context.Context, job string) error {
	if e == nil || e.DB == nil {
		return errors.New("jobs: election requires a database")
	}
	now := e.now()
	_, err := e.DB.ExecContext(ctx,
		`UPDATE t_job_lock
		    SET released_at = ?, updated_at = ?
		  WHERE job_name = ? AND owner_id = ? AND released_at IS NULL`,
		now, now, job, e.InstanceID,
	)
	if err != nil {
		return fmt.Errorf("jobs: release lock %s: %w", job, err)
	}
	return nil
}

// Holder returns the current owner and lease for observability and tests.
func (e *Election) Holder(ctx context.Context, job string) (owner string, leaseUntil time.Time, released sql.NullTime, err error) {
	if e == nil || e.DB == nil {
		return "", time.Time{}, sql.NullTime{}, errors.New("jobs: election requires a database")
	}
	err = e.DB.QueryRowContext(ctx,
		`SELECT owner_id, lease_until, released_at FROM t_job_lock WHERE job_name = ?`, job,
	).Scan(&owner, &leaseUntil, &released)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, sql.NullTime{}, nil
	}
	return owner, leaseUntil, released, err
}

// Heartbeat writes K25 job:heartbeat:{instance} with a 60s TTL. Failure is
// WARN-only: the MySQL lease is the authority, the key is liveness telemetry.
func (e *Election) Heartbeat(ctx context.Context) error {
	if e == nil || e.Redis == nil || e.InstanceID == "" {
		return nil
	}
	cmdCtx, cancel := e.Redis.CommandContext(ctx)
	defer cancel()
	value := strconv.FormatInt(e.now().UnixMilli(), 10)
	if err := e.Redis.Raw().Set(cmdCtx, redisx.JobHeartbeatKey(e.InstanceID), value, e.heartbeatTTL()).Err(); err != nil {
		if e.Logger != nil {
			e.Logger.Warn(ctx, "job_heartbeat_failed: "+err.Error(), obslog.Fields{Stage: "election"})
		}
		return err
	}
	return nil
}

// StartHeartbeat writes K25 immediately and then every RenewEvery. The returned
// function stops the loop; the key expires on its own after 60s.
func (e *Election) StartHeartbeat(ctx context.Context) func() {
	if e == nil {
		return func() {}
	}
	hbCtx, cancel := context.WithCancel(ctx)
	go func() {
		_ = e.Heartbeat(hbCtx)
		ticker := time.NewTicker(e.renewEvery())
		defer ticker.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-ticker.C:
				_ = e.Heartbeat(hbCtx)
			}
		}
	}()
	return cancel
}

// Keep acquires the lease and renews it until ctx is cancelled, then releases
// it. It is the skeleton jobsd uses in T09's first segment: the lease lifecycle
// is real, the task body is supplied by the second segment.
func (e *Election) Keep(ctx context.Context, job string) {
	if e == nil {
		return
	}
	owned := false
	busyLogged := false
	ticker := time.NewTicker(e.renewEvery())
	defer ticker.Stop()

	for {
		if !owned {
			ok, err := e.Acquire(ctx, job)
			if err != nil && e.Logger != nil {
				e.Logger.Warn(ctx, "job_lock_acquire_failed: "+err.Error(), obslog.Fields{Stage: "election"})
			}
			if ok {
				owned = true
				busyLogged = false
				if e.Logger != nil {
					e.Logger.Info(ctx, "job_lock_acquired job="+job, obslog.Fields{Stage: "election"})
				}
			} else if !busyLogged {
				busyLogged = true
				if e.Logger != nil {
					owner, leaseUntil, _, _ := e.Holder(ctx, job)
					e.Logger.Info(ctx, fmt.Sprintf("job_lock_held_by_other job=%s owner=%s lease_until=%s",
						job, owner, leaseUntil.UTC().Format(time.RFC3339)), obslog.Fields{Stage: "election"})
				}
			}
		} else {
			ok, err := e.Renew(ctx, job)
			if err != nil && e.Logger != nil {
				e.Logger.Warn(ctx, "job_lock_renew_failed: "+err.Error(), obslog.Fields{Stage: "election"})
			}
			if !ok {
				owned = false
				if e.Logger != nil {
					e.Logger.Warn(ctx, "job_lock_lost job="+job, obslog.Fields{Stage: "election"})
				}
			}
		}

		select {
		case <-ctx.Done():
			if owned {
				relCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
				if err := e.Release(relCtx, job); err != nil && e.Logger != nil {
					e.Logger.Warn(ctx, "job_lock_release_failed: "+err.Error(), obslog.Fields{Stage: "drain"})
				} else if e.Logger != nil {
					e.Logger.Info(ctx, "job_lock_released job="+job, obslog.Fields{Stage: "drain"})
				}
				cancel()
			}
			return
		case <-ticker.C:
		}
	}
}

func (e *Election) leaseTTL() time.Duration {
	if e.LeaseTTL <= 0 {
		return DefaultLeaseTTL
	}
	return e.LeaseTTL
}

func (e *Election) renewEvery() time.Duration {
	if e.RenewEvery <= 0 {
		return DefaultRenewEvery
	}
	return e.RenewEvery
}

func (e *Election) takeoverMargin() time.Duration {
	if e.TakeoverMargin < 0 {
		return 0
	}
	if e.TakeoverMargin == 0 {
		return DefaultTakeoverMargin
	}
	return e.TakeoverMargin
}

func (e *Election) heartbeatTTL() time.Duration {
	if e.HeartbeatTTL <= 0 {
		return DefaultHeartbeatTTL
	}
	return e.HeartbeatTTL
}

func (e *Election) now() time.Time {
	if e.Clock == nil {
		return time.Now().UTC()
	}
	return e.Clock.Now().UTC()
}

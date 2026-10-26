package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

// Retention defaults from PRD F11-3 / TDS §8.5 R-3.
const (
	BodyRetentionDays = 180
	MetaRetentionDays = 365
	PurgeBatchSize    = 5000
	PurgeInterval     = 200 * time.Millisecond
	PurgeMaxBatches   = 100
)

// PurgeLog deletes log rows with batched DELETE statements.
//
// 不分区、不做整表清空：t_usage_record / t_upstream_attempt 的唯一键不含
// created_at，分区即失去 uk_ur_request 的全局唯一性。每批 5000 行、间隔
// 200ms，失败或中断后下一次从同一条件继续，天然幂等。
type PurgeLog struct {
	DB         *sql.DB
	Logger     *obslog.Logger
	Clock      clock.Clock
	BodyDays   int
	MetaDays   int
	BatchSize  int
	Interval   time.Duration
	MaxBatches int
}

// PurgeResult is one run.
type PurgeResult struct {
	BodyTable      bool
	BodyDeleted    int
	MetaDeleted    int
	AttemptDeleted int
	IdemDeleted    int
	Batches        int
	Truncated      bool
}

// NewPurgeLog builds the task with production retention.
func NewPurgeLog(db *sql.DB, logger *obslog.Logger, clk clock.Clock) *PurgeLog {
	if clk == nil {
		clk = clock.Real{}
	}
	return &PurgeLog{
		DB: db, Logger: logger, Clock: clk,
		BodyDays: BodyRetentionDays, MetaDays: MetaRetentionDays,
		BatchSize: PurgeBatchSize, Interval: PurgeInterval, MaxBatches: PurgeMaxBatches,
	}
}

// Run purges the body table (180 days, when present) and the metadata tables
// (365 days), then expired idempotency rows.
func (p *PurgeLog) Run(ctx context.Context) (PurgeResult, error) {
	var out PurgeResult
	if p == nil || p.DB == nil {
		return out, fmt.Errorf("jobs: purge_log requires a database")
	}
	if p.BatchSize <= 0 {
		p.BatchSize = PurgeBatchSize
	}
	if p.MaxBatches <= 0 {
		p.MaxBatches = PurgeMaxBatches
	}
	now := p.now()
	bodyCutoff := now.AddDate(0, 0, -p.bodyDays())
	metaCutoff := now.AddDate(0, 0, -p.metaDays())

	exists, err := p.tableExists(ctx, "t_request_body")
	if err != nil {
		return out, err
	}
	out.BodyTable = exists
	if exists {
		n, more, batches, err := p.purgeCreated(ctx, "t_request_body", bodyCutoff)
		if err != nil {
			return out, err
		}
		out.BodyDeleted, out.Batches = n, out.Batches+batches
		out.Truncated = out.Truncated || more
	}

	n, more, batches, err := p.purgeCreated(ctx, "t_usage_record", metaCutoff)
	if err != nil {
		return out, err
	}
	out.MetaDeleted = n
	out.Batches += batches
	out.Truncated = out.Truncated || more

	n, more, batches, err = p.purgeCreated(ctx, "t_upstream_attempt", metaCutoff)
	if err != nil {
		return out, err
	}
	out.AttemptDeleted = n
	out.Batches += batches
	out.Truncated = out.Truncated || more

	n, more, batches, err = p.purgeExpiredIdem(ctx, now)
	if err != nil {
		return out, err
	}
	out.IdemDeleted = n
	out.Batches += batches
	out.Truncated = out.Truncated || more

	if p.Logger != nil {
		p.Logger.Info(ctx, fmt.Sprintf(
			"purge_log done body=%d meta=%d attempts=%d idem=%d batches=%d truncated=%t",
			out.BodyDeleted, out.MetaDeleted, out.AttemptDeleted, out.IdemDeleted,
			out.Batches, out.Truncated), obslog.Fields{Stage: "purge_log"})
	}
	return out, nil
}

func (p *PurgeLog) bodyDays() int {
	if p.BodyDays <= 0 {
		return BodyRetentionDays
	}
	return p.BodyDays
}

func (p *PurgeLog) metaDays() int {
	if p.MetaDays <= 0 {
		return MetaRetentionDays
	}
	return p.MetaDays
}

// purgeCreated deletes rows older than cutoff in batches. It returns the total
// deleted, whether MaxBatches was hit, and the number of batches executed.
func (p *PurgeLog) purgeCreated(ctx context.Context, table string, cutoff time.Time) (int, bool, int, error) {
	// Table names are compile-time constants from this package, never input.
	q := fmt.Sprintf(
		`DELETE FROM %s WHERE created_at < ? ORDER BY created_at LIMIT ?`, table)
	return p.purge(ctx, q, cutoff)
}

func (p *PurgeLog) purgeExpiredIdem(ctx context.Context, now time.Time) (int, bool, int, error) {
	return p.purge(ctx,
		`DELETE FROM t_idempotency_record WHERE expires_at < ? ORDER BY expires_at LIMIT ?`, now)
}

func (p *PurgeLog) purge(ctx context.Context, q string, cutoff time.Time) (int, bool, int, error) {
	total, batches := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return total, false, batches, err
		}
		res, err := p.DB.ExecContext(ctx, q, cutoff.UTC(), p.BatchSize)
		if err != nil {
			return total, false, batches, fmt.Errorf("jobs: purge batch: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, false, batches, err
		}
		total += int(n)
		batches++
		if n < int64(p.BatchSize) {
			return total, false, batches, nil
		}
		if batches >= p.MaxBatches {
			return total, true, batches, nil
		}
		if p.Interval > 0 {
			// Deliberate backoff: a large purge must not hold the primary's
			// undo log or saturate I/O for the online path.
			select {
			case <-ctx.Done():
				return total, false, batches, ctx.Err()
			case <-time.After(p.Interval):
			}
		}
	}
}

func (p *PurgeLog) tableExists(ctx context.Context, table string) (bool, error) {
	var n int
	err := p.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables
		  WHERE table_schema = DATABASE() AND table_name = ?`, table).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("jobs: probe table %s: %w", table, err)
	}
	return n > 0, nil
}

func (p *PurgeLog) now() time.Time {
	if p == nil || p.Clock == nil {
		return time.Now().UTC()
	}
	return p.Clock.Now().UTC()
}

package settle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/ledger"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

// Settler is the ledger surface the worker needs. A nil ledger is a programming
// error, not a degraded mode: the worker exists only to move money.
type Settler interface {
	Settle(ctx context.Context, p ledger.SettleParams) (ledger.SettleResult, error)
}

// Worker defaults. The retry ladder is 1s/5s/25s/125s/625s; the sixth failure
// moves the task to DEAD (TDS §3.1 T-07).
const (
	DefaultMaxAttempts  = 5
	DefaultPollInterval = 250 * time.Millisecond
	DefaultBatchSize    = 200
	// staleRunningAfter reclaims a task whose worker crashed between claiming
	// it and writing the result. It is well above the 5s settlement budget.
	staleRunningAfter = 2 * time.Minute
)

// WorkerResult describes one ProcessOne call.
type WorkerResult struct {
	Processed     bool
	RequestID     string
	TaskID        int64
	Attempt       int
	Settled       bool
	Dead          bool
	ConsumedMicro int64
	Err           error
}

// Worker consumes t_settle_task rows.
//
// 幂等键 = request_id：ledger.Settle 以 settle:{request_id} 为唯一键，重复执行
// 返回既有结果，不产生第二条 CONSUME 流水。因此「任务被重复投递」与「worker 被
// 重跑」都只会扣一次钱。
type Worker struct {
	DB          *sql.DB
	Ledger      Settler
	Clock       clock.Clock
	Logger      *obslog.Logger
	Instance    string
	MaxAttempts int
	BatchSize   int
	PollEvery   time.Duration
}

// NewWorker builds a worker.
func NewWorker(db *sql.DB, led Settler, clk clock.Clock, logger *obslog.Logger, instance string) *Worker {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Worker{
		DB:          db,
		Ledger:      led,
		Clock:       clk,
		Logger:      logger,
		Instance:    instance,
		MaxAttempts: DefaultMaxAttempts,
		BatchSize:   DefaultBatchSize,
		PollEvery:   DefaultPollInterval,
	}
}

// Run processes tasks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	if w == nil || w.DB == nil || w.Ledger == nil {
		return errors.New("settle: worker requires database and ledger")
	}
	w.reclaimStale(ctx)
	ticker := time.NewTicker(w.PollEvery)
	defer ticker.Stop()
	for {
		for i := 0; i < w.BatchSize; i++ {
			res, err := w.ProcessOne(ctx)
			if err != nil {
				if w.Logger != nil {
					w.Logger.Warn(ctx, "settle_worker_error: "+err.Error(),
						obslog.Fields{Stage: "P26"})
				}
				break
			}
			if !res.Processed {
				break
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// ProcessOne claims and executes at most one PENDING task.
func (w *Worker) ProcessOne(ctx context.Context) (WorkerResult, error) {
	if w == nil || w.DB == nil || w.Ledger == nil {
		return WorkerResult{}, errors.New("settle: worker requires database and ledger")
	}
	task, ok, err := w.claim(ctx)
	if err != nil || !ok {
		return WorkerResult{}, err
	}
	res := WorkerResult{Processed: true, TaskID: task.id, RequestID: task.payload.RequestID, Attempt: task.attempt}

	settleCtx, cancel := context.WithTimeout(ctx, ledger.SettleTxnTimeout)
	settled, settleErr := w.Ledger.Settle(settleCtx, ledger.SettleParams{
		RequestID:     task.payload.RequestID,
		UserID:        task.payload.UserID,
		ConsumedMicro: task.payload.ConsumedMicro,
		ReleaseReason: task.payload.ReleaseReason,
	})
	cancel()
	if settleErr != nil {
		res.Err = settleErr
		w.retryOrDead(ctx, task, settleErr)
		return res, nil
	}
	res.Settled = true
	res.ConsumedMicro = settled.ConsumedMicro
	if err := w.deleteTask(ctx, task.id); err != nil {
		// The money is already settled. A failed delete only causes a duplicate
		// attempt later, which the request_id idempotency key absorbs.
		if w.Logger != nil {
			w.Logger.Warn(ctx, "settle_task_delete_failed: "+err.Error(),
				obslog.Fields{Stage: "P26", RequestID: task.payload.RequestID})
		}
	}
	return res, nil
}

type claimedTask struct {
	id      int64
	payload SettlePayload
	attempt int
}

func (w *Worker) claim(ctx context.Context) (claimedTask, bool, error) {
	now := w.now()
	tnx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return claimedTask{}, false, fmt.Errorf("settle: begin claim: %w", err)
	}
	defer tnx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	var (
		id      int64
		raw     []byte
		attempt int
	)
	err = tnx.QueryRowContext(ctx,
		`SELECT id, payload, attempt_count
		   FROM t_settle_task
		  WHERE state = ? AND next_retry_at <= ?
		  ORDER BY id ASC
		  LIMIT 1
		  FOR UPDATE SKIP LOCKED`,
		TaskStatePending, now,
	).Scan(&id, &raw, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return claimedTask{}, false, nil
	}
	if err != nil {
		return claimedTask{}, false, fmt.Errorf("settle: select task: %w", err)
	}
	attempt++
	if _, err := tnx.ExecContext(ctx,
		`UPDATE t_settle_task
		    SET state = ?, locked_by = ?, locked_at = ?, attempt_count = ?, updated_at = ?
		  WHERE id = ? AND state = ?`,
		TaskStateRunning, w.Instance, now, attempt, now, id, TaskStatePending,
	); err != nil {
		return claimedTask{}, false, fmt.Errorf("settle: claim task: %w", err)
	}
	if err := tnx.Commit(); err != nil {
		return claimedTask{}, false, fmt.Errorf("settle: commit claim: %w", err)
	}

	var p SettlePayload
	if err := jsonx.Unmarshal(raw, &p); err != nil {
		// A malformed payload is permanent. Mark it DEAD rather than retrying.
		_ = w.deadTask(ctx, id, fmt.Errorf("decode payload: %w", err))
		return claimedTask{}, false, fmt.Errorf("settle: decode task %d payload: %w", id, err)
	}
	return claimedTask{id: id, payload: p, attempt: attempt}, true, nil
}

func (w *Worker) retryOrDead(ctx context.Context, task claimedTask, settleErr error) {
	if task.attempt >= w.maxAttempts() {
		if err := w.deadTask(ctx, task.id, settleErr); err != nil && w.Logger != nil {
			w.Logger.Error(ctx, "settle_task_dead_write_failed: "+err.Error(),
				obslog.Fields{Stage: "P26", RequestID: task.payload.RequestID})
		}
		if w.Logger != nil {
			w.Logger.Error(ctx, "settle_task_dead: "+settleErr.Error(),
				obslog.Fields{Stage: "P26", RequestID: task.payload.RequestID, Status: 500})
		}
		return
	}
	next := w.now().Add(retryBackoff(task.attempt))
	_, err := w.DB.ExecContext(ctx,
		`UPDATE t_settle_task
		    SET state = ?, next_retry_at = ?, last_error = ?,
		        locked_by = NULL, locked_at = NULL, updated_at = ?
		  WHERE id = ?`,
		TaskStatePending, next, truncateError(settleErr), w.now(), task.id,
	)
	if err != nil && w.Logger != nil {
		w.Logger.Error(ctx, "settle_task_retry_write_failed: "+err.Error(),
			obslog.Fields{Stage: "P26", RequestID: task.payload.RequestID})
	}
}

func (w *Worker) deadTask(ctx context.Context, id int64, cause error) error {
	_, err := w.DB.ExecContext(ctx,
		`UPDATE t_settle_task
		    SET state = ?, last_error = ?, locked_by = NULL, locked_at = NULL, updated_at = ?
		  WHERE id = ?`,
		TaskStateDead, truncateError(cause), w.now(), id,
	)
	return err
}

func (w *Worker) deleteTask(ctx context.Context, id int64) error {
	_, err := w.DB.ExecContext(ctx, `DELETE FROM t_settle_task WHERE id = ?`, id)
	return err
}

// reclaimStale resets RUNNING rows left by a crashed worker. The subsequent
// ledger.Settle is idempotent, so reclaiming cannot double-charge.
func (w *Worker) reclaimStale(ctx context.Context) {
	cutoff := w.now().Add(-staleRunningAfter)
	res, err := w.DB.ExecContext(ctx,
		`UPDATE t_settle_task
		    SET state = ?, locked_by = NULL, locked_at = NULL, updated_at = ?
		  WHERE state = ? AND locked_at IS NOT NULL AND locked_at < ?`,
		TaskStatePending, w.now(), TaskStateRunning, cutoff,
	)
	if err != nil {
		if w.Logger != nil {
			w.Logger.Warn(ctx, "settle_reclaim_failed: "+err.Error(), obslog.Fields{Stage: "P26"})
		}
		return
	}
	if n, _ := res.RowsAffected(); n > 0 && w.Logger != nil {
		w.Logger.Warn(ctx, fmt.Sprintf("settle_reclaimed_stale count=%d", n), obslog.Fields{Stage: "P26"})
	}
}

func (w *Worker) maxAttempts() int {
	if w.MaxAttempts <= 0 {
		return DefaultMaxAttempts
	}
	return w.MaxAttempts
}

func (w *Worker) now() time.Time {
	if w.Clock == nil {
		return time.Now().UTC()
	}
	return w.Clock.Now().UTC()
}

// retryBackoff is 1s, 5s, 25s, 125s, 625s for attempts 1..5.
func retryBackoff(attempt int) time.Duration {
	d := time.Second
	for i := 1; i < attempt; i++ {
		d *= 5
	}
	return d
}

func truncateError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 512 {
		return s[:512]
	}
	return s
}

// Package settle implements P26–P30: settlement enqueue, worker execution,
// prehold release, backlog backpressure, request/attempt log persistence and
// metric accumulation.
//
// 资金真相只在 DB：预扣 / 结算 / 释放的唯一事实源是 t_ledger_entry。Redis 只做
// 拦截与兜底哨兵（GR-02）；本包的任何 Redis 写入失败都不得影响资金结果。
package settle

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/platform/clock"
)

// Task types written to t_settle_task.task_type.
const (
	TaskTypeSettle = "SETTLE"
)

// t_settle_task.state values.
const (
	TaskStatePending = "PENDING"
	TaskStateRunning = "RUNNING"
	TaskStateDead    = "DEAD"
)

// SettlePayload is the JSON body of one SETTLE task. It is deliberately the
// complete settlement input: the worker must not recompute pricing from a
// price snapshot that may have changed after the request finished.
type SettlePayload struct {
	RequestID     string `json:"request_id"`
	UserID        int64  `json:"user_id"`
	ConsumedMicro int64  `json:"consumed_micro"`
	ReleaseReason string `json:"release_reason,omitempty"`
}

// EnqueueParams is the P26 input.
type EnqueueParams struct {
	RequestID     string
	UserID        int64
	ConsumedMicro int64
	ReleaseReason string
}

// Enqueuer writes one PENDING row to t_settle_task.
//
// The caller owns the 20ms budget (TDS P26). This type does not retry: a failed
// enqueue must immediately fall back to the synchronous ledger settlement in
// the same request, otherwise money would be left in HELD state with no task
// to finish it.
type Enqueuer struct {
	DB    *sql.DB
	Clock clock.Clock
}

// NewEnqueuer builds an enqueuer.
func NewEnqueuer(db *sql.DB, clk clock.Clock) *Enqueuer {
	if clk == nil {
		clk = clock.Real{}
	}
	return &Enqueuer{DB: db, Clock: clk}
}

// Enqueue writes the task. The unique key (request_id, task_type) makes a
// duplicate enqueue a no-op: a replay cannot create a second settlement.
func (e *Enqueuer) Enqueue(ctx context.Context, p EnqueueParams) error {
	if e == nil || e.DB == nil {
		return fmt.Errorf("settle: enqueue without database")
	}
	if p.RequestID == "" {
		return fmt.Errorf("settle: enqueue requires request_id")
	}
	if p.ConsumedMicro < 0 {
		return fmt.Errorf("settle: enqueue consumed_micro must be >= 0")
	}
	payload, err := jsonx.Marshal(SettlePayload{
		RequestID:     p.RequestID,
		UserID:        p.UserID,
		ConsumedMicro: p.ConsumedMicro,
		ReleaseReason: p.ReleaseReason,
	})
	if err != nil {
		return fmt.Errorf("settle: encode payload: %w", err)
	}
	now := e.now()
	_, err = e.DB.ExecContext(ctx,
		`INSERT INTO t_settle_task
		   (request_id, user_id, task_type, payload, state, attempt_count,
		    next_retry_at, created_at, updated_at)
		 VALUES (?,?,?,?,?,0,?,?,?)
		 ON DUPLICATE KEY UPDATE updated_at = VALUES(updated_at)`,
		p.RequestID, p.UserID, TaskTypeSettle, []byte(payload), TaskStatePending,
		now, now, now,
	)
	if err != nil {
		return fmt.Errorf("settle: insert task: %w", err)
	}
	return nil
}

func (e *Enqueuer) now() time.Time {
	if e == nil || e.Clock == nil {
		return time.Now().UTC()
	}
	return e.Clock.Now().UTC()
}

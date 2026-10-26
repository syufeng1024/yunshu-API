package settle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/ledger"
	"yunshu.dev/core/internal/platform/clock"
)

const testBalanceMicro = int64(1_000_000)
const testPreholdMicro = int64(100_000)
const testConsumedMicro = int64(60_000)

var testNow = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// newFundedUser inserts a user, its credit account and one refundable batch.
// The batch is what settlement debits; without it the excess would fall into
// t_arrears and the balance assertion would no longer isolate the charge.
func newFundedUser(t *testing.T) int64 {
	t.Helper()
	var (
		userID  int64
		lastErr error
	)
	for attempt := 0; attempt < 5; attempt++ {
		no := fmt.Sprintf("U%s", randomDigits(11))
		res, err := testDB.Exec(
			`INSERT INTO t_user (user_no, password_hash, password_updated_at, created_at, updated_at,
			                     overdraft_limit_micro, status)
			 VALUES (?,?,?,?,?,?,?)`,
			no, "x", testNow, testNow, testNow, 0, "NORMAL",
		)
		if err == nil {
			userID, _ = res.LastInsertId()
			lastErr = nil
			break
		}
		lastErr = err
	}
	if lastErr != nil {
		t.Fatalf("insert user: %v", lastErr)
	}

	if _, err := testDB.Exec(
		`INSERT INTO t_credit_account
		   (user_id, balance_recharge_micro, balance_bonus_micro, balance_micro,
		    overdraft_limit_micro, created_at, updated_at)
		 VALUES (?,0,?,?,0,?,?)`,
		userID, testBalanceMicro, testBalanceMicro, testNow, testNow,
	); err != nil {
		t.Fatalf("insert account: %v", err)
	}

	if _, err := testDB.Exec(
		`INSERT INTO t_credit_batch
		   (user_id, batch_no, batch_type, face_value_micro, remaining_micro, consumed_micro,
		    refundable, effective_at, created_at, updated_at)
		 VALUES (?,?,?,?,?,0,1,?,?,?)`,
		userID, fmt.Sprintf("20260601%s", randomDigits(12)), "RECHARGE",
		testBalanceMicro, testBalanceMicro, testNow, testNow, testNow,
	); err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	return userID
}

func randomDigits(n int) string {
	const digits = "0123456789"
	out := make([]byte, n)
	seed := time.Now().UnixNano()
	for i := range out {
		seed = seed*6364136223846793005 + 1442695040888963407
		out[i] = digits[uint64(seed)>>60%10]
	}
	return string(out)
}

func balanceOf(t *testing.T, userID int64) int64 {
	t.Helper()
	var v int64
	if err := testDB.QueryRow(`SELECT balance_micro FROM t_credit_account WHERE user_id = ?`, userID).Scan(&v); err != nil {
		t.Fatalf("read balance: %v", err)
	}
	return v
}

func countRows(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := testDB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func prehold(t *testing.T, led *ledger.Store, rid string, userID int64) ledger.PreholdResult {
	t.Helper()
	res, err := led.Prehold(context.Background(), ledger.PreholdParams{
		RequestID:      rid,
		UserID:         userID,
		APIKeyID:       1,
		ModelID:        1,
		NodeID:         "settle-test",
		RawEstMicro:    testPreholdMicro,
		EstMicro:       testPreholdMicro,
		MaxHoldRatioBP: 10000,
	})
	if err != nil {
		t.Fatalf("prehold: %v", err)
	}
	return res
}

// A2: enqueue → worker → one CONSUME_SETTLE and one PREHOLD_RELEASE; balance
// moves by exactly consumed. The hold is 100000 and the charge 60000, so 40000
// must come back.
func TestA2EnqueueWorkerSettlesOnce(t *testing.T) {
	ctx := context.Background()
	userID := newFundedUser(t)
	rid := newRequestID(t)
	led := ledger.NewStore(testDB, clock.Real{})

	hold := prehold(t, led, rid, userID)
	if hold.HoldMicro != testPreholdMicro {
		t.Fatalf("hold = %d, want %d", hold.HoldMicro, testPreholdMicro)
	}
	if got := balanceOf(t, userID); got != testBalanceMicro-testPreholdMicro {
		t.Fatalf("after prehold balance = %d, want %d", got, testBalanceMicro-testPreholdMicro)
	}

	enq := NewEnqueuer(testDB, clock.Real{})
	if err := enq.Enqueue(ctx, EnqueueParams{
		RequestID: rid, UserID: userID, ConsumedMicro: testConsumedMicro,
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	worker := NewWorker(testDB, led, clock.Real{}, nil, "settle-test")
	res, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("process one: %v", err)
	}
	if !res.Processed || !res.Settled || res.ConsumedMicro != testConsumedMicro {
		t.Fatalf("worker result = %+v", res)
	}

	finalBalance := balanceOf(t, userID)
	if finalBalance != testBalanceMicro-testConsumedMicro {
		t.Fatalf("after settle balance = %d, want %d (delta %d)",
			finalBalance, testBalanceMicro-testConsumedMicro, finalBalance-testBalanceMicro)
	}
	consumeRows := countRows(t, `SELECT COUNT(*) FROM t_ledger_entry WHERE idem_key = ?`, "settle:"+rid)
	if consumeRows != 1 {
		t.Fatalf("CONSUME_SETTLE rows = %d, want 1", consumeRows)
	}
	releaseRows := countRows(t, `SELECT COUNT(*) FROM t_ledger_entry WHERE idem_key = ?`, "release:"+rid)
	if releaseRows != 1 {
		t.Fatalf("PREHOLD_RELEASE rows = %d, want 1", releaseRows)
	}
	taskRows := countRows(t, `SELECT COUNT(*) FROM t_settle_task WHERE request_id = ?`, rid)
	if taskRows != 0 {
		t.Fatalf("settle task rows = %d, want 0 (successful task is deleted)", taskRows)
	}
	t.Logf("A2 user=%d balance: %d -> %d (delta %d) hold=%d consumed=%d consume_rows=%d release_rows=%d task_rows=%d",
		userID, testBalanceMicro, finalBalance, finalBalance-testBalanceMicro,
		testPreholdMicro, testConsumedMicro, consumeRows, releaseRows, taskRows)
}

// A3: three deliveries of the same request debit once. Each round re-enqueues
// the same request_id, which is what a crashed worker / duplicate dispatch
// looks like on the queue.
func TestA3RepeatedWorkerRunsDebitOnce(t *testing.T) {
	ctx := context.Background()
	userID := newFundedUser(t)
	rid := newRequestID(t)
	led := ledger.NewStore(testDB, clock.Real{})
	prehold(t, led, rid, userID)

	enq := NewEnqueuer(testDB, clock.Real{})
	worker := NewWorker(testDB, led, clock.Real{}, nil, "settle-test")
	for round := 1; round <= 3; round++ {
		if err := enq.Enqueue(ctx, EnqueueParams{
			RequestID: rid, UserID: userID, ConsumedMicro: testConsumedMicro,
		}); err != nil {
			t.Fatalf("round %d enqueue: %v", round, err)
		}
		if _, err := worker.ProcessOne(ctx); err != nil {
			t.Fatalf("round %d process: %v", round, err)
		}
	}
	finalBalance := balanceOf(t, userID)
	if finalBalance != testBalanceMicro-testConsumedMicro {
		t.Fatalf("balance = %d, want %d (one charge)", finalBalance, testBalanceMicro-testConsumedMicro)
	}
	consumeRows := countRows(t, `SELECT COUNT(*) FROM t_ledger_entry WHERE idem_key = ?`, "settle:"+rid)
	if consumeRows != 1 {
		t.Fatalf("CONSUME_SETTLE rows = %d, want exactly 1", consumeRows)
	}
	t.Logf("A3 user=%d rounds=3 balance=%d (delta %d) consume_rows=%d",
		userID, finalBalance, finalBalance-testBalanceMicro, consumeRows)
	// A fourth poll must find no task: success deletes the row.
	res, err := worker.ProcessOne(ctx)
	if err != nil {
		t.Fatalf("fourth process: %v", err)
	}
	if res.Processed {
		t.Fatalf("fourth process handled a task: %+v", res)
	}
}

// A4: a release with consumed=0 for a hold that has no CONSUME_PREHOLD ledger
// row is refused, and the account is untouched. Without this guard the release
// would credit the hold out of nothing.
func TestA4ReleaseWithoutPreholdLedgerIsRejected(t *testing.T) {
	ctx := context.Background()
	userID := newFundedUser(t)
	rid := newRequestID(t)

	// A HELD row without its ledger entry models a corrupted / partially
	// applied prehold.
	if _, err := testDB.Exec(
		`INSERT INTO t_prehold_hold
		   (request_id, user_id, api_key_id, hold_micro, estimated_micro, model_id,
		    state, expires_at, node_id, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		rid, userID, 1, testPreholdMicro, testPreholdMicro, 1,
		"HELD", testNow.Add(time.Hour), "settle-test", testNow,
	); err != nil {
		t.Fatalf("insert orphan hold: %v", err)
	}

	led := ledger.NewStore(testDB, clock.Real{})
	rel := NewReleaser(testDB, led, nil)
	_, err := rel.Release(ctx, ledger.SettleParams{
		RequestID: rid, UserID: userID, ConsumedMicro: 0, ReleaseReason: "UPSTREAM_FAIL",
	})
	if !errors.Is(err, ErrReleaseRejected) {
		t.Fatalf("err = %v, want ErrReleaseRejected", err)
	}
	if got := rel.Rejected(); got != 1 {
		t.Fatalf("rejected counter = %d, want 1", got)
	}
	finalBalance := balanceOf(t, userID)
	if finalBalance != testBalanceMicro {
		t.Fatalf("balance = %d, want %d (no credit added)", finalBalance, testBalanceMicro)
	}
	releaseRows := countRows(t, `SELECT COUNT(*) FROM t_ledger_entry WHERE idem_key = ?`, "release:"+rid)
	if releaseRows != 0 {
		t.Fatalf("PREHOLD_RELEASE rows = %d, want 0", releaseRows)
	}
	t.Logf("A4 user=%d rejected=%d balance=%d (unchanged) release_rows=%d err=%v",
		userID, rel.Rejected(), finalBalance, releaseRows, err)
}

// A6: 5001 PENDING rows put the queue above the GR-03 line. The backlog reader
// is what P08.5 consults before admitting a new request.
func TestA6BacklogOverThreshold(t *testing.T) {
	ctx := context.Background()
	cleanA6(t)

	insertPending(t, 5001)

	backlog := NewBacklog(testDB, clock.Real{})
	n, err := backlog.SettleBacklog(ctx)
	if err != nil {
		t.Fatalf("settle backlog: %v", err)
	}
	if n <= BacklogThreshold {
		t.Fatalf("backlog = %d, want > %d", n, BacklogThreshold)
	}
	exceeded := backlog.Exceeded(ctx)
	if !exceeded {
		t.Fatal("Exceeded() = false above threshold")
	}

	cleanA6(t)
	backlog.Invalidate()
	afterDrain := backlog.Exceeded(ctx)
	if afterDrain {
		t.Fatal("Exceeded() = true after the queue drained")
	}
	t.Logf("A6 pending=5001 backlog=%d threshold=%d exceeded_before=%t exceeded_after_drain=%t",
		n, BacklogThreshold, exceeded, afterDrain)
}

// A6 gateway path: the MySQL-backed backlog reader is the same object the
// gateway wires at startup. P08.5 must reject before taking an in-flight slot,
// and the rejection must carry the 503 code the HTTP layer writes.
func TestA6GatewayRejectsNewRequestOnRealBacklog(t *testing.T) {
	ctx := context.Background()
	cleanA6(t)
	insertPending(t, 5001)
	t.Cleanup(func() { cleanA6(t) })

	d := &gw.Deps{
		Backpressure:  gw.NewBackpressure(nil),
		SettleBacklog: NewBacklog(testDB, clock.Real{}),
	}
	c := &gw.Ctx{RequestID: "a6-gateway"}
	err := gw.NewBackpressureStage(d).Run(ctx, c)
	var se *gw.StageError
	if !errors.As(err, &se) {
		t.Fatalf("err = %T %v, want *gw.StageError", err, err)
	}
	if se.Stage != gw.StageP085 {
		t.Fatalf("stage = %s, want %s", se.Stage, gw.StageP085)
	}
	if se.API == nil || se.API.Code != apierr.CodeBackpressureRejected || se.API.HTTPStatus != 503 {
		t.Fatalf("api = %+v, want %s / 503", se.API, apierr.CodeBackpressureRejected)
	}
	if n := d.Backpressure.Inflight(); n != 0 {
		t.Fatalf("inflight = %d, want 0 (rejected before Acquire)", n)
	}
	if c.Slot != nil {
		t.Fatal("rejected request holds an in-flight slot")
	}
	t.Logf("A6 gateway: stage=%s code=%s status=%d inflight=%d slot=%v",
		se.Stage, se.API.Code, se.API.HTTPStatus, d.Backpressure.Inflight(), c.Slot)
}

func cleanA6(t *testing.T) {
	t.Helper()
	if _, err := testDB.Exec(`DELETE FROM t_settle_task WHERE request_id LIKE 'a6-%'`); err != nil {
		t.Fatalf("clean a6 rows: %v", err)
	}
}

func insertPending(t *testing.T, n int) {
	t.Helper()
	const chunk = 500
	now := time.Now().UTC().Truncate(time.Millisecond)
	for start := 0; start < n; start += chunk {
		end := start + chunk
		if end > n {
			end = n
		}
		var sb strings.Builder
		sb.WriteString(`INSERT INTO t_settle_task
			(request_id, user_id, task_type, payload, state, attempt_count,
			 next_retry_at, created_at, updated_at) VALUES `)
		args := make([]any, 0, (end-start)*8)
		for i := start; i < end; i++ {
			if i > start {
				sb.WriteByte(',')
			}
			sb.WriteString(`(?,?,?,?,?,0,?,?,?)`)
			rid := fmt.Sprintf("a6-%033d", i)
			args = append(args, rid, 1, TaskTypeSettle, []byte(`{}`), TaskStatePending, now, now, now)
		}
		if _, err := testDB.Exec(sb.String(), args...); err != nil {
			t.Fatalf("insert pending tasks %d..%d: %v", start, end, err)
		}
	}
}

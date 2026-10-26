package ledger

import (
	"database/sql"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"
)

var fixtureBase = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// stepClock returns a distinct, monotonic UTC instant on every call. The
// ledger package may not call time.Now, so tests use this as their clock.
type stepClock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func newStepClock() *stepClock {
	return &stepClock{t: fixtureBase, step: time.Millisecond}
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(c.step)
	return c.t
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	if testDB == nil {
		t.Fatal("testDB is nil; TestMain did not run")
	}
	return NewStore(testDB, newStepClock())
}

func execOrFail(t *testing.T, q string, args ...any) sql.Result {
	t.Helper()
	res, err := testDB.Exec(q, args...)
	if err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
	return res
}

func newUser(t *testing.T, overdraftMicro int64) int64 {
	t.Helper()
	for attempt := 0; attempt < 5; attempt++ {
		no := "U" + randomDigits(11)
		res, err := testDB.Exec(
			`INSERT INTO t_user (
				user_no, password_hash, password_updated_at, created_at, updated_at,
				overdraft_limit_micro, status
			) VALUES (?,?,?,?,?,?,?)`,
			no, "x", fixtureBase, fixtureBase, fixtureBase, overdraftMicro, UserStatusNormal,
		)
		if err == nil {
			id, err := res.LastInsertId()
			if err != nil {
				t.Fatalf("user id: %v", err)
			}
			return id
		}
		if !isDuplicate(err) {
			t.Fatalf("insert user: %v", err)
		}
	}
	t.Fatal("could not allocate a unique user_no")
	return 0
}

func newAccount(t *testing.T, userID, bonusMicro, rechargeMicro, overdraftMicro int64) {
	t.Helper()
	execOrFail(t,
		`INSERT INTO t_credit_account (
			user_id, balance_recharge_micro, balance_bonus_micro, balance_micro,
			overdraft_limit_micro, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?)`,
		userID, rechargeMicro, bonusMicro, bonusMicro+rechargeMicro,
		overdraftMicro, fixtureBase, fixtureBase,
	)
}

type batchOpts struct {
	BatchType  string
	Refundable bool
	Face       int64
	Remaining  int64
	CreatedAt  time.Time
	ExpireAt   *time.Time
	SourceID   *int64
}

func newBatch(t *testing.T, userID int64, o batchOpts) int64 {
	t.Helper()
	if o.BatchType == "" {
		if o.Refundable {
			o.BatchType = "RECHARGE"
		} else {
			o.BatchType = "BONUS"
		}
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = fixtureBase
	}
	consumed := o.Face - o.Remaining
	if consumed < 0 {
		t.Fatalf("batch remaining %d exceeds face %d", o.Remaining, o.Face)
	}
	refundable := 0
	if o.Refundable {
		refundable = 1
	}
	for attempt := 0; attempt < 5; attempt++ {
		no := o.CreatedAt.UTC().Format("20060102") + randomDigits(12)
		res, err := testDB.Exec(
			`INSERT INTO t_credit_batch (
				user_id, batch_no, batch_type, source_id, face_value_micro,
				remaining_micro, consumed_micro, refundable, effective_at, expire_at,
				created_at, updated_at
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			userID, no, o.BatchType, o.SourceID, o.Face, o.Remaining, consumed,
			refundable, o.CreatedAt.UTC(), o.ExpireAt, o.CreatedAt.UTC(), o.CreatedAt.UTC(),
		)
		if err == nil {
			id, err := res.LastInsertId()
			if err != nil {
				t.Fatalf("batch id: %v", err)
			}
			return id
		}
		if !isDuplicate(err) {
			t.Fatalf("insert batch: %v", err)
		}
	}
	t.Fatal("could not allocate a unique batch_no")
	return 0
}

type acctState struct {
	Bonus     int64
	Recharge  int64
	Total     int64
	Frozen    int64
	Overdraft int64
	Version   int64
}

func getAccount(t *testing.T, userID int64) acctState {
	t.Helper()
	var s acctState
	err := testDB.QueryRow(
		`SELECT balance_bonus_micro, balance_recharge_micro, balance_micro,
		        frozen_refund_micro, overdraft_limit_micro, version
		   FROM t_credit_account WHERE user_id = ?`, userID,
	).Scan(&s.Bonus, &s.Recharge, &s.Total, &s.Frozen, &s.Overdraft, &s.Version)
	if err != nil {
		t.Fatalf("read account %d: %v", userID, err)
	}
	return s
}

func getUserStatus(t *testing.T, userID int64) string {
	t.Helper()
	var status string
	if err := testDB.QueryRow(`SELECT status FROM t_user WHERE id = ?`, userID).Scan(&status); err != nil {
		t.Fatalf("read user status %d: %v", userID, err)
	}
	return status
}

type entryRow struct {
	ID             int64
	Direction      string
	BizType        string
	AmountBonus    int64
	AmountRecharge int64
	AmountTotal    int64
	BalanceBefore  int64
	BalanceAfter   int64
	BatchID        sql.NullInt64
	IdemKey        string
	Reason         sql.NullString
	RefundOrderID  sql.NullInt64
}

func ledgerEntries(t *testing.T, userID int64, bizType string) []entryRow {
	t.Helper()
	q := `SELECT id, direction, biz_type, amount_bonus_micro, amount_recharge_micro,
	             amount_total_micro, balance_before_micro, balance_after_micro,
	             batch_id, idem_key, reason, refund_order_id
	        FROM t_ledger_entry WHERE user_id = ?`
	args := []any{userID}
	if bizType != "" {
		q += ` AND biz_type = ?`
		args = append(args, bizType)
	}
	q += ` ORDER BY id ASC`
	rows, err := testDB.Query(q, args...)
	if err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	defer rows.Close()
	var out []entryRow
	for rows.Next() {
		var e entryRow
		if err := rows.Scan(
			&e.ID, &e.Direction, &e.BizType, &e.AmountBonus, &e.AmountRecharge,
			&e.AmountTotal, &e.BalanceBefore, &e.BalanceAfter,
			&e.BatchID, &e.IdemKey, &e.Reason, &e.RefundOrderID,
		); err != nil {
			t.Fatalf("scan ledger: %v", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate ledger: %v", err)
	}
	return out
}

func sumEntries(t *testing.T, userID int64, bizType string) int64 {
	t.Helper()
	var sum int64
	if err := testDB.QueryRow(
		`SELECT COALESCE(SUM(amount_total_micro), 0)
		   FROM t_ledger_entry WHERE user_id = ? AND biz_type = ?`,
		userID, bizType,
	).Scan(&sum); err != nil {
		t.Fatalf("sum ledger: %v", err)
	}
	return sum
}

type arrearsRow struct {
	ID      int64
	Amount  int64
	Settled int64
	State   string
}

func getArrears(t *testing.T, requestID string) (arrearsRow, bool) {
	t.Helper()
	var r arrearsRow
	err := testDB.QueryRow(
		`SELECT id, amount_micro, settled_micro, state FROM t_arrears WHERE request_id = ?`,
		requestID,
	).Scan(&r.ID, &r.Amount, &r.Settled, &r.State)
	if err == sql.ErrNoRows {
		return arrearsRow{}, false
	}
	if err != nil {
		t.Fatalf("read arrears: %v", err)
	}
	return r, true
}

func insertHoldDirect(t *testing.T, requestID string, userID, holdMicro int64, state string) {
	t.Helper()
	if state == "" {
		state = HoldStateHeld
	}
	execOrFail(t,
		`INSERT INTO t_prehold_hold (
			request_id, user_id, api_key_id, hold_micro, estimated_micro,
			model_id, state, expires_at, node_id, created_at
		) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		requestID, userID, 1, holdMicro, holdMicro, 1, state,
		fixtureBase.Add(time.Hour), "test-node", fixtureBase,
	)
}

func insertArrearsDirect(t *testing.T, userID int64, requestID string, amount int64, state string) {
	t.Helper()
	if state == "" {
		state = ArrearsStateOpen
	}
	execOrFail(t,
		`INSERT INTO t_arrears (
			user_id, request_id, arrears_no, amount_micro, settled_micro, state,
			overdraft_limit_micro, freeze_flag, created_at, updated_at
		) VALUES (?,?,?,?,0,?,?,1,?,?)`,
		userID, requestID, "A"+randomDigits(25), amount, state, int64(500),
		fixtureBase, fixtureBase,
	)
}

func setUserStatus(t *testing.T, userID int64, status string) {
	t.Helper()
	execOrFail(t, `UPDATE t_user SET status = ? WHERE id = ?`, status, userID)
}

func batchRemaining(t *testing.T, batchID int64) int64 {
	t.Helper()
	var v int64
	if err := testDB.QueryRow(
		`SELECT remaining_micro FROM t_credit_batch WHERE id = ?`, batchID,
	).Scan(&v); err != nil {
		t.Fatalf("read batch %d: %v", batchID, err)
	}
	return v
}

func getHoldState(t *testing.T, requestID string) (string, int64) {
	t.Helper()
	var state string
	var hold int64
	if err := testDB.QueryRow(
		`SELECT state, hold_micro FROM t_prehold_hold WHERE request_id = ?`, requestID,
	).Scan(&state, &hold); err != nil {
		t.Fatalf("read hold: %v", err)
	}
	return state, hold
}

func newRechargeOrder(t *testing.T, userID, amountMicro int64) int64 {
	t.Helper()
	res := execOrFail(t,
		`INSERT INTO t_recharge_order (
			order_no, user_id, order_type, pay_amount_fen, recharge_micro,
			bonus_micro, pay_channel, state, refundable_micro, expire_at,
			created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		"O"+randomDigits(27), userID, "CUSTOM", amountMicro/10000, amountMicro,
		0, "WECHAT", "PAID", 0, fixtureBase.Add(time.Hour),
		fixtureBase, fixtureBase,
	)
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("recharge order id: %v", err)
	}
	return id
}

func newRefundOrder(t *testing.T, userID, rechargeOrderID int64) int64 {
	t.Helper()
	res := execOrFail(t,
		`INSERT INTO t_refund_order (
			refund_no, user_id, recharge_order_id, apply_micro, refund_micro,
			refund_fen, fee_fen, state, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"R"+randomDigits(27), userID, rechargeOrderID, 0, 0, 0, 0,
		"APPLIED", fixtureBase, fixtureBase,
	)
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("refund order id: %v", err)
	}
	return id
}

func newInvoice(t *testing.T, userID, rechargeOrderID int64, state string) int64 {
	t.Helper()
	res := execOrFail(t,
		`INSERT INTO t_invoice_apply (
			apply_no, user_id, recharge_order_id, title_type, title,
			receive_email, amount_fen, state, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		"I"+randomDigits(27), userID, rechargeOrderID, "PERSONAL", "test",
		"test@example.com", 100, state, fixtureBase, fixtureBase,
	)
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("invoice id: %v", err)
	}
	return id
}

// uniqueOrderID gives each test run a fresh recharge-order id. Reusing a
// literal across runs would hit the previous run's recharge:{order_id}
// idempotency key and silently skip the credit.
func uniqueOrderID() int64 {
	s := randomDigits(18)
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 1
	}
	return v
}

func newRequestID(prefix string) string {
	if len(prefix) > 35 {
		prefix = prefix[:35]
	}
	return prefix + "-" + randomDigits(35-len(prefix))
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return v
}

func ptrI64(v int64) *int64 { return &v }

func ptrStr(v string) *string { return &v }

func describeEntries(entries []entryRow) string {
	var s string
	for i, e := range entries {
		s += fmt.Sprintf("%d:%s/%s/batch=%v/amt=%d;", i, e.BizType, e.Direction, e.BatchID, e.AmountTotal)
	}
	return s
}

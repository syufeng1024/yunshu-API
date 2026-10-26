package ledger

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNewStoreDefaults(t *testing.T) {
	s := NewStore(nil, nil)
	if s.Clock == nil || s.Entries == nil {
		t.Fatalf("NewStore did not default clock/entries: %+v", s)
	}
	if s.PreholdTimeout != TxnTimeout || s.SettleTimeout != SettleTxnTimeout || s.DefaultTimeout <= 0 {
		t.Fatalf("unexpected timeouts: %+v", s)
	}
}

func TestWithTxErrorBranches(t *testing.T) {
	if err := (&Store{}).withTx(context.Background(), time.Second, func(context.Context, *sql.Tx) error {
		return nil
	}); !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("nil DB err = %v, want ErrInvalidParam", err)
	}

	// timeout <= 0 falls back to the store default.
	s := NewStore(testDB, newStepClock())
	if err := s.withTx(context.Background(), 0, func(context.Context, *sql.Tx) error {
		return nil
	}); err != nil {
		t.Fatalf("zero-timeout tx: %v", err)
	}

	// A closed pool makes BeginTx fail.
	closed, err := sql.Open("mysql", defaultTestDSN)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	closed.Close()
	broken := &Store{DB: closed, DefaultTimeout: time.Second}
	if err := broken.withTx(context.Background(), time.Second, func(context.Context, *sql.Tx) error {
		return nil
	}); err == nil {
		t.Fatal("closed pool tx must fail")
	}
}

func TestAppendEntryBranches(t *testing.T) {
	gen := NewEntryNoGenerator()
	tx := mustBegin(t)
	userID := int64(999999999)

	base := Entry{
		UserID: userID, Direction: DirectionCredit, BizType: BizTypeRecharge,
		AmountRechargeMicro: 100, AmountTotalMicro: 100,
		BalanceBeforeMicro: 0, BalanceAfterMicro: 100,
		IdemKey: "branch:" + randomDigits(12),
	}
	cases := []struct {
		name    string
		mutate  func(*Entry)
		wantErr bool
	}{
		{"valid recharge", func(e *Entry) {}, false},
		{"valid bonus", func(e *Entry) {
			e.AmountBonusMicro = 100
			e.AmountRechargeMicro = 0
		}, false},
		{"valid mixed", func(e *Entry) {
			e.AmountBonusMicro = 50
			e.AmountRechargeMicro = 50
		}, false},
		{"zero created_at", func(e *Entry) { e.CreatedAt = time.Time{} }, false},
		{"missing user", func(e *Entry) { e.UserID = 0 }, true},
		{"missing idem", func(e *Entry) { e.IdemKey = "" }, true},
		{"amount mismatch", func(e *Entry) { e.AmountTotalMicro = 99 }, true},
		{"balance mismatch", func(e *Entry) { e.BalanceAfterMicro = 99 }, true},
		{"credit negative", func(e *Entry) {
			e.AmountRechargeMicro = -100
			e.AmountTotalMicro = -100
			e.BalanceAfterMicro = -100
		}, true},
		{"debit positive", func(e *Entry) {
			e.Direction = DirectionDebit
		}, true},
		{"unknown direction", func(e *Entry) { e.Direction = "SIDEWAYS" }, true},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := base
			e.IdemKey = "branch:" + randomDigits(12) + ":" + itoa(int64(i))
			c.mutate(&e)
			_, err := AppendEntry(context.Background(), tx, gen, fixtureBase, e)
			if c.wantErr {
				if !errors.Is(err, ErrInvalidParam) {
					t.Fatalf("err = %v, want ErrInvalidParam", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid entry: %v", err)
			}
		})
	}

	if _, err := AppendEntry(context.Background(), tx, nil, fixtureBase, base); !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("nil generator err = %v, want ErrInvalidParam", err)
	}
	if isDuplicateIdem(errors.New("not mysql")) {
		t.Fatal("non-MySQL error must not be reported as a duplicate idem")
	}
}

func TestEntryNoGeneratorBackwardsAndOverflow(t *testing.T) {
	g := NewEntryNoGenerator()
	later := g.Next(fixtureBase.Add(time.Second))
	backwards := g.Next(fixtureBase)
	if backwards <= later {
		t.Fatalf("clock backwards produced a smaller entry_no: %q <= %q", backwards, later)
	}

	g.mu.Lock()
	g.lastMS = fixtureBase.UnixMilli()
	for i := range g.last {
		g.last[i] = 0xff
	}
	g.mu.Unlock()
	no := g.Next(fixtureBase)
	if len(no) != 26 {
		t.Fatalf("overflow entry_no length = %d", len(no))
	}
}

func TestRandomDigitsEdge(t *testing.T) {
	if got := randomDigits(0); got != "" {
		t.Fatalf("randomDigits(0)=%q, want empty", got)
	}
	if got := randomDigits(-1); got != "" {
		t.Fatalf("randomDigits(-1)=%q, want empty", got)
	}
	if got := randomDigits(6); len(got) != 6 {
		t.Fatalf("randomDigits(6)=%q, want 6 digits", got)
	}
}

func TestLockAccountErrorBranches(t *testing.T) {
	// Insert the corrupted row BEFORE taking any FOR UPDATE gap lock; a
	// missing-key lock on the unique index would otherwise block this insert
	// from a second connection.
	userID := newUser(t, 0)
	execOrFail(t,
		`INSERT INTO t_credit_account (
			user_id, balance_recharge_micro, balance_bonus_micro, balance_micro,
			overdraft_limit_micro, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?)`,
		userID, 10, 0, 99, 0, fixtureBase, fixtureBase,
	)

	tx := mustBegin(t)
	if _, err := LockAccount(context.Background(), tx, 999999998); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("missing account err = %v, want ErrNoAccount", err)
	}
	if _, err := LockAccount(context.Background(), tx, userID); err == nil {
		t.Fatal("corrupted balance_micro must be rejected")
	}
	if _, err := LockAccount(context.Background(), tx, 0); !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("zero user err = %v, want ErrInvalidParam", err)
	}
}

func TestAccountApplyGuards(t *testing.T) {
	userID := newUser(t, 0)
	newAccount(t, userID, 0, 100, 0)

	tx := mustBegin(t)
	acct, err := LockAccount(context.Background(), tx, userID)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	if err := acct.Apply(context.Background(), tx, BalanceDelta{ConsumedMicro: -1}, 0, nil, fixtureBase); !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("negative counter err = %v, want ErrInvalidParam", err)
	}
	if err := acct.Apply(context.Background(), tx, BalanceDelta{RechargeMicro: -101}, 0, nil, fixtureBase); !errors.Is(err, ErrBelowOverdraft) {
		t.Fatalf("below floor err = %v, want ErrBelowOverdraft", err)
	}

	stale := *acct
	if err := acct.Apply(context.Background(), tx, BalanceDelta{}, 0, nil, fixtureBase); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if err := stale.Apply(context.Background(), tx, BalanceDelta{}, 0, nil, fixtureBase); !errors.Is(err, ErrCASConflict) {
		t.Fatalf("stale apply err = %v, want ErrCASConflict", err)
	}
}

func TestPreholdValidationBranches(t *testing.T) {
	s := newTestStore(t)
	valid := PreholdParams{
		RequestID: newRequestID("valid"), UserID: 999999997, APIKeyID: 1, ModelID: 1,
		NodeID: "test", RawEstMicro: 1, EstMicro: 1, MaxHoldRatioBP: 5000,
		ExpiresAt: s.Clock.Now().Add(time.Hour),
	}
	cases := []struct {
		name   string
		mutate func(*PreholdParams)
	}{
		{"missing request", func(p *PreholdParams) { p.RequestID = "" }},
		{"missing user", func(p *PreholdParams) { p.UserID = 0 }},
		{"missing key", func(p *PreholdParams) { p.APIKeyID = 0 }},
		{"missing model", func(p *PreholdParams) { p.ModelID = 0 }},
		{"missing node", func(p *PreholdParams) { p.NodeID = "" }},
		{"negative raw", func(p *PreholdParams) { p.RawEstMicro = -1 }},
		{"negative est", func(p *PreholdParams) { p.EstMicro = -1 }},
		{"ratio too high", func(p *PreholdParams) { p.MaxHoldRatioBP = 10001 }},
		{"ratio negative", func(p *PreholdParams) { p.MaxHoldRatioBP = -1 }},
		{"expires in past", func(p *PreholdParams) { p.ExpiresAt = fixtureBase.Add(-time.Hour) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := valid
			c.mutate(&p)
			if _, err := s.Prehold(context.Background(), p); !errors.Is(err, ErrInvalidParam) {
				t.Fatalf("err = %v, want ErrInvalidParam", err)
			}
		})
	}

	if got := HoldMicro(1000, 1000, 20000); got != 1000 {
		t.Fatalf("ratio >10000 clamp = %d, want 1000", got)
	}
}

func TestGetPreholdDirect(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 0)
	requestID := newRequestID("gethold")
	insertHoldDirect(t, requestID, userID, 1234, HoldStateHeld)

	got, err := s.getPrehold(context.Background(), requestID)
	if err != nil {
		t.Fatalf("getPrehold: %v", err)
	}
	if !got.Idempotent || got.HoldMicro != 1234 {
		t.Fatalf("getPrehold = %+v, want idempotent/1234", got)
	}
	if _, err := s.getPrehold(context.Background(), "missing-"+randomDigits(8)); err == nil {
		t.Fatal("missing hold must return an error")
	}
}

func TestSettleStateBranches(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 0)
	suspended := newRequestID("susp")
	insertHoldDirect(t, suspended, userID, 100, HoldStateSuspended)
	if _, err := s.Settle(context.Background(), SettleParams{RequestID: suspended, UserID: userID}); !errors.Is(err, ErrPreholdSuspended) {
		t.Fatalf("suspended err = %v, want ErrPreholdSuspended", err)
	}

	broken := newRequestID("broken")
	insertHoldDirect(t, broken, userID, 100, "BROKEN")
	if _, err := s.Settle(context.Background(), SettleParams{RequestID: broken, UserID: userID}); !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("unknown state err = %v, want ErrInvalidParam", err)
	}

	// Hold says 1,000 but the prehold ledger says -999: refuse to release.
	mismatch := newRequestID("mismatch")
	insertHoldDirect(t, mismatch, userID, 1000, HoldStateHeld)
	tx := mustBegin(t)
	if _, err := AppendEntry(context.Background(), tx, s.Entries, fixtureBase, Entry{
		UserID: userID, Direction: DirectionDebit, BizType: BizTypeConsumePrehold,
		AmountRechargeMicro: -999, AmountTotalMicro: -999,
		BalanceBeforeMicro: 1000, BalanceAfterMicro: 1,
		RequestID: &mismatch, IdemKey: "prehold:" + mismatch,
	}); err != nil {
		t.Fatalf("seed mismatched prehold: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit mismatched prehold: %v", err)
	}
	if _, err := s.Settle(context.Background(), SettleParams{RequestID: mismatch, UserID: userID}); !errors.Is(err, ErrPreholdLedgerMissing) {
		t.Fatalf("mismatch err = %v, want ErrPreholdLedgerMissing", err)
	}

	if _, err := s.Settle(context.Background(), SettleParams{RequestID: "", ConsumedMicro: 0}); !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("empty request err = %v, want ErrInvalidParam", err)
	}
	if _, err := s.Settle(context.Background(), SettleParams{RequestID: newRequestID("neg"), ConsumedMicro: -1}); !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("negative consumed err = %v, want ErrInvalidParam", err)
	}
}

func TestSettledResultDirect(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 10000, 500000)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 10000, Remaining: 10000})

	requestID := newRequestID("settleddirect")
	preholdFor(t, s, userID, 1000, 1000, requestID)
	if _, err := s.Settle(context.Background(), SettleParams{RequestID: requestID, UserID: userID, ConsumedMicro: 500}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	got, err := s.settledResult(context.Background(), requestID)
	if err != nil {
		t.Fatalf("settledResult: %v", err)
	}
	if !got.Idempotent || got.ConsumedMicro != 500 || got.State != HoldStateSettled {
		t.Fatalf("settledResult = %+v, want idempotent/500/SETTLED", got)
	}

	// Released holds are idempotent too.
	releasedID := newRequestID("releaseddirect")
	preholdFor(t, s, userID, 1000, 1000, releasedID)
	if _, err := s.Settle(context.Background(), SettleParams{RequestID: releasedID, UserID: userID, ConsumedMicro: 0}); err != nil {
		t.Fatalf("release: %v", err)
	}
	again, err := s.Settle(context.Background(), SettleParams{RequestID: releasedID, UserID: userID, ConsumedMicro: 0})
	if err != nil {
		t.Fatalf("second release: %v", err)
	}
	if !again.Idempotent || again.State != HoldStateReleased {
		t.Fatalf("released replay = %+v, want idempotent/RELEASED", again)
	}
}

func TestDeductEdgeBranches(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 0)
	tx := mustBegin(t)
	if _, err := s.deductBatchesTx(context.Background(), tx, userID, -1, 0, fixtureBase, DeductPolicyFIFOExpiry); !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("negative amount err = %v, want ErrInvalidParam", err)
	}
	out, err := s.deductBatchesTx(context.Background(), tx, userID, 0, 0, fixtureBase, DeductPolicyFIFOExpiry)
	if err != nil || out.BatchBackedMicro != 0 {
		t.Fatalf("zero amount = %+v / %v", out, err)
	}
	out, err = s.deductBatchesTx(context.Background(), tx, userID, 100, 0, fixtureBase, DeductPolicyFIFOExpiry)
	if err != nil || out.BatchBackedMicro != 0 {
		t.Fatalf("zero cap = %+v / %v", out, err)
	}
	out, err = s.deductBatchesTx(context.Background(), tx, userID, 100, -1, fixtureBase, DeductPolicyFIFOExpiry)
	if err != nil || out.LeftMicro != 100 {
		t.Fatalf("unlimited cap with no batches = %+v / %v", out, err)
	}

	if _, err := s.Debit(context.Background(), DebitParams{UserID: 0, AmountMicro: 1, RequestID: "x"}); !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("invalid debit err = %v, want ErrInvalidParam", err)
	}
	zero, err := s.Debit(context.Background(), DebitParams{UserID: userID, AmountMicro: 0, RequestID: newRequestID("zero")})
	if err != nil || zero.DeductedMicro != 0 {
		t.Fatalf("zero debit = %+v / %v", zero, err)
	}
}

func TestInsertRechargeBatchExplicitDuplicate(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 0)
	tx := mustBegin(t)
	batchNo := "EXPL" + randomDigits(16)
	p := RechargeParams{UserID: userID, AmountMicro: 100, BatchNo: batchNo}
	if _, err := s.insertRechargeBatch(context.Background(), tx, p, fixtureBase); err != nil {
		t.Fatalf("first explicit batch: %v", err)
	}
	if _, err := s.insertRechargeBatch(context.Background(), tx, p, fixtureBase); err == nil {
		t.Fatal("duplicate explicit batch_no must fail")
	}
}

func TestOffsetArrearsNoOpen(t *testing.T) {
	userID := newUser(t, 0)
	tx := mustBegin(t)
	offset, rows, err := offsetArrearsTx(context.Background(), tx, userID, 0, fixtureBase)
	if err != nil || offset != 0 || len(rows) != 0 {
		t.Fatalf("zero offset = %d/%v/%v", offset, rows, err)
	}
	offset, rows, err = offsetArrearsTx(context.Background(), tx, userID, 100, fixtureBase)
	if err != nil || offset != 0 || len(rows) != 0 {
		t.Fatalf("no-open offset = %d/%v/%v", offset, rows, err)
	}
}

func TestQuoteRefundBranches(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.QuoteRefund(context.Background(), RefundQuoteParams{UserID: 0}); !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("invalid user err = %v, want ErrInvalidParam", err)
	}

	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 79000000, 500000)
	orderID := newRechargeOrder(t, userID, 79000000)
	newBatch(t, userID, batchOpts{
		Refundable: true, Face: 79000000, Remaining: 79000000,
		CreatedAt: fixtureBase, SourceID: &orderID,
	})
	// No order filter: sums all recharge batches.
	q, err := s.QuoteRefund(context.Background(), RefundQuoteParams{UserID: userID})
	if err != nil {
		t.Fatalf("quote without order: %v", err)
	}
	if q.RefundableMicro != 79000000 {
		t.Fatalf("refundable without order = %d, want 79000000", q.RefundableMicro)
	}

	// Frozen refund money is not quotable again.
	execOrFail(t, `UPDATE t_credit_account SET frozen_refund_micro = ? WHERE user_id = ?`, 10000000, userID)
	q, err = s.QuoteRefund(context.Background(), RefundQuoteParams{UserID: userID})
	if err != nil {
		t.Fatalf("quote with frozen: %v", err)
	}
	if q.RefundableMicro != 69000000 {
		t.Fatalf("frozen-adjusted refundable = %d, want 69000000", q.RefundableMicro)
	}

	// A settlement without batch_id flags the quote for finance review.
	requestID := newRequestID("nobatchid")
	tx := mustBegin(t)
	if _, err := AppendEntry(context.Background(), tx, s.Entries, fixtureBase, Entry{
		UserID: userID, Direction: DirectionDebit, BizType: BizTypeConsumeSettle,
		AmountRechargeMicro: -1, AmountTotalMicro: -1,
		BalanceBeforeMicro: 1, BalanceAfterMicro: 0,
		RequestID: &requestID, IdemKey: "settle:" + requestID + ":noid",
	}); err != nil {
		t.Fatalf("seed no-batch settlement: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit no-batch settlement: %v", err)
	}
	q, err = s.QuoteRefund(context.Background(), RefundQuoteParams{UserID: userID})
	if err != nil {
		t.Fatalf("quote with missing batch detail: %v", err)
	}
	if !q.BatchDetailEstimated {
		t.Fatal("a CONSUME_SETTLE row without batch_id must set BatchDetailEstimated")
	}
}

func TestRefundValidationBranches(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Refund(context.Background(), RefundParams{}); !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("invalid refund err = %v, want ErrInvalidParam", err)
	}
}

func TestMarkHoldInvalidState(t *testing.T) {
	tx := mustBegin(t)
	if err := markHold(context.Background(), tx, "x", "NOPE", fixtureBase, ""); !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("err = %v, want ErrInvalidParam", err)
	}
	if err := markHold(context.Background(), tx, "missing-"+randomDigits(8), HoldStateSettled, fixtureBase, ""); !errors.Is(err, ErrCASConflict) {
		t.Fatalf("missing hold transition err = %v, want ErrCASConflict", err)
	}
}

func TestSettledResultClampsNegativeConsumed(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 0)
	requestID := newRequestID("clamp")
	insertHoldDirect(t, requestID, userID, 100, HoldStateSettled)

	tx := mustBegin(t)
	if _, err := AppendEntry(context.Background(), tx, s.Entries, fixtureBase, Entry{
		UserID: userID, Direction: DirectionCredit, BizType: BizTypeConsumeSettle,
		AmountRechargeMicro: 50, AmountTotalMicro: 50,
		BalanceBeforeMicro: 0, BalanceAfterMicro: 50,
		RequestID: &requestID, IdemKey: "settle:" + requestID + ":positive",
	}); err != nil {
		t.Fatalf("seed positive settle: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, err := s.settledResult(context.Background(), requestID)
	if err != nil {
		t.Fatalf("settledResult: %v", err)
	}
	if got.ConsumedMicro != 0 {
		t.Fatalf("clamped consumed = %d, want 0", got.ConsumedMicro)
	}
}

func TestRechargeExplicitBatchNoConflict(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 1000, 500000)
	batchNo := "CONFLICT" + randomDigits(12)
	execOrFail(t,
		`INSERT INTO t_credit_batch (
			user_id, batch_no, batch_type, face_value_micro, remaining_micro,
			consumed_micro, refundable, effective_at, expire_at, created_at, updated_at
		) VALUES (?,?,?,?,?,0,1,?,NULL,?,?)`,
		userID, batchNo, "RECHARGE", 1, 1, fixtureBase, fixtureBase, fixtureBase,
	)
	_, err := s.Recharge(context.Background(), RechargeParams{
		UserID: userID, AmountMicro: 100, RechargeOrderID: uniqueOrderID(), BatchNo: batchNo,
	})
	if err == nil {
		t.Fatal("explicit duplicate batch_no must fail the recharge")
	}
}

func TestLoadRechargeStateOutsideDirect(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 1000, 500000)

	res, err := s.loadRechargeStateOutside(context.Background(), userID, 500)
	if err != nil {
		t.Fatalf("loadRechargeStateOutside: %v", err)
	}
	if !res.Idempotent || res.RechargeMicro != 500 || res.BalanceAfterMicro != 1000 {
		t.Fatalf("result = %+v, want idempotent/500/1000", res)
	}
}

func TestInsertArrearsEdge(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500)
	tx := mustBegin(t)
	if _, err := insertArrearsTx(context.Background(), tx, s.Entries, fixtureBase, userID, "req", 0, 500); err != nil {
		t.Fatalf("zero arrears: %v", err)
	}
	if _, err := insertArrearsTx(context.Background(), tx, s.Entries, fixtureBase, userID, "", 100, 500); !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("empty request err = %v, want ErrInvalidParam", err)
	}

	requestID := newRequestID("dup-arr")
	first, err := insertArrearsTx(context.Background(), tx, s.Entries, fixtureBase, userID, requestID, 100, 500)
	if err != nil {
		t.Fatalf("first arrears: %v", err)
	}
	second, err := insertArrearsTx(context.Background(), tx, s.Entries, fixtureBase, userID, requestID, 100, 500)
	if err != nil {
		t.Fatalf("duplicate arrears: %v", err)
	}
	if first != second {
		t.Fatalf("duplicate arrears id = %d, want existing %d", second, first)
	}
}

func TestOffsetArrearsDueZeroBranch(t *testing.T) {
	userID := newUser(t, 500)
	first := newRequestID("due0")
	insertArrearsDirect(t, userID, first, 100, ArrearsStateOpen)
	execOrFail(t, `UPDATE t_arrears SET settled_micro = 100 WHERE request_id = ?`, first)
	second := newRequestID("due200")
	insertArrearsDirect(t, userID, second, 200, ArrearsStateOpen)

	tx := mustBegin(t)
	offset, rows, err := offsetArrearsTx(context.Background(), tx, userID, 100, fixtureBase)
	if err != nil {
		t.Fatalf("offset: %v", err)
	}
	if offset != 100 || len(rows) != 1 || rows[0].AmountMicro != 100 {
		t.Fatalf("offset = %d rows=%v, want 100 and one 100 row", offset, rows)
	}
}

func TestPreholdZeroHold(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500000)
	newAccount(t, userID, 0, 1000, 500000)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 1000, Remaining: 1000})

	requestID := newRequestID("zerohold")
	res, err := s.Prehold(context.Background(), PreholdParams{
		RequestID: requestID, UserID: userID, APIKeyID: 1, ModelID: 1, NodeID: "test",
		RawEstMicro: 0, EstMicro: 0, MaxHoldRatioBP: 5000,
		ExpiresAt: s.Clock.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Prehold: %v", err)
	}
	if res.HoldMicro != 0 {
		t.Fatalf("hold = %d, want 0", res.HoldMicro)
	}
	if st := getAccount(t, userID); st.Total != 1000 {
		t.Fatalf("balance = %d, want 1000", st.Total)
	}
}

func TestQuoteRefundNoAccount(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.QuoteRefund(context.Background(), RefundQuoteParams{UserID: 999999996}); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("err = %v, want ErrNoAccount", err)
	}
}

func TestReasonPtr(t *testing.T) {
	if reasonPtr("") != nil {
		t.Fatal("empty reason must be nil")
	}
	if got := reasonPtr("UPSTREAM_FAIL"); got == nil || !strings.Contains(*got, "UPSTREAM") {
		t.Fatalf("reason = %v", got)
	}
}

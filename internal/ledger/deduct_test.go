package ledger

import (
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

// A6: bonus A (earlier expiry) -> bonus B (later expiry) -> recharge C (FIFO).
// One debit spanning three batches must produce three DEBIT rows, each with
// its batch_id, and their amounts must sum to the total.
func TestA6BatchOrderAndPerBatchEntries(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 0)
	newAccount(t, userID, 2000, 1000, 0)

	expireA := fixtureBase.Add(24 * time.Hour)
	expireB := fixtureBase.Add(48 * time.Hour)
	batchA := newBatch(t, userID, batchOpts{
		Refundable: false, Face: 1000, Remaining: 1000,
		CreatedAt: fixtureBase, ExpireAt: &expireA,
	})
	batchB := newBatch(t, userID, batchOpts{
		Refundable: false, Face: 1000, Remaining: 1000,
		CreatedAt: fixtureBase.Add(-time.Hour), ExpireAt: &expireB,
	})
	batchC := newBatch(t, userID, batchOpts{
		Refundable: true, Face: 1000, Remaining: 1000,
		CreatedAt: fixtureBase.Add(-2 * time.Hour),
	})

	requestID := newRequestID("a6")
	res, err := s.Debit(t.Context(), DebitParams{
		UserID: userID, AmountMicro: 3000, RequestID: requestID, BizType: BizTypeConsumeSettle,
	})
	if err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if res.BatchBackedMicro != 3000 {
		t.Fatalf("batch backed = %d, want 3000", res.BatchBackedMicro)
	}

	entries := ledgerEntries(t, userID, BizTypeConsumeSettle)
	if len(entries) != 3 {
		t.Fatalf("entries = %d (%s), want 3", len(entries), describeEntries(entries))
	}
	wantOrder := []int64{batchA, batchB, batchC}
	for i, e := range entries {
		if e.Direction != DirectionDebit {
			t.Fatalf("entry %d direction = %s, want DEBIT", i, e.Direction)
		}
		if !e.BatchID.Valid || e.BatchID.Int64 != wantOrder[i] {
			t.Fatalf("entry %d batch = %v, want %d (order A,B,C)", i, e.BatchID, wantOrder[i])
		}
		if e.AmountTotal != -1000 {
			t.Fatalf("entry %d amount = %d, want -1000", i, e.AmountTotal)
		}
	}
	var sum int64
	for _, e := range entries {
		sum += e.AmountTotal
	}
	if sum != -3000 {
		t.Fatalf("batch entry sum = %d, want -3000", sum)
	}
	if st := getAccount(t, userID); st.Total != 0 {
		t.Fatalf("balance = %d, want 0", st.Total)
	}
	t.Logf("A6: debits=%s batch_order=[%d,%d,%d] sum=%d balance_after=%d",
		describeEntries(entries), batchA, batchB, batchC, sum, getAccount(t, userID).Total)
}

// Same expiry: created_at ASC decides.
func TestBonusSameExpiryUsesCreatedAt(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 0)
	newAccount(t, userID, 2000, 0, 0)

	expire := fixtureBase.Add(24 * time.Hour)
	older := newBatch(t, userID, batchOpts{
		Refundable: false, Face: 1000, Remaining: 1000,
		CreatedAt: fixtureBase.Add(-2 * time.Hour), ExpireAt: &expire,
	})
	newer := newBatch(t, userID, batchOpts{
		Refundable: false, Face: 1000, Remaining: 1000,
		CreatedAt: fixtureBase.Add(-time.Hour), ExpireAt: &expire,
	})

	if _, err := s.Debit(t.Context(), DebitParams{
		UserID: userID, AmountMicro: 2000, RequestID: newRequestID("sameexp"),
	}); err != nil {
		t.Fatalf("Debit: %v", err)
	}
	entries := ledgerEntries(t, userID, BizTypeConsumeSettle)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].BatchID.Int64 != older || entries[1].BatchID.Int64 != newer {
		t.Fatalf("order = %v then %v, want older(%d) then newer(%d)",
			entries[0].BatchID, entries[1].BatchID, older, newer)
	}
}

func TestExpiredBonusBatchIsNotDeducted(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 0)
	newAccount(t, userID, 0, 1000, 0)

	past := fixtureBase.Add(-time.Hour)
	expired := newBatch(t, userID, batchOpts{
		Refundable: false, Face: 1000, Remaining: 1000,
		CreatedAt: fixtureBase.Add(-2 * time.Hour), ExpireAt: &past,
	})
	valid := newBatch(t, userID, batchOpts{
		Refundable: true, Face: 1000, Remaining: 1000,
		CreatedAt: fixtureBase.Add(-time.Hour),
	})

	if _, err := s.Debit(t.Context(), DebitParams{
		UserID: userID, AmountMicro: 1000, RequestID: newRequestID("expired"),
	}); err != nil {
		t.Fatalf("Debit: %v", err)
	}
	entries := ledgerEntries(t, userID, BizTypeConsumeSettle)
	if len(entries) != 1 || entries[0].BatchID.Int64 != valid {
		t.Fatalf("entries = %s, want only valid batch %d (expired %d skipped)",
			describeEntries(entries), valid, expired)
	}
}

// The FOR UPDATE + CAS path must serialise two concurrent debits. With 1,000
// available, two 600 debits: exactly one succeeds and the balance never goes
// below zero.
func TestConcurrentDebitsDoNotOverdraw(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 0)
	newAccount(t, userID, 0, 1000, 0)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 1000, Remaining: 1000})

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.Debit(t.Context(), DebitParams{
				UserID: userID, AmountMicro: 600, RequestID: newRequestID("conc"),
			})
		}(i)
	}
	wg.Wait()

	success := 0
	for _, err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrInsufficientCredits):
			// expected for the loser
		default:
			t.Fatalf("unexpected concurrent debit error: %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("successful debits = %d, want exactly 1; errors=%v", success, errs)
	}
	st := getAccount(t, userID)
	if st.Total != 400 {
		t.Fatalf("balance = %d, want 400", st.Total)
	}
	if got := sumEntries(t, userID, BizTypeConsumeSettle); got != -600 {
		t.Fatalf("entry sum = %d, want -600", got)
	}
}

func TestDebitRejectsAmountAboveAvailable(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 500)
	newAccount(t, userID, 0, 100, 500)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 100, Remaining: 100})

	_, err := s.Debit(t.Context(), DebitParams{
		UserID: userID, AmountMicro: 101, RequestID: newRequestID("over"),
	})
	if !errors.Is(err, ErrInsufficientCredits) {
		t.Fatalf("err = %v, want ErrInsufficientCredits", err)
	}
	if st := getAccount(t, userID); st.Total != 100 || st.Version != 0 {
		t.Fatalf("account moved: %+v", st)
	}
}

// The batch update, ledger append and account CAS share one transaction. A
// duplicate ledger key after the batch update must roll the batch and the
// account back together.
func TestDebitLedgerFailureRollsBackBatchAndAccount(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 0)
	newAccount(t, userID, 0, 1000, 0)
	batchID := newBatch(t, userID, batchOpts{Refundable: true, Face: 1000, Remaining: 1000})

	requestID := newRequestID("rollback")
	// Seed the exact idem key Debit will try to insert.
	tx, err := testDB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := AppendEntry(t.Context(), tx, s.Entries, fixtureBase, Entry{
		UserID: userID, Direction: DirectionDebit, BizType: BizTypeConsumeSettle,
		AmountRechargeMicro: -1, AmountTotalMicro: -1,
		BalanceBeforeMicro: 1, BalanceAfterMicro: 0,
		BatchID: &batchID, RequestID: &requestID,
		IdemKey: "CONSUME_SETTLE:" + requestID + ":" + itoa(batchID),
	}); err != nil {
		tx.Rollback()
		t.Fatalf("seed entry: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	_, err = s.Debit(t.Context(), DebitParams{
		UserID: userID, AmountMicro: 1000, RequestID: requestID,
	})
	if !errors.Is(err, ErrDuplicateIdem) {
		t.Fatalf("err = %v, want ErrDuplicateIdem", err)
	}
	if st := getAccount(t, userID); st.Total != 1000 || st.Version != 0 {
		t.Fatalf("account changed despite rolled-back ledger: %+v", st)
	}
	if got := batchRemaining(t, batchID); got != 1000 {
		t.Fatalf("batch remaining = %d, want 1000", got)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

func TestDebitRejectsInsufficientBatches(t *testing.T) {
	s := newTestStore(t)
	userID := newUser(t, 0)
	// The account says 1,000 but only 500 is backed by a batch: a data
	// inconsistency must fail loudly, never silently create 500 of overdraft.
	newAccount(t, userID, 0, 1000, 0)
	newBatch(t, userID, batchOpts{Refundable: true, Face: 500, Remaining: 500})

	_, err := s.Debit(t.Context(), DebitParams{
		UserID: userID, AmountMicro: 1000, RequestID: newRequestID("nobatch"),
	})
	if !errors.Is(err, ErrInsufficientBatches) {
		t.Fatalf("err = %v, want ErrInsufficientBatches", err)
	}
}

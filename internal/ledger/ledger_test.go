package ledger

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestEntryNoIsULIDShapedAndMonotonic(t *testing.T) {
	g := NewEntryNoGenerator()
	ms := fixtureBase
	prev := ""
	seen := make(map[string]struct{}, 2000)
	for i := 0; i < 2000; i++ {
		no := g.Next(ms)
		if len(no) != 26 {
			t.Fatalf("entry_no %q length = %d, want 26", no, len(no))
		}
		if _, ok := seen[no]; ok {
			t.Fatalf("duplicate entry_no %q", no)
		}
		seen[no] = struct{}{}
		if prev != "" && no <= prev {
			t.Fatalf("entry_no not increasing: %q then %q", prev, no)
		}
		prev = no
	}

	// A later millisecond also produces a larger value.
	later := g.Next(ms.Add(time.Second))
	if later <= prev {
		t.Fatalf("later entry_no %q <= previous %q", later, prev)
	}
}

func TestEntryNoGeneratorConcurrentUnique(t *testing.T) {
	g := NewEntryNoGenerator()
	const workers, perWorker = 16, 100
	results := make(chan string, workers*perWorker)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				results <- g.Next(fixtureBase)
			}
		}()
	}
	wg.Wait()
	close(results)
	seen := make(map[string]struct{}, workers*perWorker)
	for no := range results {
		if _, ok := seen[no]; ok {
			t.Fatalf("duplicate entry_no under concurrency: %q", no)
		}
		seen[no] = struct{}{}
	}
	if len(seen) != workers*perWorker {
		t.Fatalf("unique entry_no = %d, want %d", len(seen), workers*perWorker)
	}
}

func TestAppendEntryValidatesArithmetic(t *testing.T) {
	now := fixtureBase
	g := NewEntryNoGenerator()
	userID := newUser(t, 500000)

	if _, err := AppendEntry(context.Background(), mustBegin(t), g, now, Entry{
		UserID: userID, Direction: DirectionCredit, BizType: BizTypeRecharge,
		AmountRechargeMicro: 100, AmountTotalMicro: 99,
		BalanceBeforeMicro: 0, BalanceAfterMicro: 99, IdemKey: "bad:total",
	}); !errors.Is(err, ErrInvalidParam) {
		t.Fatalf("amount mismatch err = %v, want ErrInvalidParam", err)
	}
}

func TestAppendEntryDuplicateIdem(t *testing.T) {
	now := fixtureBase
	g := NewEntryNoGenerator()
	userID := newUser(t, 500000)
	idem := "dup:" + randomDigits(12)

	insert := func() error {
		tx, err := testDB.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()
		_, err = AppendEntry(context.Background(), tx, g, now, Entry{
			UserID: userID, Direction: DirectionCredit, BizType: BizTypeRecharge,
			AmountRechargeMicro: 100, AmountTotalMicro: 100,
			BalanceBeforeMicro: 0, BalanceAfterMicro: 100, IdemKey: idem,
		})
		if err == nil {
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %v", err)
			}
		}
		return err
	}
	if err := insert(); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := insert(); !errors.Is(err, ErrDuplicateIdem) {
		t.Fatalf("second insert err = %v, want ErrDuplicateIdem", err)
	}
}

func TestLedgerTableHasNoMutableColumns(t *testing.T) {
	var n int
	if err := testDB.QueryRow(
		`SELECT COUNT(*) FROM information_schema.columns
		  WHERE table_schema = DATABASE() AND table_name = 't_ledger_entry'
		    AND column_name IN ('updated_at', 'deleted_at')`,
	).Scan(&n); err != nil {
		t.Fatalf("query columns: %v", err)
	}
	if n != 0 {
		t.Fatalf("t_ledger_entry has %d mutable columns; append-only shape is broken", n)
	}
}

func mustBegin(t *testing.T) *sql.Tx {
	t.Helper()
	tx, err := testDB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

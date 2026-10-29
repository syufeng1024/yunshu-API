package recon

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func execFixture(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := testDB.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatalf("fixture exec: %v\nquery: %s", err, q)
	}
}

// insertAccount writes the materialised balance. The caller is responsible for
// making it equal to the sum of the user's ledger entries when a test needs a
// consistent account.
func insertAccount(t *testing.T, userID, recharge, bonus int64) {
	t.Helper()
	insertAccountValues(t, userID, recharge, bonus, recharge+bonus)
}

// insertAccountValues seeds a deliberately inconsistent row. Tests that need a
// broken invariant go through INSERT rather than an UPDATE so the repository
// never contains a statement that overwrites a materialised balance.
func insertAccountValues(t *testing.T, userID, recharge, bonus, balance int64) {
	t.Helper()
	now := time.Now().UTC()
	execFixture(t,
		`INSERT INTO t_credit_account
		   (user_id, balance_recharge_micro, balance_bonus_micro, balance_micro,
		    overdraft_limit_micro, version, created_at, updated_at)
		 VALUES (?,?,?,?,500000,0,?,?)`,
		userID, recharge, bonus, balance, now, now)
}

func deleteAccount(t *testing.T, userID int64) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM t_upstream_attempt WHERE user_id = ?`,
		`DELETE FROM t_usage_record WHERE user_id = ?`,
		`DELETE FROM t_settle_task WHERE user_id = ?`,
		`DELETE FROM t_prehold_hold WHERE user_id = ?`,
		`DELETE FROM t_ledger_entry WHERE user_id = ?`,
		`DELETE FROM t_credit_batch WHERE user_id = ?`,
		`DELETE FROM t_credit_account WHERE user_id = ?`,
		`DELETE FROM t_user_daily_quota WHERE user_id = ?`,
		`DELETE FROM t_idempotency_record WHERE user_id = ?`,
	} {
		if _, err := testDB.ExecContext(ctx, q, userID); err != nil {
			t.Fatalf("cleanup user %d: %v\nquery: %s", userID, err, q)
		}
	}
}

// insertLedgerEntry writes one append-only row stamped at now. The caller
// controls the continuity values so R-03 fixtures can be deliberately broken.
func insertLedgerEntry(t *testing.T, userID int64, no, bizType, direction string,
	bonus, recharge, total, before, after int64, idemKey string, requestID *string) {
	t.Helper()
	insertLedgerEntryAt(t, userID, no, bizType, direction, bonus, recharge, total,
		before, after, idemKey, requestID, time.Now().UTC())
}

// insertLedgerEntryAt is insertLedgerEntry with an explicit created_at, which
// the settlement §2-1 day-window fixtures need.
func insertLedgerEntryAt(t *testing.T, userID int64, no, bizType, direction string,
	bonus, recharge, total, before, after int64, idemKey string, requestID *string,
	createdAt time.Time) {
	t.Helper()
	execFixture(t,
		`INSERT INTO t_ledger_entry
		   (user_id, entry_no, account_type, direction, biz_type,
		    amount_bonus_micro, amount_recharge_micro, amount_total_micro,
		    balance_before_micro, balance_after_micro, request_id, idem_key, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		userID, no, "MIXED", direction, bizType, bonus, recharge, total,
		before, after, requestID, idemKey, createdAt)
}

// insertBatch writes one credit batch. R-02's settlement §2-3 pass compares
// SUM(remaining_micro) with balance_recharge + balance_bonus, so every fixture
// that wants only its own violation must keep the batch sum consistent.
func insertBatch(t *testing.T, userID, face, remaining, consumed int64) {
	t.Helper()
	now := time.Now().UTC()
	execFixture(t,
		`INSERT INTO t_credit_batch
		   (user_id, batch_no, batch_type, face_value_micro, remaining_micro,
		    consumed_micro, refundable, effective_at, created_at, updated_at)
		 VALUES (?, ?, 'RECHARGE', ?, ?, ?, 1, ?, ?, ?)`,
		userID, fmt.Sprintf("%020d", now.UnixNano()), face, remaining, consumed, now, now, now)
}

// insertUsage writes the minimum NOT NULL columns of t_usage_record.
func insertUsage(t *testing.T, requestID string, userID int64, createdAt time.Time, result string, consumed int64) {
	t.Helper()
	execFixture(t,
		`INSERT INTO t_usage_record
		   (request_id, user_id, api_key_id, api_key_name_snap, model_name_snap,
		    request_model_req, inbound_protocol, http_status, result,
		    consumed_micro, total_ms, created_at)
		 VALUES (?,?,1,'fixture','fixture-model','fixture-model','OPENAI_COMPAT',200,?,?,1,?)`,
		requestID, userID, result, consumed, createdAt.UTC())
}

// insertAttempt writes the minimum NOT NULL columns of t_upstream_attempt.
func insertAttempt(t *testing.T, requestID string, userID, modelID, channelID int64,
	index int, phase, result, errorClass string, createdAt time.Time) {
	t.Helper()
	var errClass any
	if errorClass != "" {
		errClass = errorClass
	}
	execFixture(t,
		`INSERT INTO t_upstream_attempt
		   (request_id, attempt_index, user_id, model_id, route_id, channel_id,
		    channel_name_snap, protocol_code, upstream_model, phase, result,
		    error_class, total_ms, created_at)
		 VALUES (?,?,?,?,1,?, 'fixture-channel','OPENAI_COMPAT','fixture-model',?,?,?,10,?)`,
		requestID, index, userID, modelID, channelID, phase, result, errClass, createdAt.UTC())
}

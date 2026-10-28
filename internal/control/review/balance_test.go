package review

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yunshu.dev/core/internal/ledger"
)

// seedBalanceAccount creates the t_credit_account row the ledger requires. The
// review package's generic newTestUser deliberately creates none (no other
// document type needs one), so BALANCE_ADJUST tests add it explicitly.
func seedBalanceAccount(t *testing.T, uid int64) {
	t.Helper()
	now := testBase
	if _, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_credit_account
			(user_id, balance_recharge_micro, balance_bonus_micro, balance_micro,
			 overdraft_limit_micro, created_at, updated_at)
		VALUES (?,0,0,0,0,?,?)`, uid, now, now); err != nil {
		t.Fatalf("seed credit account: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_ledger_entry WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_credit_batch WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_credit_account WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_balance_adjust WHERE user_id = ?`, uid)
	})
}

func submitBalance(t *testing.T, svc *Service, operatorID, targetID int64,
	body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return invoke(t, svc.SubmitBalanceAdjust, http.MethodPost,
		"/api/admin/users/1/balance-adjust",
		map[string]string{"id": itoa(targetID)}, body, operatorID, "OPS")
}

func balanceBody(direction string, amount int64, reason string) map[string]any {
	return map[string]any{
		"direction":    direction,
		"amount_micro": amount,
		"reason":       reason,
	}
}

func balanceRowState(t *testing.T, id int64) (state string, direction string, amount int64) {
	t.Helper()
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT state, direction, amount_micro FROM t_balance_adjust WHERE id = ?`, id).
		Scan(&state, &direction, &amount); err != nil {
		t.Fatalf("read balance adjust %d: %v", id, err)
	}
	return state, direction, amount
}

func assertLedgerBalanced(t *testing.T, uid int64) {
	t.Helper()
	var sum, balance, recharge, bonus int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COALESCE(SUM(amount_total_micro), 0) FROM t_ledger_entry WHERE user_id = ?`, uid).
		Scan(&sum); err != nil {
		t.Fatalf("sum ledger: %v", err)
	}
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT balance_micro, balance_recharge_micro, balance_bonus_micro
		  FROM t_credit_account WHERE user_id = ?`, uid).
		Scan(&balance, &recharge, &bonus); err != nil {
		t.Fatalf("read account: %v", err)
	}
	if sum != balance {
		t.Fatalf("SUM(t_ledger_entry)=%d, balance_micro=%d; invariant broken", sum, balance)
	}
	if balance != recharge+bonus {
		t.Fatalf("balance_micro=%d != recharge(%d)+bonus(%d)", balance, recharge, bonus)
	}
	rows, err := testDB.QueryContext(context.Background(), `
		SELECT face_value_micro, remaining_micro, consumed_micro
		  FROM t_credit_batch WHERE user_id = ?`, uid)
	if err != nil {
		t.Fatalf("read batches: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var face, remaining, consumed int64
		if err := rows.Scan(&face, &remaining, &consumed); err != nil {
			t.Fatalf("scan batch: %v", err)
		}
		if remaining != face-consumed {
			t.Fatalf("batch remaining=%d != face(%d)-consumed(%d)", remaining, face, consumed)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate batches: %v", err)
	}
}

// assertBalanceAudit proves the write gate: every state change writes one
// t_admin_audit_log row addressed by t_balance_adjust/<id>, which is exactly
// what GET /api/admin/audit-logs?target_type=t_balance_adjust&target_id=N
// filters on.
func assertBalanceAudit(t *testing.T, adjustID int64, module, action string) {
	t.Helper()
	const where = `target_type = 't_balance_adjust' AND target_id = ?
		AND module = ? AND action = ? AND result = 'OK'`
	var n int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_admin_audit_log WHERE `+where,
		fmt.Sprint(adjustID), module, action).Scan(&n); err != nil {
		t.Fatalf("count balance audit: %v", err)
	}
	if n != 1 {
		t.Fatalf("balance audit %s/%s rows=%d, want 1", module, action, n)
	}
	var after sql.NullString
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT after_json FROM t_admin_audit_log WHERE `+where+` LIMIT 1`,
		fmt.Sprint(adjustID), module, action).Scan(&after); err != nil {
		t.Fatalf("read balance audit: %v", err)
	}
	if !after.Valid || after.String == "" || after.String == "null" {
		t.Fatalf("balance audit %s/%s after_json=%v, want a document snapshot", module, action, after)
	}
}

func countAdjustEntries(t *testing.T, uid int64) int64 {
	t.Helper()
	var n int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM t_ledger_entry
		 WHERE user_id = ? AND biz_type = ?`, uid, ledger.BizTypeAdjust).Scan(&n); err != nil {
		t.Fatalf("count adjust entries: %v", err)
	}
	return n
}

// A8 first half: submitting creates both the business row and the queue row,
// and the review list shows amount + direction + reason instead of an orphaned
// audit pointer.
func TestBalanceAdjustSubmitCreatesReviewTask(t *testing.T) {
	clk := newFixedClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "OPS")
	target := newTestUser(t, "PERSONAL")
	seedBalanceAccount(t, target)

	rec := submitBalance(t, svc, applicant, target,
		balanceBody(BalanceDirectionIncrease, 500000, "上游故障补偿"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created balanceAdjustSubmitResponse
	decodeBody(t, rec, &created)
	if created.BalanceAdjust.State != BalanceStatePending {
		t.Fatalf("state=%s, want PENDING", created.BalanceAdjust.State)
	}
	if created.ReviewTaskID <= 0 {
		t.Fatalf("review task id=%d, want > 0", created.ReviewTaskID)
	}

	var bizType, taskState string
	var bizID, taskApplicant int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT biz_type, biz_id, applicant_id, state FROM t_review_task WHERE id = ?`,
		created.ReviewTaskID).Scan(&bizType, &bizID, &taskApplicant, &taskState); err != nil {
		t.Fatalf("read review task: %v", err)
	}
	if bizType != BizBalanceAdjust || bizID != created.BalanceAdjust.ID ||
		taskApplicant != applicant || taskState != StatePending {
		t.Fatalf("review task = %s/%d/applicant=%d/%s", bizType, bizID, taskApplicant, taskState)
	}
	assertBalanceAudit(t, created.BalanceAdjust.ID, "USER", "SUBMIT")

	rec = invoke(t, svc.List, http.MethodGet,
		"/api/admin/reviews?state=PENDING&biz_type=BALANCE_ADJUST", nil, nil, applicant, "FINANCE")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var list reviewListResponse
	decodeBody(t, rec, &list)
	if len(list.Items) != 1 {
		t.Fatalf("list items=%d, want 1: %+v", len(list.Items), list.Items)
	}
	item := list.Items[0]
	if item.Direction == nil || *item.Direction != BalanceDirectionIncrease {
		t.Fatalf("direction=%v, want INCREASE", item.Direction)
	}
	if item.AmountMicro == nil || *item.AmountMicro != 500000 {
		t.Fatalf("amount_micro=%v, want 500000", item.AmountMicro)
	}
	if !strings.Contains(item.Summary, "上游故障补偿") {
		t.Fatalf("summary=%q does not contain the reason", item.Summary)
	}

	// A second pending document for the same user is refused by the generated
	// column's unique index, not by a read-then-insert check.
	rec = submitBalance(t, svc, applicant, target,
		balanceBody(BalanceDirectionDecrease, 1000, "第二张待审单"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate pending status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}

	// Body validation.
	rec = submitBalance(t, svc, applicant, target,
		balanceBody(BalanceDirectionIncrease, 0, "金额为零"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("zero amount status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	rec = submitBalance(t, svc, applicant, target,
		balanceBody(BalanceDirectionIncrease, 1000, "短"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short reason status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	t.Logf("A8 submit: adjust=%d review_task=%d state=%s direction=%s amount=%d summary=%q",
		created.BalanceAdjust.ID, created.ReviewTaskID, created.BalanceAdjust.State,
		*item.Direction, *item.AmountMicro, item.Summary)
}

// A8 second half: approval posts through the ledger and the invariant survives
// both directions.
func TestBalanceAdjustApproveKeepsLedgerBalanced(t *testing.T) {
	clk := newFixedClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "OPS")
	reviewer := newTestUser(t, "FINANCE")
	target := newTestUser(t, "PERSONAL")
	seedBalanceAccount(t, target)

	if _, err := svc.Ledger.Grant(context.Background(), ledger.GrantParams{
		UserID:          target,
		RechargeMicro:   100000,
		BizType:         ledger.BizTypeRecharge,
		IdemKeyRecharge: fmt.Sprintf("seed-%d", target),
	}); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	assertLedgerBalanced(t, target)

	// Increase.
	rec := submitBalance(t, svc, applicant, target,
		balanceBody(BalanceDirectionIncrease, 50000, "客服补偿积分"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("increase submit status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created balanceAdjustSubmitResponse
	decodeBody(t, rec, &created)
	rec = callReview(t, svc.Approve, http.MethodPost,
		"/api/admin/reviews/1/approve", itoa(created.ReviewTaskID), nil, reviewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("increase approve status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertLedgerBalanced(t, target)
	if amount := balanceOf(t, target); amount != 150000 {
		t.Fatalf("balance after increase=%d, want 150000", amount)
	}
	assertBalanceAudit(t, created.BalanceAdjust.ID, "REVIEW", "APPROVE")

	// Decrease: consumes the bonus batch created by the increase.
	rec = submitBalance(t, svc, applicant, target,
		balanceBody(BalanceDirectionDecrease, 20000, "纠正多发测试"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("decrease submit status=%d body=%s", rec.Code, rec.Body.String())
	}
	var second balanceAdjustSubmitResponse
	decodeBody(t, rec, &second)
	rec = callReview(t, svc.Approve, http.MethodPost,
		"/api/admin/reviews/1/approve", itoa(second.ReviewTaskID), nil, reviewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("decrease approve status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertLedgerBalanced(t, target)
	if amount := balanceOf(t, target); amount != 130000 {
		t.Fatalf("balance after decrease=%d, want 130000", amount)
	}

	if n := countAdjustEntries(t, target); n != 2 {
		t.Fatalf("ADJUST entries=%d, want 2", n)
	}
	var operatorID int64
	var reason string
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT operator_id, reason FROM t_ledger_entry
		 WHERE user_id = ? AND biz_type = ? ORDER BY id DESC LIMIT 1`,
		target, ledger.BizTypeAdjust).Scan(&operatorID, &reason); err != nil {
		t.Fatalf("read adjust entry: %v", err)
	}
	if operatorID != reviewer || reason != "纠正多发测试" {
		t.Fatalf("adjust entry operator=%d reason=%q, want %d / 纠正多发测试", operatorID, reason, reviewer)
	}

	var state string
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT state FROM t_balance_adjust WHERE id = ?`, second.BalanceAdjust.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != BalanceStateApproved {
		t.Fatalf("adjust state=%s, want APPROVED", state)
	}
	assertBalanceAudit(t, second.BalanceAdjust.ID, "REVIEW", "APPROVE")
	t.Logf("A8 approve: balance=130000, SUM(ledger)=balance, ADJUST entries=2, last operator=%d", operatorID)
}

func balanceOf(t *testing.T, uid int64) int64 {
	t.Helper()
	var balance int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT balance_micro FROM t_credit_account WHERE user_id = ?`, uid).Scan(&balance); err != nil {
		t.Fatalf("read balance: %v", err)
	}
	return balance
}

// A9: the applicant cannot approve their own document, and the refusal changes
// neither the balance nor the ledger.
func TestBalanceAdjustSelfAuditRefused(t *testing.T) {
	clk := newFixedClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "SUPER_ADMIN")
	target := newTestUser(t, "PERSONAL")
	seedBalanceAccount(t, target)

	if _, err := svc.Ledger.Grant(context.Background(), ledger.GrantParams{
		UserID:          target,
		RechargeMicro:   100000,
		BizType:         ledger.BizTypeRecharge,
		IdemKeyRecharge: fmt.Sprintf("seed-self-%d", target),
	}); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	rec := submitBalance(t, svc, applicant, target,
		balanceBody(BalanceDirectionIncrease, 50000, "自审测试原因"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created balanceAdjustSubmitResponse
	decodeBody(t, rec, &created)

	rec = invoke(t, svc.Approve, http.MethodPost, "/api/admin/reviews/1/approve",
		map[string]string{"id": itoa(created.ReviewTaskID)}, nil, applicant, "SUPER_ADMIN")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("self approve status=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	eb := errorOf(t, rec)
	if eb.Error.Code != "permission_denied" || !strings.Contains(eb.Error.Hint, "审批人不得为申请人") {
		t.Fatalf("self approve body=%+v", eb.Error)
	}

	if amount := balanceOf(t, target); amount != 100000 {
		t.Fatalf("balance=%d after self-audit refusal, want 100000", amount)
	}
	if n := countAdjustEntries(t, target); n != 0 {
		t.Fatalf("ADJUST entries=%d after refusal, want 0", n)
	}
	state, _, _ := balanceRowState(t, created.BalanceAdjust.ID)
	if state != BalanceStatePending {
		t.Fatalf("adjust state=%s after refusal, want PENDING", state)
	}
	t.Logf("A9 self-audit: 403 permission_denied; balance=100000; ADJUST entries=0; state=%s", state)
}

// A8 idempotency: a second approve answers 409 and does not post a second
// ledger entry.
func TestBalanceAdjustDoubleApproveNoDoubleGrant(t *testing.T) {
	clk := newFixedClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "OPS")
	reviewer := newTestUser(t, "FINANCE")
	target := newTestUser(t, "PERSONAL")
	seedBalanceAccount(t, target)

	if _, err := svc.Ledger.Grant(context.Background(), ledger.GrantParams{
		UserID:          target,
		RechargeMicro:   100000,
		BizType:         ledger.BizTypeRecharge,
		IdemKeyRecharge: fmt.Sprintf("seed-double-%d", target),
	}); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	rec := submitBalance(t, svc, applicant, target,
		balanceBody(BalanceDirectionIncrease, 50000, "幂等测试原因"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created balanceAdjustSubmitResponse
	decodeBody(t, rec, &created)

	rec = callReview(t, svc.Approve, http.MethodPost,
		"/api/admin/reviews/1/approve", itoa(created.ReviewTaskID), nil, reviewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("first approve status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = callReview(t, svc.Approve, http.MethodPost,
		"/api/admin/reviews/1/approve", itoa(created.ReviewTaskID), nil, reviewer)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second approve status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if eb := errorOf(t, rec); eb.Error.Code != "resource_state_conflict" {
		t.Fatalf("second approve code=%q", eb.Error.Code)
	}
	if amount := balanceOf(t, target); amount != 150000 {
		t.Fatalf("balance=%d after double approve, want 150000", amount)
	}
	if n := countAdjustEntries(t, target); n != 1 {
		t.Fatalf("ADJUST entries=%d after double approve, want 1", n)
	}
	// A rejection after approval must not undo the grant.
	rec = callReview(t, svc.Reject, http.MethodPost,
		"/api/admin/reviews/1/reject", itoa(created.ReviewTaskID),
		map[string]any{"reason": "事后反悔"}, reviewer)
	if rec.Code != http.StatusConflict {
		t.Fatalf("reject after approve status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if amount := balanceOf(t, target); amount != 150000 {
		t.Fatalf("balance=%d after rejected reversal, want 150000", amount)
	}
	t.Logf("idempotency: first approve=200, second=409, ADJUST entries=1, balance=150000")
}

// Rejection moves both rows and writes no money.
func TestBalanceAdjustRejectMovesBothRows(t *testing.T) {
	clk := newFixedClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "OPS")
	reviewer := newTestUser(t, "FINANCE")
	target := newTestUser(t, "PERSONAL")
	seedBalanceAccount(t, target)

	if _, err := svc.Ledger.Grant(context.Background(), ledger.GrantParams{
		UserID:          target,
		RechargeMicro:   100000,
		BizType:         ledger.BizTypeRecharge,
		IdemKeyRecharge: fmt.Sprintf("seed-reject-%d", target),
	}); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	rec := submitBalance(t, svc, applicant, target,
		balanceBody(BalanceDirectionDecrease, 20000, "待驳回测试"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created balanceAdjustSubmitResponse
	decodeBody(t, rec, &created)

	rec = callReview(t, svc.Reject, http.MethodPost,
		"/api/admin/reviews/1/reject", itoa(created.ReviewTaskID),
		map[string]any{"reason": "理由不充分"}, reviewer)
	if rec.Code != http.StatusOK {
		t.Fatalf("reject status=%d body=%s", rec.Code, rec.Body.String())
	}
	state, _, _ := balanceRowState(t, created.BalanceAdjust.ID)
	if state != BalanceStateRejected {
		t.Fatalf("adjust state=%s, want REJECTED", state)
	}
	var taskState, note string
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT state, decision_note FROM t_review_task WHERE id = ?`, created.ReviewTaskID).
		Scan(&taskState, &note); err != nil {
		t.Fatal(err)
	}
	if taskState != StateRejected || note != "理由不充分" {
		t.Fatalf("review task state=%s note=%q", taskState, note)
	}
	if amount := balanceOf(t, target); amount != 100000 {
		t.Fatalf("balance=%d after reject, want 100000", amount)
	}
	if n := countAdjustEntries(t, target); n != 0 {
		t.Fatalf("ADJUST entries=%d after reject, want 0", n)
	}
	assertBalanceAudit(t, created.BalanceAdjust.ID, "REVIEW", "REJECT")
}

// A decrease larger than the batch-backed balance is refused and leaves the
// account untouched.
func TestBalanceAdjustDecreaseOverBalanceRefused(t *testing.T) {
	clk := newFixedClock(testBase)
	svc := newTestService(t, clk)
	applicant := newTestUser(t, "OPS")
	reviewer := newTestUser(t, "FINANCE")
	target := newTestUser(t, "PERSONAL")
	seedBalanceAccount(t, target)

	if _, err := svc.Ledger.Grant(context.Background(), ledger.GrantParams{
		UserID:          target,
		RechargeMicro:   1000,
		BizType:         ledger.BizTypeRecharge,
		IdemKeyRecharge: fmt.Sprintf("seed-over-%d", target),
	}); err != nil {
		t.Fatalf("seed grant: %v", err)
	}
	rec := submitBalance(t, svc, applicant, target,
		balanceBody(BalanceDirectionDecrease, 5000, "超额扣减测试"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("submit status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created balanceAdjustSubmitResponse
	decodeBody(t, rec, &created)

	rec = callReview(t, svc.Approve, http.MethodPost,
		"/api/admin/reviews/1/approve", itoa(created.ReviewTaskID), nil, reviewer)
	if rec.Code != http.StatusConflict {
		t.Fatalf("over-balance approve status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if amount := balanceOf(t, target); amount != 1000 {
		t.Fatalf("balance=%d after refused decrease, want 1000", amount)
	}
	if n := countAdjustEntries(t, target); n != 0 {
		t.Fatalf("ADJUST entries=%d after refused decrease, want 0", n)
	}
	// The failed approval rolled back, so the document is still PENDING and can
	// be rejected by the operator.
	state, _, _ := balanceRowState(t, created.BalanceAdjust.ID)
	if state != BalanceStatePending {
		t.Fatalf("adjust state=%s after refused approval, want PENDING", state)
	}
}

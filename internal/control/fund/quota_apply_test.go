package fund

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A6 + A13 + G-08: submit → approve (not self) → policy row with the next-day
// 00:00 UTC+8 window and +30 days, then cfg:ver advances after commit.
func TestA6QuotaApplyApproveAndEffective(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	payPlanOrder(t, svc, uid, "STANDARD")

	current, _, apiErr := svc.currentDailyQuota(context.Background(), uid)
	if apiErr != nil {
		t.Fatalf("current quota: %v", apiErr)
	}
	if current <= 0 {
		t.Fatalf("current quota = %d, want the STANDARD daily quota", current)
	}
	target := current * 2

	// The publish hook runs after commit; record what the gateway would have
	// seen and advance cfg:ver the way RouteSnapshot.Publish does.
	var (
		publishedPolicy int64
		publishCalls    int
		cfgVersion      int64
	)
	svc.PublishRoutes = func(ctx context.Context) error {
		publishCalls++
		if err := testDB.QueryRowContext(ctx, `
			SELECT daily_quota_micro FROM t_rate_limit_policy
			 WHERE scope_type = 'USER' AND scope_id = ? AND source = 'QUOTA_APPLY'
			 ORDER BY id DESC LIMIT 1`, uid).Scan(&publishedPolicy); err != nil {
			return err
		}
		v, err := svc.Redis.Raw().Incr(ctx, "cfg:ver").Result()
		if err != nil {
			return err
		}
		cfgVersion = v
		return nil
	}

	rec := call(t, svc.CreateQuotaApply, http.MethodPost, "/api/console/quota-applies", map[string]any{
		"target_quota_micro": target,
		"apply_reason":       "活动期间并发上升，需要临时提高日额度",
	}, uid)
	if rec.Code != http.StatusCreated {
		t.Fatalf("quota apply status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created createQuotaResponse
	decodeBody(t, rec, &created)
	if created.Apply.State != QuotaStatePending {
		t.Fatalf("state = %q, want PENDING", created.Apply.State)
	}
	if created.Apply.CurrentQuotaMicro != current || created.Apply.TargetQuotaMicro != target {
		t.Fatalf("snapshot = %d/%d, want %d/%d",
			created.Apply.CurrentQuotaMicro, created.Apply.TargetQuotaMicro, current, target)
	}

	// Before approval the daily quota is unchanged: no QUOTA_APPLY row exists.
	var before int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM t_rate_limit_policy
		 WHERE scope_type = 'USER' AND scope_id = ? AND source = 'QUOTA_APPLY'`, uid).
		Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Fatalf("QUOTA_APPLY policy rows before approval = %d, want 0", before)
	}

	// A13: the applicant may not approve their own request.
	if _, err := svc.ApproveQuotaApply(context.Background(), created.Apply.ID, uid); !errors.Is(err, ErrSelfAudit) {
		t.Fatalf("self-audit error = %v, want ErrSelfAudit", err)
	}
	var guardRows int
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM t_admin_audit_log
		 WHERE operator_id = ? AND action = 'APPROVE' AND result = 'REJECTED'`,
		uid).Scan(&guardRows); err != nil {
		t.Fatal(err)
	}
	if guardRows != 1 {
		t.Fatalf("self-audit guard rows = %d, want 1", guardRows)
	}
	t.Logf("A13 self-audit refused: %v; state=%s; guard_audit_rows=%d",
		ErrSelfAudit, quotaStateOf(t, created.Apply.ID), guardRows)
	if st := quotaStateOf(t, created.Apply.ID); st != QuotaStatePending {
		t.Fatalf("state after refused self-audit = %q, want PENDING", st)
	}

	approved, err := svc.ApproveQuotaApply(context.Background(), created.Apply.ID, 900021)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approved.State != QuotaStateApproved {
		t.Fatalf("state = %q, want APPROVED", approved.State)
	}
	if approved.ApprovedQuotaMicro == nil || *approved.ApprovedQuotaMicro != target {
		t.Fatalf("approved_quota_micro = %v, want %d", approved.ApprovedQuotaMicro, target)
	}

	// The approved window is the next 00:00 in UTC+8, for 30 days.
	now := time.Now().UTC()
	wantFrom := nextDayUTC8(now)
	if approved.EffectiveFrom == nil || !approved.EffectiveFrom.Equal(wantFrom) {
		t.Fatalf("effective_from = %v, want %v", approved.EffectiveFrom, wantFrom)
	}
	local := approved.EffectiveFrom.In(utc8)
	if local.Hour() != 0 || local.Minute() != 0 || local.Second() != 0 {
		t.Fatalf("effective_from in UTC+8 = %s, want 00:00:00", local)
	}
	wantTo := wantFrom.AddDate(0, 0, 30)
	if approved.EffectiveTo == nil || !approved.EffectiveTo.Equal(wantTo) {
		t.Fatalf("effective_to = %v, want %v", approved.EffectiveTo, wantTo)
	}

	if publishCalls != 1 {
		t.Fatalf("PublishRoutes calls = %d, want 1", publishCalls)
	}
	if publishedPolicy != target {
		t.Fatalf("published policy daily quota = %d, want %d", publishedPolicy, target)
	}
	if cfgVersion <= 0 {
		t.Fatalf("cfg:ver after publish = %d, want > 0", cfgVersion)
	}

	// The row itself carries the QUOTA_APPLY source and the fallback pointer.
	var (
		source   string
		daily    int64
		fallback *int64
		from, to time.Time
	)
	var fbNull *int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT source, daily_quota_micro, fallback_policy_id, effective_from, effective_to
		  FROM t_rate_limit_policy
		 WHERE scope_type = 'USER' AND scope_id = ? AND source = 'QUOTA_APPLY'
		 ORDER BY id DESC LIMIT 1`, uid).
		Scan(&source, &daily, &fbNull, &from, &to); err != nil {
		t.Fatal(err)
	}
	fallback = fbNull
	if source != "QUOTA_APPLY" || daily != target {
		t.Fatalf("policy = %s/%d, want QUOTA_APPLY/%d", source, daily, target)
	}
	if fallback == nil {
		t.Fatal("fallback_policy_id is NULL; the plan row in force should be referenced")
	}
	t.Logf("policy: source=%s daily=%d effective_from=%s (UTC+8 %s) effective_to=%s fallback=%d cfg_ver=%d",
		source, daily, from.UTC(), from.In(utc8), to.UTC(), *fallback, cfgVersion)
}

// A6 negative: a target above 5x the current daily quota is refused.
func TestQuotaTargetCeiling(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	payPlanOrder(t, svc, uid, "STANDARD")
	current, _, apiErr := svc.currentDailyQuota(context.Background(), uid)
	if apiErr != nil {
		t.Fatal(apiErr)
	}

	rec := call(t, svc.CreateQuotaApply, http.MethodPost, "/api/console/quota-applies", map[string]any{
		"target_quota_micro": current*5 + 1,
		"apply_reason":       "测试超额",
	}, uid)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("over-limit status=%d body=%s", rec.Code, rec.Body.String())
	}
	if msg := errorBody(t, rec).Error.Message; !strings.Contains(msg, "单次上限") {
		t.Fatalf("message = %q, want the 5x ceiling", msg)
	}
}

// A13 is also enforced on rejection, and a rejection notifies the applicant.
func TestQuotaRejectSelfAuditAndNotification(t *testing.T) {
	svc, _ := newTestService(t)
	uid := newTestUser(t, nil)
	payPlanOrder(t, svc, uid, "STANDARD")
	current, _, _ := svc.currentDailyQuota(context.Background(), uid)
	rec := call(t, svc.CreateQuotaApply, http.MethodPost, "/api/console/quota-applies", map[string]any{
		"target_quota_micro": current * 2,
		"apply_reason":       "测试驳回",
	}, uid)
	var created createQuotaResponse
	decodeBody(t, rec, &created)

	if err := svc.RejectQuotaApply(context.Background(), created.Apply.ID, uid, "自审"); !errors.Is(err, ErrSelfAudit) {
		t.Fatalf("self reject error = %v, want ErrSelfAudit", err)
	}
	if err := svc.RejectQuotaApply(context.Background(), created.Apply.ID, 900022, "当前额度足够"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if st := quotaStateOf(t, created.Apply.ID); st != QuotaStateRejected {
		t.Fatalf("state = %q, want REJECTED", st)
	}
	if n := countNotifications(t, uid, "QUOTA_AUDIT"); n != 1 {
		t.Fatalf("QUOTA_AUDIT notifications = %d, want 1", n)
	}
}

func quotaStateOf(t *testing.T, id int64) string {
	t.Helper()
	var state string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT state FROM t_quota_apply WHERE id = ?`, id).Scan(&state); err != nil {
		t.Fatalf("quota state: %v", err)
	}
	return state
}

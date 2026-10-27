package ops

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRiskRuleCRUDAndHits(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")

	body := map[string]any{
		"rule_name":      "t15-rule-" + uniqueSuffix(),
		"metric":         "rpm_1m",
		"operator":       "GTE",
		"threshold":      1000,
		"window_seconds": 60,
		"action":         "ALERT",
		"scope":          "USER",
	}
	rec := call(t, svc.CreateRiskRule, http.MethodPost, "/api/admin/risk/rules", body,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create rule status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created riskRuleDetailResponse
	decodeBody(t, rec, &created)
	ruleID := created.Rule.ID
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM t_risk_hit_log WHERE rule_id = ?`, ruleID)
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM t_risk_rule WHERE id = ?`, ruleID)
	})

	rec = call(t, svc.UpdateRiskRule, http.MethodPatch,
		fmt.Sprintf("/api/admin/risk/rules/%d", ruleID),
		map[string]any{"threshold": 2000, "action": "THROTTLE",
			"action_param": map[string]any{"ratio_bp": 5000}},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(ruleID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("update rule status=%d body=%s", rec.Code, rec.Body.String())
	}
	var updated riskRuleDetailResponse
	decodeBody(t, rec, &updated)
	if updated.Rule.Threshold != 2000 || updated.Rule.Action != "THROTTLE" {
		t.Fatalf("rule = %+v, want threshold 2000 / THROTTLE", updated.Rule)
	}

	hitUser := newTestUser(t, "PERSONAL")
	if _, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_risk_hit_log
			(rule_id, scope_type, scope_value, user_id, metric_value, threshold,
			 action_taken, action_result, created_at)
		VALUES (?, 'USER', ?, ?, 2500, 2000, 'THROTTLE', 'APPLIED', ?)`,
		ruleID, fmt.Sprint(hitUser), hitUser, time.Now().UTC()); err != nil {
		t.Fatalf("insert hit: %v", err)
	}

	rec = call(t, svc.ListRiskHits, http.MethodGet,
		fmt.Sprintf("/api/admin/risk/hits?rule_id=%d", ruleID), nil,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusOK {
		t.Fatalf("list hits status=%d body=%s", rec.Code, rec.Body.String())
	}
	var hits riskHitListResponse
	decodeBody(t, rec, &hits)
	if hits.Total != 1 || len(hits.Items) != 1 {
		t.Fatalf("hits total=%d items=%d, want 1", hits.Total, len(hits.Items))
	}
	if hits.Items[0].RuleName != updated.Rule.RuleName || hits.Items[0].MetricValue != 2500 {
		t.Fatalf("hit = %+v", hits.Items[0])
	}

	rec = call(t, svc.DeleteRiskRule, http.MethodDelete,
		fmt.Sprintf("/api/admin/risk/rules/%d", ruleID), nil,
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(ruleID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("delete rule status=%d body=%s", rec.Code, rec.Body.String())
	}
	var enabled bool
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT enabled FROM t_risk_rule WHERE id = ?`, ruleID).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("deleted rule is still enabled; historical hits would break")
	}
}

func TestRiskScoreWritesBeforeAfterAudit(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	target := newTestUser(t, "PERSONAL")

	rec := call(t, svc.SetUserRiskScore, http.MethodPost,
		fmt.Sprintf("/api/admin/risk/users/%d/score", target),
		map[string]any{"risk_score": 85, "reason": "连续命中风控规则"},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": fmt.Sprint(target)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("score status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp riskScoreResponse
	decodeBody(t, rec, &resp)
	if resp.RiskScore != 85 {
		t.Fatalf("risk_score = %d, want 85", resp.RiskScore)
	}

	var before, after, result string
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT before_json, after_json, result FROM t_admin_audit_log
		 WHERE module = 'RISK' AND action = 'UPDATE_RISK_SCORE' AND target_id = ?
		 ORDER BY id DESC LIMIT 1`, fmt.Sprint(target)).Scan(&before, &after, &result); err != nil {
		t.Fatalf("load audit: %v", err)
	}
	if !strings.Contains(before, `"risk_score": 0`) || !strings.Contains(after, `"risk_score": 85`) {
		t.Fatalf("audit before=%s after=%s, want 0 → 85", before, after)
	}
	if result != "OK" {
		t.Fatalf("result = %q, want OK", result)
	}
}

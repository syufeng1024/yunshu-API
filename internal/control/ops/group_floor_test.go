package ops

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// T31 / D-28: the group-ratio write path shares the price-side floor. These
// tests use the seeded contract group (8500 bp) and insert the price rule
// directly: creating a 30000/20000 rule through the API would be refused at
// contract's 8500 ratio before the group test could run.

func loadGroupRow(t *testing.T, code string) (id, ratio int64, modelMode, channelMode, remark string) {
	t.Helper()
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT id, ratio_bp, model_scope_mode, channel_scope_mode, remark
		  FROM t_price_group WHERE code = ?`, code).Scan(
		&id, &ratio, &modelMode, &channelMode, &remark); err != nil {
		t.Fatalf("load group %s: %v", code, err)
	}
	return
}

func restoreGroup(t *testing.T, id, ratio int64, modelMode, channelMode, remark string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testDB.ExecContext(ctx, `
			UPDATE t_price_group
			   SET ratio_bp = ?, model_scope_mode = ?, channel_scope_mode = ?, remark = ?
			 WHERE id = ?`, ratio, modelMode, channelMode, remark, id)
		_, _ = testDB.ExecContext(ctx,
			`DELETE FROM t_price_group_scope WHERE group_id = ?`, id)
	})
}

func insertTestPriceRule(t *testing.T, modelID, in, out, cost int64) {
	t.Helper()
	if _, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_price_rule
			(scope_level, scope_id, model_type, in_price_micro_per_k, out_price_micro_per_k,
			 per_call_price_micro, cost_in_micro_per_k, cost_out_micro_per_k,
			 effective_from, version, enabled, created_by, created_at)
		VALUES ('MODEL', ?, 'TEXT', ?, ?, 0, ?, ?, '2026-01-01 00:00:00.000', 1, 1, 0, NOW(3))`,
		modelID, in, out, cost, cost); err != nil {
		t.Fatalf("insert price rule: %v", err)
	}
}

func auditCount(t *testing.T, uid, targetID int64) int64 {
	t.Helper()
	var n int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM t_admin_audit_log
		 WHERE operator_id = ? AND target_type = 't_price_group' AND target_id = ?`,
		uid, fmt.Sprint(targetID)).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	return n
}

// G1 + G2: a ratio below the floor is refused with the exact shortfall and
// leaves neither the row nor an audit entry; a ratio above it is accepted and
// audited with before/after.
func TestGroupRatioFloorRejectAndAccept(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	ctx := context.Background()

	contractID, originalBP, modelMode, channelMode, remark := loadGroupRow(t, "contract")
	restoreGroup(t, contractID, originalBP, modelMode, channelMode, remark)

	model := newPricingModel(t, svc, uid, "TEXT")
	insertTestPriceRule(t, model.ID, 30000, 30000, 20000)

	patch := func(ratio float64) map[string]any {
		return map[string]any{
			"ratio":            ratio,
			"model_scope_mode": "SELECTED",
			"model_ids":        []int64{model.ID},
		}
	}
	opts := testCallOptions{uid: uid, role: "OPS",
		pathVal: map[string]string{"id": fmt.Sprint(contractID)}}

	// G1: 30000 × 0.6 = 18000 < ceil(4 × 20000 / 3) = 26667; 差额 8667.
	rec := call(t, svc.UpdateGroup, http.MethodPatch,
		fmt.Sprintf("/api/admin/groups/%d", contractID), patch(0.6), opts)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("G1 ratio 0.6 status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	eb := errorBody(t, rec)
	if eb.Error.Code != "invalid_request_body" {
		t.Fatalf("G1 code=%q, want invalid_request_body", eb.Error.Code)
	}
	for _, want := range []string{"contract", "6000", "18000", "26667", "8667", model.ModelName} {
		if !strings.Contains(eb.Error.Message, want) {
			t.Fatalf("G1 message %q missing %q", eb.Error.Message, want)
		}
	}
	var stored int64
	if err := testDB.QueryRowContext(ctx,
		`SELECT ratio_bp FROM t_price_group WHERE id = ?`, contractID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != originalBP {
		t.Fatalf("G1 stored ratio=%d, want unchanged %d", stored, originalBP)
	}
	if n := auditCount(t, uid, contractID); n != 0 {
		t.Fatalf("G1 audit rows=%d, want 0", n)
	}
	t.Logf("G1 → 400: %s", eb.Error.Message)

	// G2: 30000 × 0.9 = 27000 ≥ 26667.
	rec = call(t, svc.UpdateGroup, http.MethodPatch,
		fmt.Sprintf("/api/admin/groups/%d", contractID), patch(0.9), opts)
	if rec.Code != http.StatusOK {
		t.Fatalf("G2 ratio 0.9 status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var detail groupDetailResponse
	decodeBody(t, rec, &detail)
	if detail.Group.RatioBP != 9000 {
		t.Fatalf("G2 response ratio_bp=%d, want 9000", detail.Group.RatioBP)
	}
	if len(detail.Warnings) != 0 {
		t.Fatalf("G2 warnings=%v, want none", detail.Warnings)
	}
	if err := testDB.QueryRowContext(ctx,
		`SELECT ratio_bp FROM t_price_group WHERE id = ?`, contractID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 9000 {
		t.Fatalf("G2 stored ratio=%d, want 9000", stored)
	}
	var beforeJSON, afterJSON string
	if err := testDB.QueryRowContext(ctx, `
		SELECT before_json, after_json FROM t_admin_audit_log
		 WHERE operator_id = ? AND target_type = 't_price_group' AND target_id = ?
		 ORDER BY id DESC LIMIT 1`, uid, fmt.Sprint(contractID)).Scan(
		&beforeJSON, &afterJSON); err != nil {
		t.Fatalf("G2 load audit: %v", err)
	}
	if !strings.Contains(beforeJSON, `"ratio_bp": 8500`) ||
		!strings.Contains(afterJSON, `"ratio_bp": 9000`) {
		t.Fatalf("G2 audit before=%s after=%s, want 8500 → 9000", beforeJSON, afterJSON)
	}
	t.Logf("G2 → 200 ratio_bp=9000, audit before/after carry 8500/9000")
}

// G3: internal's ratio 0 is never floor-checked, but every save needs both
// confirm=true and a non-empty reason.
func TestInternalGroupRequiresConfirmationAndReason(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	ctx := context.Background()

	internalID, originalBP, modelMode, channelMode, remark := loadGroupRow(t, "internal")
	restoreGroup(t, internalID, originalBP, modelMode, channelMode, remark)

	opts := testCallOptions{uid: uid, role: "OPS",
		pathVal: map[string]string{"id": fmt.Sprint(internalID)}}
	target := fmt.Sprintf("/api/admin/groups/%d", internalID)

	rec := call(t, svc.UpdateGroup, http.MethodPatch, target,
		map[string]any{"ratio": 0, "confirm": true}, opts)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("G3 missing reason status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if eb := errorBody(t, rec); eb.Error.Code != "invalid_request_body" ||
		!strings.Contains(eb.Error.Message, "reason") {
		t.Fatalf("G3 missing reason = %+v", eb.Error)
	}

	rec = call(t, svc.UpdateGroup, http.MethodPatch, target,
		map[string]any{"ratio": 0, "reason": "内部联调"}, opts)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("G3 missing confirm status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	if eb := errorBody(t, rec); eb.Error.Code != "invalid_request_body" ||
		!strings.Contains(eb.Error.Message, "confirm") {
		t.Fatalf("G3 missing confirm = %+v", eb.Error)
	}

	rec = call(t, svc.UpdateGroup, http.MethodPatch, target,
		map[string]any{"ratio": 0, "confirm": true, "reason": "内部联调"}, opts)
	if rec.Code != http.StatusOK {
		t.Fatalf("G3 internal 0 status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var stored int64
	if err := testDB.QueryRowContext(ctx,
		`SELECT ratio_bp FROM t_price_group WHERE id = ?`, internalID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Fatalf("G3 stored ratio=%d, want 0", stored)
	}
	var reason string
	if err := testDB.QueryRowContext(ctx, `
		SELECT reason FROM t_admin_audit_log
		 WHERE operator_id = ? AND target_type = 't_price_group' AND target_id = ?
		 ORDER BY id DESC LIMIT 1`, uid, fmt.Sprint(internalID)).Scan(&reason); err != nil {
		t.Fatalf("G3 audit reason: %v", err)
	}
	if reason != "内部联调" {
		t.Fatalf("G3 audit reason=%q, want 内部联调", reason)
	}
	t.Logf("G3 internal 0 → 200, audit reason=%q; missing reason/confirm each 400", reason)
}

// G4: internal's explicit channel scope may not contain the main channel.
func TestInternalGroupRejectsMainChannel(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	ctx := context.Background()

	internalID, originalBP, modelMode, channelMode, remark := loadGroupRow(t, "internal")
	restoreGroup(t, internalID, originalBP, modelMode, channelMode, remark)

	channel := createTestChannel(t, svc, uid,
		channelBody(t, "t31-main-channel-"+uniqueSuffix(), "DOMESTIC"))
	if _, err := testDB.ExecContext(ctx,
		`UPDATE t_channel SET weight = 1000000 WHERE id = ?`, channel.ID); err != nil {
		t.Fatalf("raise channel weight: %v", err)
	}

	rec := call(t, svc.UpdateGroup, http.MethodPatch,
		fmt.Sprintf("/api/admin/groups/%d", internalID),
		map[string]any{
			"ratio":              0,
			"confirm":            true,
			"reason":             "渠道隔离验证",
			"channel_scope_mode": "SELECTED",
			"channel_ids":        []int64{channel.ID},
		},
		testCallOptions{uid: uid, role: "OPS",
			pathVal: map[string]string{"id": fmt.Sprint(internalID)}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("G4 status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	eb := errorBody(t, rec)
	if eb.Error.Code != "invalid_request_body" || !strings.Contains(eb.Error.Message, channel.Name) {
		t.Fatalf("G4 code=%q message=%q, want invalid_request_body naming %q",
			eb.Error.Code, eb.Error.Message, channel.Name)
	}
	t.Logf("G4 → 400: %s", eb.Error.Message)
}

// G5: a model without a cost price is a warning plus confirmation, never a
// silent save and never a hard rejection.
func TestGroupRatioMissingCostWarnsAndNeedsConfirm(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	ctx := context.Background()

	contractID, originalBP, modelMode, channelMode, remark := loadGroupRow(t, "contract")
	restoreGroup(t, contractID, originalBP, modelMode, channelMode, remark)

	// No t_price_rule row at all → no cost for either TEXT dimension.
	model := newPricingModel(t, svc, uid, "TEXT")

	body := func(confirm bool) map[string]any {
		m := map[string]any{
			"ratio":            0.9,
			"model_scope_mode": "SELECTED",
			"model_ids":        []int64{model.ID},
		}
		if confirm {
			m["confirm"] = true
		}
		return m
	}
	opts := testCallOptions{uid: uid, role: "OPS",
		pathVal: map[string]string{"id": fmt.Sprint(contractID)}}

	rec := call(t, svc.UpdateGroup, http.MethodPatch,
		fmt.Sprintf("/api/admin/groups/%d", contractID), body(false), opts)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("G5 unconfirmed status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	var refusal struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		Warnings []string `json:"warnings"`
	}
	decodeBody(t, rec, &refusal)
	if refusal.Error.Code != "invalid_request_body" {
		t.Fatalf("G5 code=%q, want invalid_request_body", refusal.Error.Code)
	}
	// The hand-rolled envelope (warnings[] is not part of apierr.Write) must
	// still carry the request id header every other error response has.
	if rec.Header().Get("X-Request-Id") == "" {
		t.Fatal("G5 refusal missing X-Request-Id header")
	}
	if len(refusal.Warnings) != 1 || !strings.Contains(refusal.Warnings[0], model.ModelName) {
		t.Fatalf("G5 warnings=%v, want one naming %s", refusal.Warnings, model.ModelName)
	}
	var stored int64
	if err := testDB.QueryRowContext(ctx,
		`SELECT ratio_bp FROM t_price_group WHERE id = ?`, contractID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != originalBP {
		t.Fatalf("G5 unconfirmed stored ratio=%d, want unchanged %d", stored, originalBP)
	}

	rec = call(t, svc.UpdateGroup, http.MethodPatch,
		fmt.Sprintf("/api/admin/groups/%d", contractID), body(true), opts)
	if rec.Code != http.StatusOK {
		t.Fatalf("G5 confirmed status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var detail groupDetailResponse
	decodeBody(t, rec, &detail)
	if len(detail.Warnings) != 1 || !strings.Contains(detail.Warnings[0], "缺成本价") {
		t.Fatalf("G5 confirmed warnings=%v, want the missing-cost warning", detail.Warnings)
	}
	if detail.Group.RatioBP != 9000 {
		t.Fatalf("G5 confirmed ratio_bp=%d, want 9000", detail.Group.RatioBP)
	}
	t.Logf("G5 unconfirmed → 400 + warnings=%v; confirmed → 200 ratio_bp=9000", detail.Warnings)
}

// The refusal message names at most five models; the rest are counted so the
// operator can size the blast radius (T31 §3 D-28 6).
func TestModelNameListCapsAtFive(t *testing.T) {
	names := []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7"}
	if got, want := modelNameList(names, 5), "m1、m2、m3、m4、m5 等 7 个"; got != want {
		t.Fatalf("modelNameList(7) = %q, want %q", got, want)
	}
	if got, want := modelNameList(names[:5], 5), "m1、m2、m3、m4、m5"; got != want {
		t.Fatalf("modelNameList(5) = %q, want %q", got, want)
	}
}

// G7 (D-28 ③): internal usage is not revenue. The overview keeps the request
// count but drops internal's consumed_micro from the money aggregate.
func TestOverviewRevenueExcludesInternalGroup(t *testing.T) {
	db := newOverviewDB(t)

	rows := []struct {
		group    string
		consumed int64
	}{
		{"standard", 100},
		{"internal", 500},
		{"internal", 0},
	}
	for i, row := range rows {
		if _, err := db.ExecContext(context.Background(), `
			INSERT INTO t_usage_record
				(request_id, user_id, api_key_id, api_key_name_snap, model_name_snap,
				 request_model_req, inbound_protocol, http_status, result,
				 consumed_micro, total_ms, group_code_snap, created_at)
			VALUES (?, 1, 1, 't31-key', 't31-model', 't31-model', 'OPENAI_COMPAT', 200,
			        'SUCCESS', ?, 10, ?, ?)`,
			fmt.Sprintf("%036d", i+1), row.consumed, row.group, overviewNow); err != nil {
			t.Fatalf("seed usage row %d: %v", i, err)
		}
	}

	resp := fetchOverview(t, overviewService(db))
	if resp.Today.ConsumedMicro != 100 {
		t.Fatalf("consumed_micro=%d, want 100 (internal 500 excluded)", resp.Today.ConsumedMicro)
	}
	if resp.Today.Requests != 3 {
		t.Fatalf("requests=%d, want 3 (internal still counted for availability)",
			resp.Today.Requests)
	}
	t.Logf("G7 requests=3, consumed_micro=100 with 500 internal excluded")
}

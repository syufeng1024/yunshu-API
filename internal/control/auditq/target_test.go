package auditq

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/platform/clock"
)

func insertAuditTarget(t *testing.T, operatorID int64, targetType, targetID, action string, createdAt time.Time) int64 {
	t.Helper()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_admin_audit_log
			(trace_id, operator_id, operator_role, operator_ip, module, action,
			 target_type, target_id, before_json, after_json, reason, result, created_at)
		VALUES (?, ?, 'OPS', ?, 'CHANNEL', ?, ?, ?, NULL, NULL, NULL, 'OK', ?)`,
		fmt.Sprintf("%032x", operatorID), operatorID, []byte{192, 0, 2, 1},
		action, targetType, targetID, createdAt)
	if err != nil {
		t.Fatalf("insert targeted audit row: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("targeted audit id: %v", err)
	}
	return id
}

// A7: target_type/target_id go into SQL WHERE; omitting them keeps the
// original operator/window/order result.
func TestAuditListTargetFilters(t *testing.T) {
	base := time.Date(2002, 1, 1, 0, 0, 0, 0, time.UTC)
	opID := int64(9_100_000_000 + time.Now().UnixNano()%1_000_000)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(),
			`DELETE FROM t_admin_audit_log WHERE operator_id = ?`, opID)
	})

	insertAuditTarget(t, opID, "t_channel", "7", "UPDATE", base)
	insertAuditTarget(t, opID, "t_channel", "8", "UPDATE", base.Add(time.Minute))
	insertAuditTarget(t, opID, "t_user", "7", "DISABLE", base.Add(2*time.Minute))

	window := "from=" + base.Add(-time.Hour).Format(time.RFC3339) +
		"&to=" + base.Add(3*time.Hour).Format(time.RFC3339)
	svc := New(testDB, clock.NewFixed(base.Add(4*time.Hour)), nil)

	resp := decodeList(t, listAudit(t, svc, fmt.Sprintf(
		"operator_id=%d&target_type=t_channel&target_id=7&%s", opID, window)))
	if resp.Total != 1 || len(resp.Items) != 1 {
		t.Fatalf("target total=%d items=%d, want 1/1", resp.Total, len(resp.Items))
	}
	if resp.Items[0].TargetType != "t_channel" || resp.Items[0].TargetID != "7" {
		t.Fatalf("target=%s#%s, want t_channel#7",
			resp.Items[0].TargetType, resp.Items[0].TargetID)
	}

	resp = decodeList(t, listAudit(t, svc, fmt.Sprintf(
		"operator_id=%d&target_type=t_channel&%s", opID, window)))
	if resp.Total != 2 {
		t.Fatalf("target_type-only total=%d, want 2", resp.Total)
	}

	// No target params: the original query path, newest first.
	resp = decodeList(t, listAudit(t, svc, fmt.Sprintf("operator_id=%d&%s", opID, window)))
	if resp.Total != 3 {
		t.Fatalf("unfiltered total=%d, want 3", resp.Total)
	}
	wantActions := []string{"DISABLE", "UPDATE", "UPDATE"}
	for i, want := range wantActions {
		if resp.Items[i].Action != want {
			t.Fatalf("item %d action=%q, want %q (newest first)", i, resp.Items[i].Action, want)
		}
	}

	// The query limit follows maxModuleRunes (32).
	longTarget := strings.Repeat("x", 33)
	rec := listAudit(t, svc, fmt.Sprintf(
		"operator_id=%d&target_id=%s&%s", opID, longTarget, window))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("long target_id status=%d, want 400", rec.Code)
	}
	if code := decodeAPIError(t, rec); code != "invalid_query_parameter" {
		t.Fatalf("long target_id code=%q, want invalid_query_parameter", code)
	}
}

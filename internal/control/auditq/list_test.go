package auditq

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/platform/clock"
)

type apiErrorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func listAudit(t *testing.T, svc *Service, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/audit-logs?"+query, nil)
	rec := httptest.NewRecorder()
	svc.List(rec, req)
	return rec
}

func decodeList(t *testing.T, rec *httptest.ResponseRecorder) listResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var resp listResponse
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	return resp
}

func decodeAPIError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body apiErrorBody
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body (%d %s): %v", rec.Code, rec.Body.String(), err)
	}
	return body.Error.Code
}

func TestAuditListFiltersAndOrder(t *testing.T) {
	// A fixed year that no other test writes to, so the time-only query is
	// deterministic even against the shared dirty schema.
	base := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	opID := int64(9_000_000_000 + time.Now().UnixNano()%1_000_000)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(),
			`DELETE FROM t_admin_audit_log WHERE operator_id = ?`, opID)
	})

	// Insert in created_at order so the time-only page's id DESC order is also
	// created_at DESC, matching production (the audit writer always uses
	// "now" at insert time).
	insertAudit(t, opID, "USER", "LOGIN", "OK", base, "", "")
	insertAudit(t, opID, "CHANNEL", "CREATE", "OK", base.Add(30*time.Minute), "", "")
	insertAudit(t, opID, "USER", "UPDATE", "OK", base.Add(time.Hour),
		`{"status":"NORMAL"}`, `{"status":"DISABLED"}`)

	window := "from=" + base.Add(-time.Hour).Format(time.RFC3339) +
		"&to=" + base.Add(3*time.Hour).Format(time.RFC3339)
	svc := New(testDB, clock.NewFixed(base.Add(4*time.Hour)), nil)

	// operator_id: newest first, before/after preserved as raw JSON.
	resp := decodeList(t, listAudit(t, svc,
		fmt.Sprintf("operator_id=%d&%s&page_size=2", opID, window)))
	if resp.Total != 3 {
		t.Fatalf("total=%d, want 3", resp.Total)
	}
	if len(resp.Items) != 2 || resp.PageSize != 2 {
		t.Fatalf("items=%d page_size=%d, want 2/2", len(resp.Items), resp.PageSize)
	}
	if resp.Items[0].Action != "UPDATE" {
		t.Fatalf("first action=%q, want UPDATE (newest first)", resp.Items[0].Action)
	}
	if resp.Items[1].Action != "CREATE" {
		t.Fatalf("second action=%q, want CREATE", resp.Items[1].Action)
	}
	if resp.Items[0].OperatorIP != "192.0.2.1" {
		t.Fatalf("operator_ip=%q, want 192.0.2.1", resp.Items[0].OperatorIP)
	}
	if string(resp.Items[0].Before) != `{"status": "NORMAL"}` &&
		string(resp.Items[0].Before) != `{"status":"NORMAL"}` {
		t.Fatalf("before=%s, want the stored JSON", string(resp.Items[0].Before))
	}
	if string(resp.Items[0].After) == "" {
		t.Fatalf("after is empty, want the stored JSON")
	}

	// module: equality filter, case-normalised to the stored uppercase value.
	resp = decodeList(t, listAudit(t, svc, fmt.Sprintf("module=user&%s", window)))
	if resp.Total != 2 {
		t.Fatalf("module=user total=%d, want 2", resp.Total)
	}
	for _, it := range resp.Items {
		if it.Module != "USER" {
			t.Fatalf("module=%q, want USER", it.Module)
		}
	}

	// action combined with the operator prefix.
	resp = decodeList(t, listAudit(t, svc,
		fmt.Sprintf("operator_id=%d&action=LOGIN&%s", opID, window)))
	if resp.Total != 1 || resp.Items[0].Action != "LOGIN" {
		t.Fatalf("action filter total=%d items=%+v", resp.Total, resp.Items)
	}

	// Time-only: the query still uses an index (module skip scan) and is
	// ordered newest first.
	resp = decodeList(t, listAudit(t, svc, window))
	if resp.Total != 3 {
		t.Fatalf("time-only total=%d, want 3", resp.Total)
	}
	for i := 1; i < len(resp.Items); i++ {
		if resp.Items[i-1].CreatedAt.Before(resp.Items[i].CreatedAt) {
			t.Fatalf("items are not newest-first: %s before %s",
				resp.Items[i-1].CreatedAt, resp.Items[i].CreatedAt)
		}
	}
}

// 0024 makes a standalone action/result filter a bounded range scan over
// idx_aal_created, so the former 400 ("needs operator_id or module") is gone.
// The assertions below pin the actual filtered rows, not just the status code.
func TestAuditListAllowsActionAndResultAlone(t *testing.T) {
	base := time.Date(2000, 2, 1, 0, 0, 0, 0, time.UTC)
	opID := int64(9_200_000_000 + time.Now().UnixNano()%1_000_000)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(),
			`DELETE FROM t_admin_audit_log WHERE operator_id = ?`, opID)
	})
	insertAudit(t, opID, "USER", "LOGIN", "OK", base, "", "")
	insertAudit(t, opID, "USER", "UPDATE", "OK", base.Add(time.Minute), "", "")
	insertAudit(t, opID, "CHANNEL", "CREATE", "REJECTED", base.Add(2*time.Minute), "", "")

	window := "from=" + base.Add(-time.Hour).Format(time.RFC3339) +
		"&to=" + base.Add(3*time.Hour).Format(time.RFC3339)
	svc := New(testDB, clock.NewFixed(base.Add(4*time.Hour)), nil)

	for _, tt := range []struct {
		query string
		want  int64
	}{
		{"action=LOGIN", 1},
		{"result=OK", 2},
		{"action=LOGIN&result=OK", 1},
		{"result=REJECTED", 1},
	} {
		t.Run(tt.query, func(t *testing.T) {
			resp := decodeList(t, listAudit(t, svc, tt.query+"&"+window))
			if resp.Total != tt.want || int64(len(resp.Items)) != tt.want {
				t.Fatalf("total=%d items=%d, want %d", resp.Total, len(resp.Items), tt.want)
			}
			for _, it := range resp.Items {
				if it.OperatorID != fmt.Sprint(opID) {
					t.Fatalf("operator_id=%s, want %d", it.OperatorID, opID)
				}
			}
		})
	}
}

func TestAuditListParameterValidation(t *testing.T) {
	svc := New(testDB, clock.Real{}, nil)
	tests := []struct {
		name  string
		query string
	}{
		{"page_size over cap", "operator_id=1&page_size=101"},
		{"page below one", "operator_id=1&page=0"},
		{"bad operator id", "operator_id=abc"},
		{"unknown sort", "operator_id=1&sort=module"},
		{"ascending order", "operator_id=1&order=asc"},
		{"bad from", "operator_id=1&from=not-a-date"},
		{"from after to", "operator_id=1&from=2026-02-01&to=2026-01-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := listAudit(t, svc, tt.query)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
			if code := decodeAPIError(t, rec); code != apierr.CodeInvalidQueryParameter {
				t.Fatalf("code=%q, want %q", code, apierr.CodeInvalidQueryParameter)
			}
		})
	}
}

// The endpoint is newest-first by contract; a caller paginating in ascending
// order would never advance. order=asc is rejected rather than flipped.
func TestAuditListOrderIsDescending(t *testing.T) {
	svc := New(testDB, clock.Real{}, nil)
	rec := listAudit(t, svc, "operator_id=1&order=desc")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"items"`) {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

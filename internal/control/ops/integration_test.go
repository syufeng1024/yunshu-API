package ops

import (
	"bytes"
	"context"
	"net/http"
	"testing"
)

// restoreIntegration resets a seeded row to its 0026 state. Tests mutate the
// five fixed rows in place: inserting a sixth row would change what the public
// route is allowed to return.
func restoreIntegration(t *testing.T, toolKey string) {
	t.Helper()
	t.Cleanup(func() {
		if _, err := testDB.ExecContext(context.Background(), `
			UPDATE t_integration SET enabled = 0, deep_link_template = NULL
			 WHERE tool_key = ?`, toolKey); err != nil {
			t.Errorf("restore t_integration.%s: %v", toolKey, err)
		}
	})
}

func integrationID(t *testing.T, toolKey string) int64 {
	t.Helper()
	var id int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT id FROM t_integration WHERE tool_key = ?`, toolKey).Scan(&id); err != nil {
		t.Fatalf("t_integration.%s: %v", toolKey, err)
	}
	return id
}

// I1: the five tools are fixed; addressing an unknown key must not create a
// row and must answer resource_not_found.
func TestIntegrationUnknownToolKeyIsNotFound(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")

	rec := call(t, svc.UpdateIntegration, http.MethodPatch, "/api/admin/integrations/not-a-tool",
		map[string]any{
			"enabled":            true,
			"deep_link_template": "https://example.com/{key}",
		},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"toolKey": "not-a-tool"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404", rec.Code, rec.Body.String())
	}
	if got := errorBody(t, rec).Error.Code; got != "resource_not_found" {
		t.Fatalf("code=%q, want resource_not_found", got)
	}

	var count int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_integration WHERE tool_key = 'not-a-tool'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unknown tool_key was inserted (rows=%d)", count)
	}
}

// I2: D-30 Q2 — only {key} / {base_url} / {model} are valid. The refusal must
// name the offending placeholder, not just say "invalid template".
func TestIntegrationRejectsUnlistedPlaceholder(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	const key = "cherry-studio"
	restoreIntegration(t, key)

	rec := call(t, svc.UpdateIntegration, http.MethodPatch, "/api/admin/integrations/"+key,
		map[string]any{"deep_link_template": "https://example.com/import?token={token}"},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"toolKey": key}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	body := errorBody(t, rec)
	if body.Error.Code != "invalid_request_body" {
		t.Fatalf("code=%q, want invalid_request_body", body.Error.Code)
	}
	if !bytes.Contains([]byte(body.Error.Message), []byte("{token}")) {
		t.Fatalf("message=%q must name {token}", body.Error.Message)
	}

	var tpl *string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT deep_link_template FROM t_integration WHERE tool_key = ?`, key).Scan(&tpl); err != nil {
		t.Fatal(err)
	}
	if tpl != nil {
		t.Fatalf("rejected template was stored: %q", *tpl)
	}
}

// I3: enabling a tool with no template is refused; an empty template counts as
// no template (normalizeIntegrationTemplate maps "" to NULL).
func TestIntegrationCannotEnableWithoutTemplate(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	const key = "cherry-studio"
	restoreIntegration(t, key)

	for name, body := range map[string]map[string]any{
		"omitted": {"enabled": true},
		"empty":   {"enabled": true, "deep_link_template": ""},
	} {
		t.Run(name, func(t *testing.T) {
			rec := call(t, svc.UpdateIntegration, http.MethodPatch, "/api/admin/integrations/"+key,
				body, testCallOptions{uid: uid, role: "OPS",
					pathVal: map[string]string{"toolKey": key}})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
			if got := errorBody(t, rec).Error.Code; got != "invalid_request_body" {
				t.Fatalf("code=%q, want invalid_request_body", got)
			}
		})
	}
}

// I4: a valid save persists both fields and writes the before/after audit in
// the same transaction.
func TestIntegrationSaveWritesAudit(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	const key = "openai-sdk"
	const template = "https://example.com/{base_url}/{model}/{key}"
	restoreIntegration(t, key)
	id := integrationID(t, key)

	rec := call(t, svc.UpdateIntegration, http.MethodPatch, "/api/admin/integrations/"+key,
		map[string]any{"enabled": true, "deep_link_template": template},
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"toolKey": key}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var resp integrationDetailResponse
	decodeBody(t, rec, &resp)
	if resp.Integration.ToolKey != key || !resp.Integration.Enabled {
		t.Fatalf("response=%+v, want %s enabled", resp.Integration, key)
	}
	if resp.Integration.DeepLinkTemplate == nil || *resp.Integration.DeepLinkTemplate != template {
		t.Fatalf("template=%v, want %q", resp.Integration.DeepLinkTemplate, template)
	}

	var before, after []byte
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT before_json, after_json FROM t_admin_audit_log
		 WHERE module = 'INTEGRATION' AND action = 'UPDATE' AND target_id = ?
		 ORDER BY id DESC LIMIT 1`, itoa(id)).Scan(&before, &after); err != nil {
		t.Fatal(err)
	}
	// MySQL normalises JSON columns with a space after the colon.
	if !bytes.Contains(before, []byte(`"enabled": false`)) ||
		!bytes.Contains(before, []byte(`"deep_link_template": null`)) {
		t.Fatalf("before_json=%s, want disabled with a null template", before)
	}
	if !bytes.Contains(after, []byte(`"enabled": true`)) ||
		!bytes.Contains(after, []byte(template)) {
		t.Fatalf("after_json=%s, want enabled with %q", after, template)
	}
	t.Logf("integration audit before=%s after=%s", before, after)
}

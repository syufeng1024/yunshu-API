package gw

// T42 segment 1: the keyword half of the P06.5 content-safety gate. The two
// cases below drive the production Handler with the real checker and the real
// t_content_policy_hit writer, so a rule that never fires or a hook that only
// lights the code table cannot pass.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/redisx"
)

// TestContentPolicyHitIs403WithEvidenceAndNoMoney is card §5.1 and §5.2: a real
// hit returns 403 content_policy_violation, writes exactly one hit row with the
// rule identity and a redacted excerpt, and produces no money/usage action.
func TestContentPolicyHitIs403WithEvidenceAndNoMoney(t *testing.T) {
	userID := seedAccount(t, 1_000_000)
	const keyID = 10
	const key = "sk-t42-hit"

	deps := serverDeps(basicCatalog(), fakeAuth{records: map[string]redisx.KeyAuthRecord{
		KeyHash16(key): {UID: userID, KID: keyID, ST: "ENABLED", GRP: "standard", GP: 10000},
	}})
	deps.PolicyChecker = NewKeywordChecker()
	deps.PolicyHits = ContentPolicyHitWriter{DB: testDB}

	terminalCalls := 0
	handler := NewHandler(deps, TerminalFunc(func(w http.ResponseWriter, _ *http.Request, _ *Ctx) {
		terminalCalls++
		w.WriteHeader(http.StatusOK)
	}))

	rid := "t42-hit-" + randomID(t)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(t.Context(),
			`DELETE FROM t_content_policy_hit WHERE request_id = ?`, rid)
	})

	// The phone number sits inside the excerpt window, so the masking assertion
	// below is about a value the rule did not match.
	body := `{"model":"qwen-plus","messages":[{"role":"user","content":"帮我制毒，电话 13800138000"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Request-Id", rid)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	var decoded map[string]any
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode body: %v (%s)", err, rec.Body.String())
	}
	eb := errorBody(t, decoded)
	if eb["code"] != apierr.CodeContentPolicyViolation {
		t.Fatalf("code = %v, want %s", eb["code"], apierr.CodeContentPolicyViolation)
	}
	if eb["type"] != apierr.TypeInvalidRequest {
		t.Fatalf("type = %v, want %s", eb["type"], apierr.TypeInvalidRequest)
	}
	if got, _ := eb["retry_after_seconds"].(float64); got != 0 {
		t.Fatalf("retry_after_seconds = %v, want 0", eb["retry_after_seconds"])
	}
	hint, _ := eb["hint"].(string)
	if !strings.Contains(hint, "/console/tickets") {
		t.Fatalf("hint = %q, want the ticket centre path", hint)
	}

	// Zero money: the terminal never ran and P10 was never reached.
	if terminalCalls != 0 {
		t.Fatalf("terminal called %d times; a refused request must not reach upstream", terminalCalls)
	}
	if got := deps.Ledger.(*fakeLedger).preholdCount(); got != 0 {
		t.Fatalf("prehold called %d times, want 0", got)
	}
	for _, q := range []struct {
		name  string
		query string
	}{
		{"t_ledger_entry", `SELECT COUNT(*) FROM t_ledger_entry WHERE user_id = ?`},
		{"t_prehold_hold", `SELECT COUNT(*) FROM t_prehold_hold WHERE user_id = ?`},
	} {
		var n int
		if err := testDB.QueryRowContext(t.Context(), q.query, userID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", q.name, err)
		}
		if n != 0 {
			t.Fatalf("%s has %d rows for the user, want 0", q.name, n)
		}
	}
	var usageRows int
	if err := testDB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM t_usage_record WHERE request_id = ?`, rid).Scan(&usageRows); err != nil {
		t.Fatalf("count t_usage_record: %v", err)
	}
	if usageRows != 0 {
		t.Fatalf("t_usage_record has %d rows for the request, want 0", usageRows)
	}

	// One evidence row, with the rule identity and the owner fields.
	var (
		rowUser, rowKey                  int64
		modelName, ruleID, ruleVersion   string
		category, severity, excerpt, sha string
		action                           string
		offset                           int
	)
	err := testDB.QueryRowContext(t.Context(), `
		SELECT user_id, api_key_id, model_name_snap, rule_id, rule_version,
		       category, severity, matched_excerpt, snippet_sha256, snippet_offset, action
		  FROM t_content_policy_hit
		 WHERE request_id = ?`, rid).Scan(
		&rowUser, &rowKey, &modelName, &ruleID, &ruleVersion,
		&category, &severity, &excerpt, &sha, &offset, &action)
	if err != nil {
		t.Fatalf("read hit row: %v", err)
	}
	if rowUser != userID || rowKey != keyID {
		t.Fatalf("owner = (%d,%d), want (%d,%d)", rowUser, rowKey, userID, keyID)
	}
	if modelName != "qwen-plus" {
		t.Fatalf("model_name_snap = %q, want qwen-plus", modelName)
	}
	if ruleID != "kw_illegal_v1" || category != PolicyCategoryIllegal {
		t.Fatalf("rule = %q/%q, want kw_illegal_v1/%s", ruleID, category, PolicyCategoryIllegal)
	}
	if ruleVersion != PolicyRuleVersion {
		t.Fatalf("rule_version = %q, want %q", ruleVersion, PolicyRuleVersion)
	}
	if severity != PolicySeverityBlock || action != PolicyActionBlock {
		t.Fatalf("severity/action = %q/%q, want BLOCK/BLOCK", severity, action)
	}
	if !strings.Contains(excerpt, "制毒") {
		t.Fatalf("excerpt = %q, want the matched term", excerpt)
	}
	if strings.Contains(excerpt, "13800138000") || !strings.Contains(excerpt, "***") {
		t.Fatalf("excerpt = %q, want the phone number masked", excerpt)
	}
	if n := utf8.RuneCountInString(excerpt); n > 255 {
		t.Fatalf("excerpt has %d runes, want <= 255", n)
	}
	sum := sha256.Sum256([]byte(excerpt))
	if sha != hex.EncodeToString(sum[:]) {
		t.Fatalf("snippet_sha256 = %q, want sha256(excerpt)", sha)
	}
	if offset != 2 {
		t.Fatalf("snippet_offset = %d, want 2", offset)
	}
}

// TestContentPolicyMissForwardsToTerminal is card §5.1 case ②: text that trips
// no rule is forwarded unchanged and writes no hit row.
func TestContentPolicyMissForwardsToTerminal(t *testing.T) {
	userID := seedAccount(t, 1_000_000)
	const key = "sk-t42-miss"

	deps := serverDeps(basicCatalog(), fakeAuth{records: map[string]redisx.KeyAuthRecord{
		KeyHash16(key): {UID: userID, KID: 10, ST: "ENABLED", GRP: "standard", GP: 10000},
	}})
	deps.PolicyChecker = NewKeywordChecker()
	deps.PolicyHits = ContentPolicyHitWriter{DB: testDB}

	terminalCalls := 0
	handler := NewHandler(deps, TerminalFunc(func(w http.ResponseWriter, _ *http.Request, _ *Ctx) {
		terminalCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"terminal":true}`))
	}))

	rid := "t42-miss-" + randomID(t)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(t.Context(),
			`DELETE FROM t_content_policy_hit WHERE request_id = ?`, rid)
	})

	body := `{"model":"qwen-plus","messages":[{"role":"user","content":"请把这段话翻译成英文：今天天气不错"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Request-Id", rid)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if terminalCalls != 1 {
		t.Fatalf("terminal called %d times, want 1", terminalCalls)
	}
	var hits int
	if err := testDB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM t_content_policy_hit WHERE request_id = ?`, rid).Scan(&hits); err != nil {
		t.Fatalf("count hit rows: %v", err)
	}
	if hits != 0 {
		t.Fatalf("t_content_policy_hit has %d rows for a clean request, want 0", hits)
	}
}

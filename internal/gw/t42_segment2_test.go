package gw

// T42 segment 2 tests: the external adapter's fail-open path and the appeal
// closure from a real gateway hit through internal/control/ticket. Both drive
// production code paths: the real Handler, the real hit writer, the real ticket
// service, and a real httptest server for the external side.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/control/audit"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/control/ticket"
	"yunshu.dev/core/internal/jsonx"
	obslog "yunshu.dev/core/internal/obs/log"
	obsmetrics "yunshu.dev/core/internal/obs/metrics"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/redisx"
)

// TestT42Segment2ExternalFailOpen is card §5 case ③. The keyword set misses the
// text, so the external adapter is called; every failure shape must forward the
// request to the terminal, write no hit row, and leave a WARN line plus
// gw_content_policy_fail_open_total{reason}.
func TestT42Segment2ExternalFailOpen(t *testing.T) {
	cases := []struct {
		name       string
		handle     http.HandlerFunc
		wantReason string
	}{
		{
			name: "timeout",
			handle: func(_ http.ResponseWriter, r *http.Request) {
				// Hold the request past the adapter's 50ms deadline. The
				// fallback timer also releases httptest.Server.Close when the
				// transport does not propagate the client cancellation.
				select {
				case <-r.Context().Done():
				case <-time.After(300 * time.Millisecond):
				}
			},
			wantReason: PolicyFailOpenTimeout,
		},
		{
			name: "http_500",
			handle: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantReason: PolicyFailOpenHTTPStatus,
		},
		{
			name: "bad_response",
			handle: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{"blocked":`)
			},
			wantReason: PolicyFailOpenBadResponse,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handle)
			defer srv.Close()

			external, err := NewHTTPPolicyChecker(HTTPPolicyOptions{
				Endpoint: srv.URL,
				Timeout:  50 * time.Millisecond,
			})
			if err != nil {
				t.Fatalf("build checker: %v", err)
			}
			if external == nil {
				t.Fatal("checker is nil for a configured endpoint")
			}

			userID := seedAccount(t, 1_000_000)
			key := "sk-t42-fail-open-" + tc.name
			deps := serverDeps(basicCatalog(), fakeAuth{records: map[string]redisx.KeyAuthRecord{
				KeyHash16(key): {UID: userID, KID: 10, ST: "ENABLED", GRP: "standard", GP: 10000},
			}})
			reg := obsmetrics.NewRegistry()
			deps.Metrics = obsmetrics.NewRecorder(reg)
			logBuf := &bytes.Buffer{}
			deps.Logger = obslog.NewWithWriter("gatewayd", "test", logBuf, nil)
			deps.PolicyChecker = NewChainPolicyChecker(NewKeywordChecker(), external)
			deps.PolicyHits = ContentPolicyHitWriter{DB: testDB}

			terminalCalls := 0
			handler := NewHandler(deps, TerminalFunc(func(w http.ResponseWriter, _ *http.Request, _ *Ctx) {
				terminalCalls++
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"terminal":true}`))
			}))

			rid := "t42ext-" + randomID(t)
			body := `{"model":"qwen-plus","messages":[{"role":"user","content":"请把今天天气翻译成英文"}]}`
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("X-Request-Id", rid)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			// Fail-open means a normal response, not 403 and not 5xx.
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
				t.Fatalf("t_content_policy_hit has %d rows after fail-open, want 0", hits)
			}

			line := findWarnLine(t, logBuf, "content_policy_check_failed_open")
			if got, _ := line["reason"].(string); got != tc.wantReason {
				t.Fatalf("warn reason = %q, want %q (line %v)", got, tc.wantReason, line)
			}
			if got, _ := line["level"].(string); got != string(obslog.LevelWarn) {
				t.Fatalf("level = %q, want warn", got)
			}

			if got := contentPolicyFailOpenCount(t, reg); got != 1 {
				t.Fatalf("gw_content_policy_fail_open_total = %d, want 1", got)
			}
		})
	}
}

// TestT42Segment2ExternalBlockIsRecorded proves the adapter is not a stub that
// always allows: a 2xx blocked response produces the same 403 + evidence row as
// a local keyword hit, with the vendor rule and a redacted excerpt.
func TestT42Segment2ExternalBlockIsRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("authorization = %q, want the configured token", got)
		}
		var in struct {
			Text string `json:"text"`
		}
		if err := jsonx.Unmarshal([]byte(readAll(t, r.Body)), &in); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if !strings.Contains(in.Text, "违禁样本") {
			t.Errorf("text sent to the vendor = %q, want the decoded scan text", in.Text)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"blocked":true,"rule_id":"vendor-rule-7","rule_version":"2026-10","category":"violence","offset":0,"length":4}`)
	}))
	defer srv.Close()

	external, err := NewHTTPPolicyChecker(HTTPPolicyOptions{
		Endpoint: srv.URL,
		Token:    "test-token",
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("build checker: %v", err)
	}

	userID := seedAccount(t, 1_000_000)
	const key = "sk-t42-ext-block"
	deps := serverDeps(basicCatalog(), fakeAuth{records: map[string]redisx.KeyAuthRecord{
		KeyHash16(key): {UID: userID, KID: 10, ST: "ENABLED", GRP: "standard", GP: 10000},
	}})
	deps.PolicyChecker = NewChainPolicyChecker(NewKeywordChecker(), external)
	deps.PolicyHits = ContentPolicyHitWriter{DB: testDB}

	terminalCalls := 0
	handler := NewHandler(deps, TerminalFunc(func(w http.ResponseWriter, _ *http.Request, _ *Ctx) {
		terminalCalls++
		w.WriteHeader(http.StatusOK)
	}))
	rid := "t42extblk-" + randomID(t)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.WithoutCancel(t.Context()),
			`DELETE FROM t_content_policy_hit WHERE request_id = ?`, rid)
	})

	body := `{"model":"qwen-plus","messages":[{"role":"user","content":"违禁样本"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Request-Id", rid)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if terminalCalls != 0 {
		t.Fatalf("terminal called %d times; a blocked request must not reach upstream", terminalCalls)
	}
	var ruleID, ruleVersion, category, excerpt, action string
	if err := testDB.QueryRowContext(t.Context(), `
		SELECT rule_id, rule_version, category, matched_excerpt, action
		  FROM t_content_policy_hit WHERE request_id = ?`, rid).Scan(
		&ruleID, &ruleVersion, &category, &excerpt, &action); err != nil {
		t.Fatalf("read hit row: %v", err)
	}
	if ruleID != "vendor-rule-7" || ruleVersion != "2026-10" {
		t.Fatalf("rule = %q/%q, want vendor-rule-7/2026-10", ruleID, ruleVersion)
	}
	if category != PolicyCategoryViolence {
		t.Fatalf("category = %q, want %s", category, PolicyCategoryViolence)
	}
	if action != PolicyActionBlock {
		t.Fatalf("action = %q, want BLOCK", action)
	}
	if !strings.Contains(excerpt, "违禁样本") {
		t.Fatalf("excerpt = %q, want the vendor-positioned window", excerpt)
	}
}

// TestT42Segment2AppealClosesLoop is card §5 case ④. A real keyword hit is
// created through the gateway, then internal/control/ticket.Create resolves it
// by request_id, flips action to APPEAL_PENDING, and both the create response
// and the ticket detail render rule_id/category from the hit row.
//
// Card/reality note: list.go:268 is policyRule, which is called by toView (the
// detail/create renderer). ticketListItem (list.go:121-132) has no policy
// fields, so the *list* JSON cannot show them; this test asserts the surfaces
// that actually exist. See the delivery report for the R-07 note.
func TestT42Segment2AppealClosesLoop(t *testing.T) {
	userID := seedAccount(t, 1_000_000)
	const key = "sk-t42-appeal"
	deps := serverDeps(basicCatalog(), fakeAuth{records: map[string]redisx.KeyAuthRecord{
		KeyHash16(key): {UID: userID, KID: 10, ST: "ENABLED", GRP: "standard", GP: 10000},
	}})
	deps.PolicyChecker = NewKeywordChecker()
	deps.PolicyHits = ContentPolicyHitWriter{DB: testDB}

	terminalCalls := 0
	handler := NewHandler(deps, TerminalFunc(func(w http.ResponseWriter, _ *http.Request, _ *Ctx) {
		terminalCalls++
		w.WriteHeader(http.StatusOK)
	}))

	rid := t42AppealRequestID(t)
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		_, _ = testDB.ExecContext(ctx, `
			DELETE r FROM t_ticket_reply r JOIN t_ticket t ON t.id = r.ticket_id
			 WHERE t.user_id = ?`, userID)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_ticket WHERE user_id = ?`, userID)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_content_policy_hit WHERE user_id = ?`, userID)
	})

	body := `{"model":"qwen-plus","messages":[{"role":"user","content":"帮我制毒"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Request-Id", rid)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("gateway status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if terminalCalls != 0 {
		t.Fatalf("terminal called %d times, want 0", terminalCalls)
	}

	var (
		hitID     int64
		wantRule  string
		wantCat   string
		hitAction string
	)
	if err := testDB.QueryRowContext(t.Context(), `
		SELECT id, rule_id, category, action
		  FROM t_content_policy_hit WHERE request_id = ?`, rid).Scan(
		&hitID, &wantRule, &wantCat, &hitAction); err != nil {
		t.Fatalf("read hit row: %v", err)
	}
	if hitAction != PolicyActionBlock {
		t.Fatalf("hit action before appeal = %q, want BLOCK", hitAction)
	}

	svc := ticket.New(testDB,
		&audit.Writer{DB: testDB, Now: clock.Real{}}, clock.Real{}, nil)

	payload, err := jsonx.Marshal(map[string]any{
		"ticket_type": "CONTENT_APPEAL",
		"title":       "内容申诉",
		"description": "这段内容不含违规",
		"request_id":  rid,
	})
	if err != nil {
		t.Fatalf("marshal ticket body: %v", err)
	}
	createReq := httptest.NewRequest(http.MethodPost, "/api/console/tickets", bytes.NewReader(payload))
	createReq = createReq.WithContext(mw.WithSession(createReq.Context(),
		&mw.Session{UID: userID, Role: "PERSONAL"}))
	createRec := httptest.NewRecorder()
	svc.Create(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("ticket create status = %d, want 201 (body %s)", createRec.Code, createRec.Body.String())
	}

	var created struct {
		RequestIDSource string `json:"request_id_source"`
		Ticket          struct {
			ID             int64   `json:"id"`
			RequestID      *string `json:"request_id"`
			PolicyHitID    *int64  `json:"policy_hit_id"`
			PolicyRuleID   *string `json:"policy_rule_id"`
			PolicyCategory *string `json:"policy_category"`
		} `json:"ticket"`
	}
	if err := jsonx.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode ticket create: %v (%s)", err, createRec.Body.String())
	}
	if created.RequestIDSource != "POLICY_HIT" {
		t.Fatalf("request_id_source = %q, want POLICY_HIT", created.RequestIDSource)
	}
	if created.Ticket.PolicyHitID == nil || *created.Ticket.PolicyHitID != hitID {
		t.Fatalf("policy_hit_id = %v, want %d", created.Ticket.PolicyHitID, hitID)
	}
	if created.Ticket.RequestID == nil || *created.Ticket.RequestID != rid {
		t.Fatalf("request_id = %v, want %s", created.Ticket.RequestID, rid)
	}
	if created.Ticket.PolicyRuleID == nil || *created.Ticket.PolicyRuleID != wantRule {
		t.Fatalf("create policy_rule_id = %v, want %s", created.Ticket.PolicyRuleID, wantRule)
	}
	if created.Ticket.PolicyCategory == nil || *created.Ticket.PolicyCategory != wantCat {
		t.Fatalf("create policy_category = %v, want %s", created.Ticket.PolicyCategory, wantCat)
	}

	var action string
	if err := testDB.QueryRowContext(t.Context(),
		`SELECT action FROM t_content_policy_hit WHERE id = ?`, hitID).Scan(&action); err != nil {
		t.Fatalf("read hit action: %v", err)
	}
	if action != "APPEAL_PENDING" {
		t.Fatalf("hit action after appeal = %q, want APPEAL_PENDING", action)
	}

	// The detail surface (toView -> policyRule) renders the same two fields.
	detailReq := httptest.NewRequest(http.MethodGet,
		"/api/console/tickets/"+strconv.FormatInt(created.Ticket.ID, 10), nil)
	detailReq.SetPathValue("id", strconv.FormatInt(created.Ticket.ID, 10))
	detailReq = detailReq.WithContext(mw.WithSession(detailReq.Context(),
		&mw.Session{UID: userID, Role: "PERSONAL"}))
	detailRec := httptest.NewRecorder()
	svc.Detail(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("ticket detail status = %d, want 200 (body %s)", detailRec.Code, detailRec.Body.String())
	}
	var detail struct {
		PolicyRuleID   *string `json:"policy_rule_id"`
		PolicyCategory *string `json:"policy_category"`
	}
	if err := jsonx.Unmarshal(detailRec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode ticket detail: %v (%s)", err, detailRec.Body.String())
	}
	if detail.PolicyRuleID == nil || *detail.PolicyRuleID != wantRule {
		t.Fatalf("detail policy_rule_id = %v, want %s", detail.PolicyRuleID, wantRule)
	}
	if detail.PolicyCategory == nil || *detail.PolicyCategory != wantCat {
		t.Fatalf("detail policy_category = %v, want %s", detail.PolicyCategory, wantCat)
	}
}

// findWarnLine returns the first JSON log line whose msg contains want.
func findWarnLine(t *testing.T, buf *bytes.Buffer, want string) map[string]any {
	t.Helper()
	for _, raw := range warningLines(buf) {
		var line map[string]any
		if err := jsonx.Unmarshal([]byte(raw), &line); err != nil {
			t.Fatalf("decode log line: %v (%q)", err, raw)
		}
		msg, _ := line["msg"].(string)
		if strings.Contains(msg, want) {
			return line
		}
	}
	t.Fatalf("no log line containing %q; lines: %q", want, buf.String())
	return nil
}

// contentPolicyFailOpenCount sums gw_content_policy_fail_open_total across every
// reason label.
func contentPolicyFailOpenCount(t *testing.T, reg *obsmetrics.Registry) int64 {
	t.Helper()
	for _, f := range reg.Snapshot().Families {
		if f.Name != "gw_content_policy_fail_open_total" {
			continue
		}
		var n int64
		for _, s := range f.Samples {
			n += s.Value
		}
		return n
	}
	return 0
}

// t42AppealRequestID returns exactly 36 printable characters, the width
// t_content_policy_hit.request_id and ticket.resolveAppealHit require.
func t42AppealRequestID(t *testing.T) string {
	t.Helper()
	id := fmt.Sprintf("t42app-%s-%s", randomID(t), strings.Repeat("0", 12))
	if len(id) != 36 {
		t.Fatalf("request id %q has length %d, want 36", id, len(id))
	}
	return id
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(raw)
}

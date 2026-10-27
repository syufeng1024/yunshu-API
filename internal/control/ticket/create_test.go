package ticket

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// F19-1: the create path re-checks the same 90-day ownership window the picker
// applies. A foreign id, an expired id and an absent id must be byte-for-byte
// the same refusal.
func TestF19_1CreateRejectsRequestIDOutsideOwnWindow(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	owner := newTestUser(t, "PERSONAL")
	other := newTestUser(t, "PERSONAL")

	recent := newRequestID()
	seedUsage(t, owner, recent, testBase.Add(-10*24*time.Hour))
	stale := newRequestID()
	seedUsage(t, owner, stale, testBase.Add(-100*24*time.Hour))
	foreign := newRequestID()
	seedUsage(t, other, foreign, testBase.Add(-24*time.Hour))
	missing := newRequestID()

	ok := createTicket(t, svc, owner, map[string]any{
		"ticket_type": "TECH",
		"title":       "最近请求",
		"description": "正文",
		"request_id":  recent,
	})
	if ok.Ticket.RequestID == nil || *ok.Ticket.RequestID != recent {
		t.Fatalf("request_id = %v, want %s", ok.Ticket.RequestID, recent)
	}
	if ok.RequestIDSource != "USER" {
		t.Fatalf("request_id_source = %q, want USER", ok.RequestIDSource)
	}

	foreignRec := callUser(t, svc.Create, http.MethodPost, "/api/console/tickets", map[string]any{
		"ticket_type": "TECH", "title": "越权", "description": "正文", "request_id": foreign,
	}, owner)
	if foreignRec.Code != http.StatusNotFound {
		t.Fatalf("foreign request_id status=%d body=%s, want 404", foreignRec.Code, foreignRec.Body.String())
	}
	foreignErr := errorOf(t, foreignRec)

	missingRec := callUser(t, svc.Create, http.MethodPost, "/api/console/tickets", map[string]any{
		"ticket_type": "TECH", "title": "不存在", "description": "正文", "request_id": missing,
	}, owner)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing request_id status=%d, want 404", missingRec.Code)
	}
	missingErr := errorOf(t, missingRec)

	if foreignErr.Error.Code != "resource_not_found" || missingErr.Error.Code != "resource_not_found" {
		t.Fatalf("codes foreign=%q missing=%q, want resource_not_found for both",
			foreignErr.Error.Code, missingErr.Error.Code)
	}
	if foreignErr.Error.Message != missingErr.Error.Message {
		t.Fatalf("foreign/missing messages differ:\n  foreign=%q\n  missing=%q",
			foreignErr.Error.Message, missingErr.Error.Message)
	}
	staleRec := callUser(t, svc.Create, http.MethodPost, "/api/console/tickets", map[string]any{
		"ticket_type": "TECH", "title": "过期", "description": "正文", "request_id": stale,
	}, owner)
	if staleRec.Code != http.StatusNotFound {
		t.Fatalf("100-day-old own request_id status=%d, want 404", staleRec.Code)
	}
	staleErr := errorOf(t, staleRec)
	if staleErr.Error.Message != missingErr.Error.Message {
		t.Fatalf("stale message=%q, want the same message as missing", staleErr.Error.Message)
	}
	t.Logf("越权 request_id → HTTP %d %s %q", foreignRec.Code, foreignErr.Error.Code, foreignErr.Error.Message)
	t.Logf("不存在 request_id → HTTP %d %s %q", missingRec.Code, missingErr.Error.Code, missingErr.Error.Message)
	t.Logf("100 天前本人 request_id → HTTP %d %s %q", staleRec.Code, staleErr.Error.Code, staleErr.Error.Message)
}

// F19-1: the picker never returns a 90-day-old row or another account's row.
func TestF19_1PickerListsOnlyOwnLast90Days(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	owner := newTestUser(t, "PERSONAL")
	other := newTestUser(t, "PERSONAL")

	inWindow := newRequestID()
	seedUsage(t, owner, inWindow, testBase.Add(-10*24*time.Hour))
	outside := newRequestID()
	seedUsage(t, owner, outside, testBase.Add(-91*24*time.Hour))
	foreign := newRequestID()
	seedUsage(t, other, foreign, testBase.Add(-1*24*time.Hour))

	rec := callUser(t, svc.Picker, http.MethodGet, "/api/console/request-ids", nil, owner)
	if rec.Code != http.StatusOK {
		t.Fatalf("picker status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp requestIDListResponse
	decodeBody(t, rec, &resp)
	if resp.Total != 1 || len(resp.Items) != 1 {
		t.Fatalf("picker total=%d items=%d, want 1/1 (body=%s)", resp.Total, len(resp.Items), rec.Body.String())
	}
	if resp.Items[0].RequestID != inWindow {
		t.Fatalf("picker returned %s, want only %s", resp.Items[0].RequestID, inWindow)
	}
	if !resp.Since.Equal(testBase.Add(-pickerWindow)) {
		t.Fatalf("since=%s, want %s", resp.Since, testBase.Add(-pickerWindow))
	}

	q := callUser(t, svc.Picker, http.MethodGet, "/api/console/request-ids?q="+inWindow[:8], nil, owner)
	var qResp requestIDListResponse
	decodeBody(t, q, &qResp)
	if qResp.Total != 1 || qResp.Items[0].RequestID != inWindow {
		t.Fatalf("picker q total=%d body=%s", qResp.Total, q.Body.String())
	}
}

// F19-3: a CONTENT_APPEAL ticket derives the rule id and request_id from
// t_content_policy_hit. The user never supplies a rule id.
func TestF19_3ContentAppealDerivesRuleAndRequest(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	owner := newTestUser(t, "PERSONAL")

	reqID := newRequestID()
	hitID := seedHit(t, owner, reqID, "kw_terror_v3", hitActionBlock, testBase.Add(-time.Hour))

	resp := createTicket(t, svc, owner, map[string]any{
		"ticket_type": "CONTENT_APPEAL",
		"title":       "内容申诉",
		"description": "这段内容不含违规",
		"request_id":  reqID,
	})
	if resp.RequestIDSource != "POLICY_HIT" {
		t.Fatalf("request_id_source=%q, want POLICY_HIT", resp.RequestIDSource)
	}
	if resp.Ticket.PolicyHitID == nil || *resp.Ticket.PolicyHitID != hitID {
		t.Fatalf("policy_hit_id=%v, want %d", resp.Ticket.PolicyHitID, hitID)
	}
	if resp.Ticket.PolicyRuleID == nil || *resp.Ticket.PolicyRuleID != "kw_terror_v3" {
		t.Fatalf("policy_rule_id=%v, want kw_terror_v3", resp.Ticket.PolicyRuleID)
	}
	if resp.Ticket.RequestID == nil || *resp.Ticket.RequestID != reqID {
		t.Fatalf("request_id=%v, want %s", resp.Ticket.RequestID, reqID)
	}

	action, appealAt, appealNote := hitRow(t, hitID)
	if action != hitActionAppealPending {
		t.Fatalf("hit action=%q, want APPEAL_PENDING", action)
	}
	if !appealAt.Valid || !appealAt.Time.UTC().Equal(testBase) {
		t.Fatalf("appeal_at=%v, want %s", appealAt, testBase)
	}
	if !appealNote.Valid || appealNote.String != "这段内容不含违规" {
		t.Fatalf("appeal_note=%q", appealNote.String)
	}

	// A second appeal on the same hit is a state conflict, not a new ticket.
	again := callUser(t, svc.Create, http.MethodPost, "/api/console/tickets", map[string]any{
		"ticket_type": "CONTENT_APPEAL", "title": "再申诉", "description": "正文", "request_id": reqID,
	}, owner)
	if again.Code != http.StatusConflict {
		t.Fatalf("second appeal status=%d body=%s, want 409", again.Code, again.Body.String())
	}
	if code := errorOf(t, again).Error.Code; code != "resource_state_conflict" {
		t.Fatalf("second appeal code=%q, want resource_state_conflict", code)
	}
}

// The hit record is the evidence: when both identifiers are sent, the hit's
// request_id wins and the response says the value was corrected.
func TestF19_3PolicyHitIDWinsOverRequestID(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	owner := newTestUser(t, "PERSONAL")

	hitRequest := newRequestID()
	hitID := seedHit(t, owner, hitRequest, "kw_illegal_v2", hitActionBlock, testBase.Add(-time.Hour))
	typed := newRequestID()

	resp := createTicket(t, svc, owner, map[string]any{
		"ticket_type":   "CONTENT_APPEAL",
		"title":         "申诉",
		"description":   "正文",
		"policy_hit_id": hitID,
		"request_id":    typed,
	})
	if resp.Ticket.RequestID == nil || *resp.Ticket.RequestID != hitRequest {
		t.Fatalf("request_id=%v, want the hit's %s", resp.Ticket.RequestID, hitRequest)
	}
	if resp.Notice == "" || !strings.Contains(resp.Notice, "命中记录") {
		t.Fatalf("notice=%q, want an explanation that the hit record won", resp.Notice)
	}
}

// A foreign hit and a nonexistent hit answer the same 404 body: zero leak.
func TestF19_3ForeignHitIsIndistinguishableFromMissing(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	owner := newTestUser(t, "PERSONAL")
	other := newTestUser(t, "PERSONAL")

	foreignHit := seedHit(t, other, newRequestID(), "kw_terror_v3", hitActionBlock, testBase.Add(-time.Hour))
	foreignRec := callUser(t, svc.Create, http.MethodPost, "/api/console/tickets", map[string]any{
		"ticket_type": "CONTENT_APPEAL", "title": "越权", "description": "正文",
		"policy_hit_id": foreignHit,
	}, owner)
	missingRec := callUser(t, svc.Create, http.MethodPost, "/api/console/tickets", map[string]any{
		"ticket_type": "CONTENT_APPEAL", "title": "不存在", "description": "正文",
		"policy_hit_id": int64(999999999),
	}, owner)

	if foreignRec.Code != http.StatusNotFound || missingRec.Code != http.StatusNotFound {
		t.Fatalf("statuses foreign=%d missing=%d, want 404/404", foreignRec.Code, missingRec.Code)
	}
	f, m := errorOf(t, foreignRec), errorOf(t, missingRec)
	if f.Error.Code != m.Error.Code || f.Error.Message != m.Error.Message {
		t.Fatalf("foreign/missing differ: %q/%q vs %q/%q",
			f.Error.Code, f.Error.Message, m.Error.Code, m.Error.Message)
	}
	t.Logf("他人命中记录 → HTTP %d %s %q", foreignRec.Code, f.Error.Code, f.Error.Message)
	t.Logf("不存在命中记录 → HTTP %d %s %q", missingRec.Code, m.Error.Code, m.Error.Message)
}

func TestContentAppealEligibility(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	owner := newTestUser(t, "PERSONAL")

	oldHit := seedHit(t, owner, newRequestID(), "kw_old", hitActionBlock, testBase.Add(-31*24*time.Hour))
	rec := callUser(t, svc.Create, http.MethodPost, "/api/console/tickets", map[string]any{
		"ticket_type": "CONTENT_APPEAL", "title": "过期申诉", "description": "正文",
		"policy_hit_id": oldHit,
	}, owner)
	if rec.Code != http.StatusConflict {
		t.Fatalf("31-day-old hit status=%d body=%s, want 409", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "30 天") {
		t.Fatalf("message=%s, want the 30-day window", rec.Body.String())
	}

	pendingHit := seedHit(t, owner, newRequestID(), "kw_pending", hitActionAppealPending, testBase.Add(-time.Hour))
	rec = callUser(t, svc.Create, http.MethodPost, "/api/console/tickets", map[string]any{
		"ticket_type": "CONTENT_APPEAL", "title": "重复", "description": "正文",
		"policy_hit_id": pendingHit,
	}, owner)
	if rec.Code != http.StatusConflict {
		t.Fatalf("APPEAL_PENDING hit status=%d, want 409", rec.Code)
	}

	// A hit reference is mandatory for this type; a non-appeal type must not
	// smuggle one in.
	rec = callUser(t, svc.Create, http.MethodPost, "/api/console/tickets", map[string]any{
		"ticket_type": "CONTENT_APPEAL", "title": "无关联", "description": "正文",
	}, owner)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("appeal without hit status=%d, want 400", rec.Code)
	}
	rec = callUser(t, svc.Create, http.MethodPost, "/api/console/tickets", map[string]any{
		"ticket_type": "TECH", "title": "错用", "description": "正文", "policy_hit_id": pendingHit,
	}, owner)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("policy_hit_id on TECH status=%d, want 400", rec.Code)
	}
}

func TestCreateValidatesFieldsAndNormalisesText(t *testing.T) {
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	owner := newTestUser(t, "PERSONAL")

	cases := []struct {
		name string
		body map[string]any
	}{
		{"unknown type", simpleTicket("REOPENED", "标题")},
		{"empty title", simpleTicket("TECH", "   ")},
		{"title too long", simpleTicket("TECH", strings.Repeat("字", maxTitleRunes+1))},
		{"empty description", map[string]any{"ticket_type": "TECH", "title": "标题", "description": " \r\n "}},
		{"description too long", map[string]any{
			"ticket_type": "TECH", "title": "标题",
			"description": strings.Repeat("字", maxDescriptionRunes+1),
		}},
		{"malformed request_id", map[string]any{
			"ticket_type": "TECH", "title": "标题", "description": "正文", "request_id": "not-a-uuid",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := callUser(t, svc.Create, http.MethodPost, "/api/console/tickets", tc.body, owner)
			if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s, want 400 (or 404 for an unusable id)",
					rec.Code, rec.Body.String())
			}
		})
	}

	resp := createTicket(t, svc, owner, map[string]any{
		"ticket_type": "OTHER",
		"title":       "  多行\r\n标题\t测试  ",
		"description": "第一行\r\n第二行\x00第三行",
	})
	if resp.Ticket.Title != "多行 标题 测试" {
		t.Fatalf("title=%q, want collapsed whitespace", resp.Ticket.Title)
	}
	if resp.Ticket.Description != "第一行\n第二行第三行" {
		t.Fatalf("description=%q, want CRLF normalised and NUL dropped", resp.Ticket.Description)
	}
	if resp.Ticket.State != StateOpen || !resp.Ticket.FirstResponseDueAt.Equal(testBase.Add(firstResponseSLA)) {
		t.Fatalf("new ticket state/due = %s/%s", resp.Ticket.State, resp.Ticket.FirstResponseDueAt)
	}
}

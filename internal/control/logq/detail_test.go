package logq

import (
	"net/http"
	"testing"
	"time"
)

// A6: one retry means two timeline rows, ordered by attempt_index ascending,
// each carrying its own latency and result.
func TestDetailReturnsAttemptTimelineInOrder(t *testing.T) {
	uid := newTestUser(t, "PERSONAL")
	svc := newTestService(t, newFakeClock(testBase))
	requestID := newRequestID()
	seedUsage(t, usageSeed{
		requestID: requestID, uid: uid, model: "gpt-4o", result: "SUCCESS",
		createdAt: testBase,
	})
	// Insert out of order on purpose: the response order must come from
	// attempt_index, not from the primary key.
	seedAttempt(t, uid, requestID, 2, "SUCCESS", 200, 12, 300, 900)
	seedAttempt(t, uid, requestID, 1, "FAILED", 502, 30, 0, 120)

	rec := callUserID(t, svc.Detail, http.MethodGet, "/api/console/logs/"+requestID, requestID, uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out detailResponse
	decodeBody(t, rec, &out)
	if len(out.Timeline) != 2 {
		t.Fatalf("timeline length=%d want 2: %+v", len(out.Timeline), out.Timeline)
	}
	first, second := out.Timeline[0], out.Timeline[1]
	if first.AttemptIndex != 1 || second.AttemptIndex != 2 {
		t.Fatalf("order = %d,%d want 1,2", first.AttemptIndex, second.AttemptIndex)
	}
	if first.Result != "FAILED" || first.HTTPStatus == nil || *first.HTTPStatus != 502 {
		t.Fatalf("attempt 1 = %+v, want FAILED/502", first)
	}
	if first.ConnectMs == nil || *first.ConnectMs != 30 || first.TotalMs != 120 {
		t.Fatalf("attempt 1 latency = %+v, want connect=30 total=120", first)
	}
	if second.Result != "SUCCESS" || second.FirstByteMs == nil || *second.FirstByteMs != 300 {
		t.Fatalf("attempt 2 = %+v, want SUCCESS/first_byte=300", second)
	}
	if out.Request.ChannelName == nil || *out.Request.ChannelName != "aliyun-primary" {
		t.Fatalf("snapshot channel name lost: %+v", out.Request)
	}
	for _, a := range out.Timeline {
		t.Logf("attempt %d result=%s http_status=%v connect_ms=%v first_byte_ms=%v total_ms=%d",
			a.AttemptIndex, a.Result, derefInt(a.HTTPStatus), derefInt(a.ConnectMs),
			derefInt(a.FirstByteMs), a.TotalMs)
	}
}

func derefInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// A8: another account's request_id and a request_id that never existed must
// produce the identical response body, not merely the same status.
func TestDetailOtherUserIsIdenticalToMissing(t *testing.T) {
	owner := newTestUser(t, "PERSONAL")
	attacker := newTestUser(t, "PERSONAL")
	svc := newTestService(t, newFakeClock(testBase))

	foreign := newRequestID()
	seedUsage(t, usageSeed{requestID: foreign, uid: owner, createdAt: testBase})

	foreignRec := callUserID(t, svc.Detail, http.MethodGet,
		"/api/console/logs/"+foreign, foreign, attacker)
	missingID := newRequestID()
	missingRec := callUserID(t, svc.Detail, http.MethodGet,
		"/api/console/logs/"+missingID, missingID, attacker)

	if foreignRec.Code != http.StatusNotFound || missingRec.Code != http.StatusNotFound {
		t.Fatalf("status foreign=%d missing=%d, want 404/404", foreignRec.Code, missingRec.Code)
	}
	if foreignRec.Body.String() != missingRec.Body.String() {
		t.Fatalf("bodies differ:\nforeign: %s\nmissing: %s", foreignRec.Body.String(), missingRec.Body.String())
	}
	if got := errorOf(t, foreignRec).Error.Code; got != "resource_not_found" {
		t.Fatalf("code=%q want resource_not_found", got)
	}
	t.Logf("foreign  %d %s", foreignRec.Code, foreignRec.Body.String())
	t.Logf("missing  %d %s", missingRec.Code, missingRec.Body.String())
}

// A7: a 366-day-old metadata row is still inside its retention period and is
// reachable by exact request_id; only the body has a 180-day limit.
func TestDetail366DayMetadataIsVisible(t *testing.T) {
	uid := newTestUser(t, "PERSONAL")
	svc := newTestService(t, newFakeClock(testBase))
	requestID := newRequestID()
	createdAt := testBase.Add(-366 * 24 * time.Hour)
	seedUsage(t, usageSeed{requestID: requestID, uid: uid, createdAt: createdAt})

	rec := callUserID(t, svc.Detail, http.MethodGet, "/api/console/logs/"+requestID, requestID, uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("366-day metadata status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out detailResponse
	decodeBody(t, rec, &out)
	if !out.Request.CreatedAt.Equal(createdAt) {
		t.Fatalf("created_at=%s want %s", out.Request.CreatedAt, createdAt)
	}
	if out.Request.RequestID != requestID {
		t.Fatalf("request_id=%q want %q", out.Request.RequestID, requestID)
	}
	t.Logf("HTTP %d request_id=%s created_at=%s model=%s",
		rec.Code, out.Request.RequestID, out.Request.CreatedAt.Format(time.RFC3339), out.Request.ModelName)
}

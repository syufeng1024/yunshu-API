package logq

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Before T22 creates t_request_body, a body read must answer 404 with an
// explanation — never 500, and never an empty string pretending success.
func TestBodyMissingTableIs404(t *testing.T) {
	uid := newTestUser(t, "PERSONAL")
	svc := newTestService(t, newFakeClock(testBase))
	requestID := newRequestID()
	seedUsage(t, usageSeed{
		requestID: requestID, uid: uid, fullLog: true, createdAt: testBase,
	})

	rec := callUserID(t, svc.Body, http.MethodGet,
		"/api/console/logs/"+requestID+"/body", requestID, uid)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
	body := errorOf(t, rec)
	if body.Error.Code != "resource_not_found" {
		t.Fatalf("code=%q want resource_not_found", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, "正文") {
		t.Fatalf("message must explain the missing body: %+v", body.Error)
	}
	t.Logf("t_request_body absent -> HTTP %d code=%s message=%q hint=%q",
		rec.Code, body.Error.Code, body.Error.Message, body.Error.Hint)
}

// Hard constraint 3: a body older than 180 days is 404 even when the usage
// row itself is readable. The store must not be consulted at all.
func TestBodyOlderThan180DaysIs404(t *testing.T) {
	uid := newTestUser(t, "PERSONAL")
	svc := newTestService(t, newFakeClock(testBase))
	requestID := newRequestID()
	seedUsage(t, usageSeed{
		requestID: requestID, uid: uid, fullLog: true,
		createdAt: testBase.Add(-181 * 24 * time.Hour),
	})
	store := &fakeBodyStore{body: "should-not-be-read"}
	svc.BodyStore = store

	rec := callUserID(t, svc.Body, http.MethodGet,
		"/api/console/logs/"+requestID+"/body", requestID, uid)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
	if store.calls != 0 {
		t.Fatalf("body store consulted %d times for an expired body", store.calls)
	}
}

// A body that was never stored (full_log_saved = 0) answers 404 without a
// lookup; the absence of the row is the expected state, not an error.
func TestBodyNotOptedInIs404(t *testing.T) {
	uid := newTestUser(t, "PERSONAL")
	svc := newTestService(t, newFakeClock(testBase))
	requestID := newRequestID()
	seedUsage(t, usageSeed{
		requestID: requestID, uid: uid, fullLog: false, createdAt: testBase,
	})
	store := &fakeBodyStore{body: "should-not-be-read"}
	svc.BodyStore = store

	rec := callUserID(t, svc.Body, http.MethodGet,
		"/api/console/logs/"+requestID+"/body", requestID, uid)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
	if store.calls != 0 {
		t.Fatalf("body store consulted %d times with full_log_saved=0", store.calls)
	}
}

// Hard constraint 10: the body is returned with phone, e-mail and sk- keys
// masked. Masking never rejects the body.
func TestBodyRedactsSensitiveValues(t *testing.T) {
	uid := newTestUser(t, "PERSONAL")
	svc := newTestService(t, newFakeClock(testBase))
	requestID := newRequestID()
	seedUsage(t, usageSeed{
		requestID: requestID, uid: uid, fullLog: true, createdAt: testBase,
	})
	const raw = `{"phone":"13800138000","email":"zhangsan@example.com","key":"sk-abcdefghijklmnop"}` + "\n" +
		`{"note":"call 13900139000 or mail lisi@corp.cn"}`
	store := &fakeBodyStore{body: raw}
	svc.BodyStore = store

	rec := callUserID(t, svc.Body, http.MethodGet,
		"/api/console/logs/"+requestID+"/body", requestID, uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var out bodyResponse
	decodeBody(t, rec, &out)
	if !out.Redacted {
		t.Fatal("redacted flag is false for a body containing sensitive values")
	}
	for _, secret := range []string{"13800138000", "13900139000", "zhangsan@example.com", "lisi@corp.cn", "sk-abcdefghijklmnop"} {
		if strings.Contains(out.Body, secret) {
			t.Fatalf("body still contains %q: %s", secret, out.Body)
		}
	}
	for _, want := range []string{"138****8000", "139****9000", "z***@example.com", "l***@corp.cn", "sk-****mnop"} {
		if !strings.Contains(out.Body, want) {
			t.Fatalf("body missing mask %q: %s", want, out.Body)
		}
	}
	// The non-sensitive note survives; masking edits, it does not truncate.
	if !strings.Contains(out.Body, "call") || !strings.Contains(out.Body, "or mail") {
		t.Fatalf("non-sensitive text was lost: %s", out.Body)
	}
	if store.lastUID != uid || store.lastReqID != requestID {
		t.Fatalf("store scope = (%d,%s), want (%d,%s)", store.lastUID, store.lastReqID, uid, requestID)
	}
	t.Logf("redacted=%v body=%s", out.Redacted, out.Body)
}

// A8 for the body route: another account's request_id and an absent one are
// byte-identical, and neither reveals that a body exists.
func TestBodyOtherUserIsIdenticalToMissing(t *testing.T) {
	owner := newTestUser(t, "PERSONAL")
	attacker := newTestUser(t, "PERSONAL")
	svc := newTestService(t, newFakeClock(testBase))

	foreign := newRequestID()
	seedUsage(t, usageSeed{requestID: foreign, uid: owner, fullLog: true, createdAt: testBase})

	foreignRec := callUserID(t, svc.Body, http.MethodGet,
		"/api/console/logs/"+foreign+"/body", foreign, attacker)
	missing := newRequestID()
	missingRec := callUserID(t, svc.Body, http.MethodGet,
		"/api/console/logs/"+missing+"/body", missing, attacker)

	if foreignRec.Code != http.StatusNotFound || missingRec.Code != http.StatusNotFound {
		t.Fatalf("status foreign=%d missing=%d, want 404/404", foreignRec.Code, missingRec.Code)
	}
	if foreignRec.Body.String() != missingRec.Body.String() {
		t.Fatalf("bodies differ:\nforeign: %s\nmissing: %s", foreignRec.Body.String(), missingRec.Body.String())
	}
	t.Logf("foreign  %d %s", foreignRec.Code, foreignRec.Body.String())
	t.Logf("missing  %d %s", missingRec.Code, missingRec.Body.String())
}

// The 180-day boundary is inclusive of the last retained instant: exactly 180
// days old is still readable, 180 days plus a millisecond is not.
func TestBody180DayBoundary(t *testing.T) {
	uid := newTestUser(t, "PERSONAL")
	svc := newTestService(t, newFakeClock(testBase))
	atEdge := newRequestID()
	seedUsage(t, usageSeed{
		requestID: atEdge, uid: uid, fullLog: true,
		createdAt: testBase.Add(-bodyWindow),
	})
	svc.BodyStore = &fakeBodyStore{body: "edge body"}

	rec := callUserID(t, svc.Body, http.MethodGet,
		"/api/console/logs/"+atEdge+"/body", atEdge, uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("exactly 180 days status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}

	past := newRequestID()
	seedUsage(t, usageSeed{
		requestID: past, uid: uid, fullLog: true,
		createdAt: testBase.Add(-bodyWindow - time.Millisecond),
	})
	rec = callUserID(t, svc.Body, http.MethodGet,
		"/api/console/logs/"+past+"/body", past, uid)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("180 days + 1ms status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

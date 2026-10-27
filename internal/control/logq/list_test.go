package logq

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A5: state and model are two predicates in the same WHERE clause. Giving
// both must narrow the result to their intersection, not widen it to a union.
func TestListStateAndModelFiltersIntersect(t *testing.T) {
	uid := newTestUser(t, "PERSONAL")
	// The clock sits a few seconds after testBase so every seeded row is in
	// the past, as a real row would be when the list is queried.
	clk := newFakeClock(testBase.Add(10 * time.Second))
	svc := newTestService(t, clk)

	rSuccessA := newRequestID()
	rFailedA := newRequestID()
	rFailedB := newRequestID()
	rSuccessB := newRequestID()
	seedUsage(t, usageSeed{requestID: rSuccessA, uid: uid, model: "gpt-4o", result: "SUCCESS", createdAt: testBase})
	seedUsage(t, usageSeed{requestID: rFailedA, uid: uid, model: "gpt-4o", result: "FAILED", httpCode: 502, createdAt: testBase.Add(time.Second)})
	seedUsage(t, usageSeed{requestID: rFailedB, uid: uid, model: "claude-sonnet", result: "FAILED", httpCode: 503, createdAt: testBase.Add(2 * time.Second)})
	seedUsage(t, usageSeed{requestID: rSuccessB, uid: uid, model: "claude-sonnet", result: "SUCCESS", createdAt: testBase.Add(3 * time.Second)})

	cases := []struct {
		name  string
		query string
		want  int64
	}{
		{"intersection", "?state=failed&model=gpt-4o", 1},
		{"failed only", "?state=failed", 2},
		{"model only", "?model=gpt-4o", 2},
		{"union would be three", "?state=success&model=gpt-4o", 1},
		{"unfiltered keeps failures", "", 4},
	}
	for _, tc := range cases {
		rec := callUser(t, svc.List, http.MethodGet, "/api/console/logs"+tc.query, uid)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		var out listResponse
		decodeBody(t, rec, &out)
		if out.Total != tc.want {
			t.Fatalf("%s total=%d want %d", tc.name, out.Total, tc.want)
		}
		if tc.name == "intersection" {
			if len(out.Items) != 1 || out.Items[0].RequestID != rFailedA {
				t.Fatalf("intersection returned %+v, want only %s", out.Items, rFailedA)
			}
			if out.Items[0].Result != "FAILED" || out.Items[0].FailStage == nil {
				t.Fatalf("failure row lost its status: %+v", out.Items[0])
			}
			t.Logf("state=failed&model=gpt-4o -> total=%d request=%s result=%s fail_stage=%s http_status=%d",
				out.Total, out.Items[0].RequestID, out.Items[0].Result,
				*out.Items[0].FailStage, out.Items[0].HTTPStatus)
		} else {
			t.Logf("%s -> total=%d", tc.query, out.Total)
		}
	}
}

// The default range is seven days; an older row needs an explicit from.
func TestListDefaultWindowIsSevenDays(t *testing.T) {
	uid := newTestUser(t, "PERSONAL")
	svc := newTestService(t, newFakeClock(testBase))

	recent := newRequestID()
	old := newRequestID()
	seedUsage(t, usageSeed{requestID: recent, uid: uid, createdAt: testBase.Add(-time.Hour)})
	seedUsage(t, usageSeed{requestID: old, uid: uid, createdAt: testBase.Add(-8 * 24 * time.Hour)})

	rec := callUser(t, svc.List, http.MethodGet, "/api/console/logs", uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out listResponse
	decodeBody(t, rec, &out)
	if out.Total != 1 || len(out.Items) != 1 || out.Items[0].RequestID != recent {
		t.Fatalf("default window returned %+v, want only %s", out.Items, recent)
	}
}

// A9: a from older than 365 days is a query error with the archive hint. The
// exact boundary is allowed, 366 days is not.
func TestListRejectsFromOlderThanOnlineWindow(t *testing.T) {
	uid := newTestUser(t, "PERSONAL")
	svc := newTestService(t, newFakeClock(testBase))

	exact := testBase.Add(-365 * 24 * time.Hour).Format(time.RFC3339)
	rec := callUser(t, svc.List, http.MethodGet, "/api/console/logs?from="+exact, uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("exactly 365 days must be allowed: %d %s", rec.Code, rec.Body.String())
	}

	tooOld := testBase.Add(-366 * 24 * time.Hour).Format(time.RFC3339)
	rec = callUser(t, svc.List, http.MethodGet, "/api/console/logs?from="+tooOld, uid)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("366 days status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	body := errorOf(t, rec)
	if body.Error.Code != "invalid_query_parameter" {
		t.Fatalf("code=%q want invalid_query_parameter", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, "365") || !strings.Contains(body.Error.Hint, "归档") {
		t.Fatalf("message/hint must name the window and archive export: %+v", body.Error)
	}
	t.Logf("from=366d -> HTTP %d code=%s message=%q hint=%q",
		rec.Code, body.Error.Code, body.Error.Message, body.Error.Hint)
}

// A date-only upper bound covers the whole day. Without this, a console user
// picking "today" would see only the 00:00:00 instant.
func TestListDateOnlyUpperBoundCoversTheDay(t *testing.T) {
	uid := newTestUser(t, "PERSONAL")
	svc := newTestService(t, newFakeClock(testBase))
	requestID := newRequestID()
	seedUsage(t, usageSeed{requestID: requestID, uid: uid, createdAt: testBase})

	rec := callUser(t, svc.List, http.MethodGet,
		"/api/console/logs?from=2026-10-27&to=2026-10-27", uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out listResponse
	decodeBody(t, rec, &out)
	if out.Total != 1 {
		t.Fatalf("date-only day total=%d want 1", out.Total)
	}
	if !out.To.Equal(time.Date(2026, 10, 27, 23, 59, 59, 999_000_000, time.UTC)) {
		t.Fatalf("to=%s, want end of day", out.To)
	}
}

func TestListRejectsBadQueryParameters(t *testing.T) {
	uid := newTestUser(t, "PERSONAL")
	svc := newTestService(t, newFakeClock(testBase))

	cases := []string{
		"/api/console/logs?page_size=101",
		"/api/console/logs?state=bogus",
		"/api/console/logs?from=not-a-time",
		"/api/console/logs?from=2026-10-27T00:00:00Z&to=2026-10-26T00:00:00Z",
	}
	for _, target := range cases {
		rec := callUser(t, svc.List, http.MethodGet, target, uid)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d want 400 body=%s", target, rec.Code, rec.Body.String())
		}
		if got := errorOf(t, rec).Error.Code; got != "invalid_query_parameter" {
			t.Fatalf("%s code=%q want invalid_query_parameter", target, got)
		}
	}
}

// LogDB wins over DB. The main pool here points at a closed port; the query
// still succeeds, proving the reads are not silently served by DB.
func TestListUsesLogDBWhenConfigured(t *testing.T) {
	uid := newTestUser(t, "PERSONAL")
	requestID := newRequestID()
	seedUsage(t, usageSeed{requestID: requestID, uid: uid, createdAt: testBase})

	broken, err := sql.Open("mysql", "root:wrong@tcp(127.0.0.1:1)/missing?parseTime=true")
	if err != nil {
		t.Fatalf("open broken pool: %v", err)
	}
	defer broken.Close()

	svc := New(broken, testDB, newFakeClock(testBase), nil)
	rec := callUser(t, svc.List, http.MethodGet, "/api/console/logs", uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("log DB routing failed: %d %s", rec.Code, rec.Body.String())
	}
	var out listResponse
	decodeBody(t, rec, &out)
	if out.Total != 1 || out.Items[0].RequestID != requestID {
		t.Fatalf("returned %+v, want %s", out.Items, requestID)
	}
}

package usage

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func windowValues(from, to time.Time, extra map[string]string) string {
	v := url.Values{}
	v.Set("from", from.Format(time.RFC3339))
	v.Set("to", to.Format(time.RFC3339))
	for k, val := range extra {
		v.Set(k, val)
	}
	return v.Encode()
}

// A12: a failed request is visible in the request count and in the success
// rate, but it bills nothing. The same account has one SUCCESS (420,000 micro,
// 120/80 tokens) and one FAILED row whose token fields are deliberately large;
// usage must report 2 requests / 1 success / 5000 bp, while tokens and money
// come from the SUCCESS row alone. The failed row stays visible in logq's list
// too — that query has its own, unfiltered default.
func TestUsageCountsFailuresInRateButBillsOnlySuccess(t *testing.T) {
	uid := newTestUser(t)
	svc := newTestService(t, newFakeClock(testBase))
	seedUsage(t, usageSeed{
		uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		result: "SUCCESS", tokensIn: 120, tokensOut: 80,
		consumed: 420_000, recharge: 300_000, bonus: 120_000,
		createdAt: testBase.Add(-time.Hour),
	})
	seedUsage(t, usageSeed{
		uid: uid, modelID: 3, result: "FAILED", httpCode: http.StatusBadGateway,
		tokensIn: 999, tokensOut: 999, consumed: 0,
		createdAt: testBase.Add(-2 * time.Hour),
	})

	rec := callHandler(t, svc.Query,
		"/api/console/usage?"+windowValues(testBase.Add(-24*time.Hour), testBase, nil), uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out usageResponse
	decodeBody(t, rec, &out)
	if len(out.Items) != 1 {
		t.Fatalf("items=%d want 1 (failed row must not appear): %+v", len(out.Items), out.Items)
	}
	item := out.Items[0]
	if item.RequestTotal != 2 || item.RequestSuccess != 1 {
		t.Fatalf("request_total=%d success=%d want 2/1 (FAILED counts in the denominator)",
			item.RequestTotal, item.RequestSuccess)
	}
	if item.SuccessRateBP != 5_000 {
		t.Fatalf("success_rate_bp=%d want 5000", item.SuccessRateBP)
	}
	if item.TokensIn != 120 || item.TokensOut != 80 {
		t.Fatalf("tokens=%d/%d want 120/80; failed row leaked into billing", item.TokensIn, item.TokensOut)
	}
	if item.ConsumedMicro != 420_000 || item.AmountFen != 42 || item.AmountYuan != "0.42" {
		t.Fatalf("money = %d micro / %d fen / %q, want 420000/42/0.42",
			item.ConsumedMicro, item.AmountFen, item.AmountYuan)
	}
	if item.ConsumedRechargeMicro+item.ConsumedBonusMicro != item.ConsumedMicro {
		t.Fatalf("split %d+%d != %d", item.ConsumedRechargeMicro,
			item.ConsumedBonusMicro, item.ConsumedMicro)
	}
	// The same account still has two log rows: the log list's source query is
	// unfiltered, so the FAILED row above is visible there. Usage sees both
	// rows in request_total but bills only one. This is the A12 pair on one
	// dataset.
	var logRows int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM t_usage_record WHERE user_id = ?`, uid).Scan(&logRows); err != nil {
		t.Fatalf("count log rows: %v", err)
	}
	if logRows != 2 {
		t.Fatalf("log rows=%d want 2 (SUCCESS+FAILED)", logRows)
	}
	t.Logf("A12 same account: log rows=%d (SUCCESS+FAILED), usage total=%d success=%d rate_bp=%d amount_yuan=%s",
		logRows, item.RequestTotal, item.RequestSuccess, item.SuccessRateBP, item.AmountYuan)
}

// The rate must be a real number, not a constant 10000. Two SUCCESS rows and
// one FAILED row on the same model give 3 total / 2 success / 6666 bp
// (integer truncation: 20000/3 = 6666). The FAILED row carries non-zero token
// and money columns on purpose — a defensive writer could leave a partial
// settlement there, and it must not reach any billing column.
func TestUsageSuccessRateUsesAllRequests(t *testing.T) {
	uid := newTestUser(t)
	svc := newTestService(t, newFakeClock(testBase))
	base := testBase.Add(-time.Hour)
	seedUsage(t, usageSeed{uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		tokensIn: 100, tokensOut: 60, consumed: 100_000, recharge: 100_000, createdAt: base})
	seedUsage(t, usageSeed{uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		tokensIn: 200, tokensOut: 40, consumed: 200_000, recharge: 200_000, createdAt: base.Add(time.Minute)})
	seedUsage(t, usageSeed{uid: uid, modelID: 3, result: "FAILED", httpCode: http.StatusBadGateway,
		tokensIn: 999, tokensOut: 999, consumed: 700_000, recharge: 700_000,
		createdAt: base.Add(2 * time.Minute)})

	rec := callHandler(t, svc.Query,
		"/api/console/usage?"+windowValues(testBase.Add(-24*time.Hour), testBase, nil), uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out usageResponse
	decodeBody(t, rec, &out)
	if len(out.Items) != 1 {
		t.Fatalf("items=%d want 1 model group: %+v", len(out.Items), out.Items)
	}
	item := out.Items[0]
	if item.RequestTotal != 3 || item.RequestSuccess != 2 {
		t.Fatalf("request_total=%d request_success=%d want 3/2",
			item.RequestTotal, item.RequestSuccess)
	}
	if item.SuccessRateBP != 6_666 {
		t.Fatalf("success_rate_bp=%d want 6666 (20000/3 truncated)", item.SuccessRateBP)
	}
	if item.TokensIn != 300 {
		t.Fatalf("tokens_in=%d want 300 (FAILED 999 must not be summed)", item.TokensIn)
	}
	if item.TokensOut != 100 {
		t.Fatalf("tokens_out=%d want 100 (FAILED 999 must not be summed)", item.TokensOut)
	}
	if item.ConsumedMicro != 300_000 {
		t.Fatalf("consumed_micro=%d want 300000 (FAILED 700000 must not be summed)",
			item.ConsumedMicro)
	}
	if item.ConsumedRechargeMicro != 300_000 || item.ConsumedBonusMicro != 0 {
		t.Fatalf("split=%d/%d want 300000/0",
			item.ConsumedRechargeMicro, item.ConsumedBonusMicro)
	}
	if out.Totals.RequestTotal != 3 || out.Totals.RequestSuccess != 2 ||
		out.Totals.SuccessRateBP != 6_666 || out.Totals.ConsumedMicro != 300_000 {
		t.Fatalf("totals=%+v want 3/2/6666/300000", out.Totals)
	}
	t.Logf("3 rows: total=%d success=%d rate_bp=%d consumed_micro=%d tokens_in=%d tokens_out=%d",
		item.RequestTotal, item.RequestSuccess, item.SuccessRateBP,
		item.ConsumedMicro, item.TokensIn, item.TokensOut)
}

// A2 / F9-1: switching dimension keeps every other filter and re-groups on
// the server. Model 9's large channel-7 spend must disappear when model_id=3
// is set, proving the WHERE clause did not change; the rows split by channel
// prove the GROUP BY did.
func TestUsageDimensionSwitchKeepsFilters(t *testing.T) {
	uid := newTestUser(t)
	svc := newTestService(t, newFakeClock(testBase))
	base := testBase.Add(-3 * time.Hour)
	seedUsage(t, usageSeed{uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		consumed: 100_000, recharge: 100_000, createdAt: base})
	seedUsage(t, usageSeed{uid: uid, modelID: 3, channelID: 8, channelName: "volc-secondary",
		consumed: 200_000, recharge: 200_000, createdAt: base.Add(time.Minute)})
	seedUsage(t, usageSeed{uid: uid, modelID: 9, channelID: 7, channelName: "aliyun-primary",
		consumed: 900_000, recharge: 900_000, createdAt: base.Add(2 * time.Minute)})

	window := windowValues(testBase.Add(-24*time.Hour), testBase, nil)

	byModel := callHandler(t, svc.Query,
		"/api/console/usage?"+window+"&dimension=model&model_id=3", uid)
	if byModel.Code != http.StatusOK {
		t.Fatalf("model status=%d body=%s", byModel.Code, byModel.Body.String())
	}
	var modelOut usageResponse
	decodeBody(t, byModel, &modelOut)
	if len(modelOut.Items) != 1 || modelOut.Items[0].ID != 3 ||
		modelOut.Items[0].ConsumedMicro != 300_000 {
		t.Fatalf("model dimension = %+v, want one model 3 row with 300000 micro",
			modelOut.Items)
	}

	byChannel := callHandler(t, svc.Query,
		"/api/console/usage?"+window+"&dimension=channel&model_id=3", uid)
	if byChannel.Code != http.StatusOK {
		t.Fatalf("channel status=%d body=%s", byChannel.Code, byChannel.Body.String())
	}
	var channelOut usageResponse
	decodeBody(t, byChannel, &channelOut)
	if channelOut.Dimension != "channel" {
		t.Fatalf("dimension=%q want channel", channelOut.Dimension)
	}
	if channelOut.Filter.ModelID == nil || *channelOut.Filter.ModelID != 3 {
		t.Fatalf("model_id filter dropped on dimension switch: %+v", channelOut.Filter)
	}
	if len(channelOut.Items) != 2 {
		t.Fatalf("channel items=%d want 2: %+v", len(channelOut.Items), channelOut.Items)
	}
	if channelOut.Items[0].ID != 8 || channelOut.Items[0].ConsumedMicro != 200_000 {
		t.Fatalf("first channel row = %+v, want id 8 / 200000 (consumed desc)", channelOut.Items[0])
	}
	if channelOut.Items[1].ID != 7 || channelOut.Items[1].ConsumedMicro != 100_000 {
		t.Fatalf("second channel row = %+v, want id 7 / 100000", channelOut.Items[1])
	}
	for _, item := range channelOut.Items {
		if item.ConsumedMicro >= 900_000 {
			t.Fatalf("model 9 leaked into model_id=3 filter: %+v", item)
		}
	}
	modelFilter := "<nil>"
	if channelOut.Filter.ModelID != nil {
		modelFilter = itoa(*channelOut.Filter.ModelID)
	}
	t.Logf("A2 model: %+v", modelOut.Items)
	t.Logf("A2 channel(dimension=%s, filter model_id=%s): items=%+v",
		channelOut.Dimension, modelFilter, channelOut.Items)
}

// A9: a from older than the 365-day online window is refused with a hint to
// use archive export, not answered with a short page.
func TestUsageRejectsWindowBeyond365Days(t *testing.T) {
	uid := newTestUser(t)
	svc := newTestService(t, newFakeClock(testBase))
	from := testBase.Add(-366 * 24 * time.Hour)
	rec := callHandler(t, svc.Query,
		"/api/console/usage?"+windowValues(from, testBase, nil), uid)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s want 400", rec.Code, rec.Body.String())
	}
	body := errorOf(t, rec)
	if body.Error.Code != "invalid_query_parameter" {
		t.Fatalf("code=%q want invalid_query_parameter", body.Error.Code)
	}
	if !strings.Contains(body.Error.Hint, "归档") {
		t.Fatalf("hint=%q must point at archive export", body.Error.Hint)
	}
	t.Logf("A9 %d %s", rec.Code, rec.Body.String())
}

// A4 (query half): every column the CSV writes names a JSON member the page
// already receives. The response's "columns" member is the same header slice
// in the same order, so the front end cannot drift from the file.
func TestUsageColumnsMatchJSONMembers(t *testing.T) {
	uid := newTestUser(t)
	svc := newTestService(t, newFakeClock(testBase))
	seedUsage(t, usageSeed{uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		consumed: 100_000, recharge: 100_000, createdAt: testBase.Add(-time.Hour)})

	rec := callHandler(t, svc.Query,
		"/api/console/usage?"+windowValues(testBase.Add(-24*time.Hour), testBase, nil), uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	decodeBody(t, rec, &raw)

	columns, ok := raw["columns"].([]any)
	if !ok || len(columns) != len(usageColumns) {
		t.Fatalf("columns=%v want %v", raw["columns"], usageColumnHeaders())
	}
	for i, want := range usageColumnHeaders() {
		if got := columns[i].(string); got != want {
			t.Fatalf("columns[%d]=%q want %q", i, got, want)
		}
	}
	items := raw["items"].([]any)
	if len(items) == 0 {
		t.Fatal("no items to check")
	}
	item := items[0].(map[string]any)
	for _, col := range usageColumns {
		if _, ok := item[col.JSON]; !ok {
			t.Errorf("column %q references JSON member %q missing from item", col.Header, col.JSON)
		}
	}
	t.Logf("A4 headers=%v", usageColumnHeaders())
}

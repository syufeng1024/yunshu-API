package usage

import (
	"bytes"
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func startExport(t *testing.T, svc *Service, uid int64, query string) exportStartResponse {
	t.Helper()
	rec := call(t, svc.StartExport, http.MethodPost,
		"/api/console/usage/export?"+query, nil, uid)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("start export status=%d body=%s want 202", rec.Code, rec.Body.String())
	}
	var out exportStartResponse
	decodeBody(t, rec, &out)
	if out.TaskID == "" || out.State != exportStateRunning {
		t.Fatalf("start export = %+v, want task id and RUNNING", out)
	}
	return out
}

func exportStatus(t *testing.T, svc *Service, taskID string, uid int64) exportStatusResponse {
	t.Helper()
	rec := call(t, svc.ExportToken, http.MethodGet,
		"/api/console/exports/"+taskID, map[string]string{"token": taskID}, uid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status poll=%d body=%s", rec.Code, rec.Body.String())
	}
	var out exportStatusResponse
	decodeBody(t, rec, &out)
	return out
}

func waitExportDone(t *testing.T, svc *Service, taskID string, uid int64) exportStatusResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out := exportStatus(t, svc, taskID, uid)
		if out.State != exportStateRunning {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("export %s stayed RUNNING past the deadline", taskID)
	return exportStatusResponse{}
}

func downloadExport(t *testing.T, svc *Service, url string, uid int64) *http.Response {
	t.Helper()
	token := strings.TrimPrefix(url, "/api/console/exports/")
	rec := call(t, svc.ExportToken, http.MethodGet,
		url, map[string]string{"token": token}, uid)
	return rec.Result()
}

// A4 + hard constraint 5: the CSV contains the filtered rows only, and its
// header is usageColumns in order — the same slice the page's "columns"
// member is built from. Three matching rows exist (two SUCCESS, one FAILED)
// plus one SUCCESS row of another model; the file has exactly three data rows,
// one of which is the FAILED-only day.
func TestExportAsyncFilteredCSVMatchesColumns(t *testing.T) {
	uid := newTestUser(t)
	svc := newTestService(t, newFakeClock(testBase))
	// Model 3 has three SUCCESS rows (two share a CST day) plus one FAILED
	// row on an earlier day; model 9 is outside the filter. Exporting model 3
	// by day must yield exactly three data rows: the third SUCCESS folds into
	// its day, the FAILED row forms its own group with request_total=1 and no
	// billed fields, and model 9 never appears.
	day1 := testBase.Add(-49 * time.Hour)
	day2 := testBase.Add(-25 * time.Hour)
	seedUsage(t, usageSeed{uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		consumed: 100_000, recharge: 100_000, createdAt: day1})
	seedUsage(t, usageSeed{uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		consumed: 200_000, recharge: 200_000, createdAt: day2})
	seedUsage(t, usageSeed{uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		consumed: 300_000, recharge: 300_000, createdAt: day2.Add(time.Minute)})
	seedUsage(t, usageSeed{uid: uid, modelID: 3, result: "FAILED", httpCode: http.StatusBadGateway,
		consumed: 0, createdAt: testBase.Add(-73 * time.Hour)})
	seedUsage(t, usageSeed{uid: uid, modelID: 9, channelID: 7, channelName: "aliyun-primary",
		consumed: 900_000, recharge: 900_000, createdAt: day2.Add(2 * time.Minute)})

	query := windowValues(testBase.Add(-96*time.Hour), testBase,
		map[string]string{"model_id": "3", "dimension": "day"})
	started := startExport(t, svc, uid, query)

	// A11: one running export per account. The worker may already be done,
	// but the K27 lock lives for the full hour, so the second POST is a 409.
	second := call(t, svc.StartExport, http.MethodPost,
		"/api/console/usage/export?"+query, nil, uid)
	if second.Code != http.StatusConflict {
		t.Fatalf("second export status=%d body=%s want 409",
			second.Code, second.Body.String())
	}
	if got := errorOf(t, second).Error.Code; got != "resource_state_conflict" {
		t.Fatalf("second export code=%q want resource_state_conflict", got)
	}

	status := waitExportDone(t, svc, started.TaskID, uid)
	if status.State != exportStateDone {
		t.Fatalf("state=%q want DONE", status.State)
	}
	if status.URL == "" || status.ExpiresAt == nil {
		t.Fatalf("DONE status missing url/expires_at: %+v", status)
	}
	wantExp := testBase.Add(time.Hour)
	if !status.ExpiresAt.Equal(wantExp) {
		t.Fatalf("expires_at=%s want %s", status.ExpiresAt, wantExp)
	}

	resp := downloadExport(t, svc, status.URL, uid)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type=%q want text/csv", ct)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read csv: %v", err)
	}
	records, err := csv.NewReader(bytes.NewReader(buf.Bytes())).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v\n%s", err, buf.String())
	}
	if len(records) < 2 {
		t.Fatalf("csv has %d lines, want header + data\n%s", len(records), buf.String())
	}

	wantHeader := usageColumnHeaders()
	if len(records[0]) != len(wantHeader) {
		t.Fatalf("header width=%d want %d", len(records[0]), len(wantHeader))
	}
	for i, want := range wantHeader {
		if records[0][i] != want {
			t.Fatalf("header[%d]=%q want %q", i, records[0][i], want)
		}
	}
	col := map[string]int{}
	for i, h := range records[0] {
		col[h] = i
	}

	rows := records[1:]
	if len(rows) != 3 {
		t.Fatalf("csv data rows=%d want 3 (two SUCCESS days + one FAILED-only day)\n%s",
			len(rows), buf.String())
	}
	var sum int64
	days := map[string]bool{}
	failedDays := 0
	for _, row := range rows {
		if row[col["维度"]] != "day" {
			t.Fatalf("row dimension=%q want day: %v", row[col["维度"]], row)
		}
		days[row[col["名称"]]] = true
		total, err := strconv.ParseInt(row[col["请求数"]], 10, 64)
		if err != nil {
			t.Fatalf("parse request_total: %v", err)
		}
		success, err := strconv.ParseInt(row[col["成功数"]], 10, 64)
		if err != nil {
			t.Fatalf("parse request_success: %v", err)
		}
		if success > total {
			t.Fatalf("day %s: success=%d > total=%d", row[col["名称"]], success, total)
		}
		v, err := strconv.ParseInt(row[col["消耗微积分"]], 10, 64)
		if err != nil {
			t.Fatalf("parse consumed: %v", err)
		}
		if total == 1 && success == 0 {
			failedDays++
			if v != 0 {
				t.Fatalf("FAILED-only day %s consumed=%d want 0", row[col["名称"]], v)
			}
			if rate := row[col["成功率(基点)"]]; rate != "0" {
				t.Fatalf("FAILED-only day %s rate=%q want 0", row[col["名称"]], rate)
			}
		}
		sum += v
	}
	if len(days) != 3 {
		t.Fatalf("distinct days=%v want 3", days)
	}
	if failedDays != 1 {
		t.Fatalf("FAILED-only rows=%d want 1: %v", failedDays, rows)
	}
	if sum != 600_000 {
		t.Fatalf("consumed sum=%d want 600000 (model 9 and FAILED money excluded)", sum)
	}
	t.Logf("A4 header=%v", records[0])
	t.Logf("A4 rows=%d sum_consumed_micro=%d", len(rows), sum)
	t.Logf("A11 second POST → %d %s", second.Code, second.Body.String())
}

// Hard constraint 5, literal shape: three matching rows on two CST days
// export as two data rows, never as the whole table.
func TestExportContainsOnlyFilteredRows(t *testing.T) {
	uid := newTestUser(t)
	svc := newTestService(t, newFakeClock(testBase))
	day1 := testBase.Add(-49 * time.Hour)
	day2 := testBase.Add(-25 * time.Hour)
	seedUsage(t, usageSeed{uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		consumed: 100_000, recharge: 100_000, createdAt: day1})
	seedUsage(t, usageSeed{uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		consumed: 200_000, recharge: 200_000, createdAt: day1.Add(time.Minute)})
	seedUsage(t, usageSeed{uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		consumed: 300_000, recharge: 300_000, createdAt: day2})

	started := startExport(t, svc, uid, windowValues(
		testBase.Add(-96*time.Hour), testBase,
		map[string]string{"model_id": "3", "dimension": "day"}))
	status := waitExportDone(t, svc, started.TaskID, uid)
	if status.State != exportStateDone {
		t.Fatalf("state=%q want DONE", status.State)
	}
	resp := downloadExport(t, svc, status.URL, uid)
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read csv: %v", err)
	}
	records, err := csv.NewReader(bytes.NewReader(buf.Bytes())).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records)-1 != 2 {
		t.Fatalf("csv data rows=%d want 2 from 3 filtered rows\n%s",
			len(records)-1, buf.String())
	}
	t.Logf("A5 3 filtered rows → %d csv data rows\n%s", len(records)-1, buf.String())
}

// A11 (concurrency half, missing-key half): without EXPORT_HMAC_KEY the start
// endpoint refuses with 503 instead of ever producing an unsigned link.
func TestExportRefusesWithoutHMACKey(t *testing.T) {
	uid := newTestUser(t)
	svc := newTestService(t, newFakeClock(testBase))
	svc.HMACKey = nil

	rec := call(t, svc.StartExport, http.MethodPost,
		"/api/console/usage/export?"+windowValues(testBase.Add(-24*time.Hour), testBase, nil),
		nil, uid)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s want 503", rec.Code, rec.Body.String())
	}
	body := errorOf(t, rec)
	if body.Error.Code != "config_unavailable" {
		t.Fatalf("code=%q want config_unavailable", body.Error.Code)
	}
	if !strings.Contains(body.Error.Message, "导出密钥") {
		t.Fatalf("message=%q must name the missing export key", body.Error.Message)
	}
	t.Logf("A11 no key: %d %s", rec.Code, rec.Body.String())
}

// A11 (expiry half): the task and download tokens die after one hour even
// though the file is still on disk.
func TestExportTokensExpireAfterOneHour(t *testing.T) {
	uid := newTestUser(t)
	clk := newFakeClock(testBase)
	svc := newTestService(t, clk)
	seedUsage(t, usageSeed{uid: uid, modelID: 3, channelID: 7, channelName: "aliyun-primary",
		consumed: 100_000, recharge: 100_000, createdAt: testBase.Add(-time.Hour)})

	started := startExport(t, svc, uid,
		windowValues(testBase.Add(-24*time.Hour), testBase, nil))
	status := waitExportDone(t, svc, started.TaskID, uid)
	if status.State != exportStateDone {
		t.Fatalf("state=%q want DONE", status.State)
	}

	clk.set(testBase.Add(time.Hour + time.Second))

	expiredTask := call(t, svc.ExportToken, http.MethodGet,
		"/api/console/exports/"+started.TaskID,
		map[string]string{"token": started.TaskID}, uid)
	if expiredTask.Code != http.StatusNotFound {
		t.Fatalf("expired task status=%d body=%s want 404",
			expiredTask.Code, expiredTask.Body.String())
	}
	if got := errorOf(t, expiredTask).Error.Code; got != "resource_not_found" {
		t.Fatalf("expired task code=%q want resource_not_found", got)
	}

	expiredDownload := call(t, svc.ExportToken, http.MethodGet,
		status.URL, map[string]string{"token": strings.TrimPrefix(status.URL, "/api/console/exports/")}, uid)
	if expiredDownload.Code != http.StatusNotFound {
		t.Fatalf("expired download status=%d body=%s want 404",
			expiredDownload.Code, expiredDownload.Body.String())
	}
	t.Logf("A11 after +1h1s: task=%d download=%d", expiredTask.Code, expiredDownload.Code)
	t.Logf("A11 task body: %s", expiredTask.Body.String())
}

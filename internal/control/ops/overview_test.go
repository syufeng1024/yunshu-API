package ops

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"yunshu.dev/core/internal/control/ctltest"
	"yunshu.dev/core/internal/platform/clock"
)

var overviewNow = time.Date(2035, 1, 2, 12, 0, 0, 0, time.UTC)

// newOverviewDB creates a clean migrated schema. The shared yunshu_test DB is
// used by parallel package binaries (ticket/review/etc.), so global aggregate
// assertions need an isolated schema to stay deterministic.
func newOverviewDB(t *testing.T) *sql.DB {
	t.Helper()
	base := os.Getenv("TEST_DB_DSN")
	if base == "" {
		base = ctltest.DefaultDSN
	}
	cfg, err := mysql.ParseDSN(base)
	if err != nil {
		t.Fatalf("parse TEST_DB_DSN: %v", err)
	}
	name := fmt.Sprintf("yunshu_t24_ov_%d", time.Now().UnixNano())
	cfg.DBName = name
	t.Setenv("TEST_DB_DSN", cfg.FormatDSN())

	db, err := ctltest.SetupDB()
	if err != nil {
		t.Fatalf("setup isolated overview DB: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		adminCfg := *cfg
		adminCfg.DBName = ""
		admin, err := sql.Open("mysql", adminCfg.FormatDSN())
		if err == nil {
			_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+name+"`")
			_ = admin.Close()
		}
	})
	return db
}

func overviewService(db *sql.DB) *Service {
	return New(db, nil, clock.NewFixed(overviewNow), nil, nil, nil)
}

func fetchOverview(t *testing.T, svc *Service) adminOverviewResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/overview", nil)
	rec := httptest.NewRecorder()
	svc.Overview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var resp adminOverviewResponse
	decodeBody(t, rec, &resp)
	return resp
}

func seedOverviewRows(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	var protocolID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM t_protocol LIMIT 1`).Scan(&protocolID); err != nil {
		t.Fatalf("seed protocol id: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO t_channel
			(name, protocol_id, region, base_url, upstream_key_enc, upstream_key_mask,
			 health_state, enabled, created_by, created_at, updated_at)
		VALUES ('t24-overview-abnormal', ?, 'DOMESTIC', 'https://example.invalid/v1', ?,
		        'sk-****0000', 'OPEN', 1, 1, ?, ?)`,
		protocolID, []byte("sealed"), overviewNow, overviewNow); err != nil {
		t.Fatalf("seed abnormal channel: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO t_review_task (biz_type, biz_id, applicant_id, state, created_at)
		VALUES ('QUOTA_APPLY', 1, 1, 'PENDING', ?)`, overviewNow); err != nil {
		t.Fatalf("seed pending review: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO t_ticket
			(ticket_no, user_id, ticket_type, title, description, state,
			 first_response_due_at, created_at, updated_at)
		VALUES ('TK2035010200000001', 1, 'TECH', '超时工单', '正文', 'OPEN', ?, ?, ?)`,
		overviewNow.Add(-time.Hour), overviewNow.Add(-2*time.Hour), overviewNow); err != nil {
		t.Fatalf("seed overdue ticket: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO t_prehold_hold
			(request_id, user_id, api_key_id, hold_micro, estimated_micro, model_id,
			 state, expires_at, node_id, created_at)
		VALUES (?, 1, 1, 100, 100, 1, 'HELD', ?, 'node', ?)`,
		strings.Repeat("a", 36), overviewNow.Add(-time.Minute), overviewNow.Add(-time.Hour)); err != nil {
		t.Fatalf("seed dangling prehold: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO t_reconcile_diff
			(diff_key, biz_date, diff_type, status, detected_at, created_at, updated_at)
		VALUES (?, ?, 'AMOUNT_MISMATCH', 'OPEN', ?, ?, ?)`,
		strings.Repeat("b", 64), overviewNow.In(overviewCST).Format("2006-01-02"),
		overviewNow, overviewNow, overviewNow); err != nil {
		t.Fatalf("seed recon diff: %v", err)
	}

	usage := []struct {
		status   int
		result   string
		consumed int64
	}{
		{200, "SUCCESS", 100},
		{500, "FAILED", 0},
		{429, "FAILED", 0},
	}
	for i, row := range usage {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO t_usage_record
				(request_id, user_id, api_key_id, api_key_name_snap, model_name_snap,
				 request_model_req, inbound_protocol, http_status, result,
				 consumed_micro, total_ms, created_at)
			VALUES (?, 1, 1, 't24-key', 't24-model', 't24-model', 'OPENAI_COMPAT', ?, ?, ?, 10, ?)`,
			fmt.Sprintf("%036d", i+1), row.status, row.result, row.consumed, overviewNow); err != nil {
			t.Fatalf("seed usage row %d: %v", i, err)
		}
	}
}

// A2: empty database must not 500, and every count is a real zero while the
// success rate is null (not 0).
func TestAdminOverviewEmpty(t *testing.T) {
	db := newOverviewDB(t)
	resp := fetchOverview(t, overviewService(db))

	if resp.Today.Requests != 0 || resp.Today.ConsumedMicro != 0 {
		t.Fatalf("today=%+v, want zero requests/consumed", resp.Today)
	}
	if resp.Today.SuccessRateBP != nil {
		t.Fatalf("success_rate_bp=%d, want null on an empty database", *resp.Today.SuccessRateBP)
	}
	if resp.Alerts != (adminOverviewAlerts{}) {
		t.Fatalf("alerts=%+v, want all zero", resp.Alerts)
	}
	if resp.Counts != (adminOverviewCounts{}) {
		t.Fatalf("counts=%+v, want all zero", resp.Counts)
	}
}

// A1: exact today aggregate and every alert/count from real rows.
func TestAdminOverviewAggregates(t *testing.T) {
	db := newOverviewDB(t)
	seedOverviewRows(t, db)
	resp := fetchOverview(t, overviewService(db))

	if resp.Today.Requests != 3 {
		t.Fatalf("today.requests=%d, want 3", resp.Today.Requests)
	}
	if resp.Today.ConsumedMicro != 100 {
		t.Fatalf("today.consumed_micro=%d, want 100", resp.Today.ConsumedMicro)
	}
	if resp.Today.SuccessRateBP == nil || *resp.Today.SuccessRateBP != 5000 {
		t.Fatalf("success_rate_bp=%v, want 5000 (1 success / (200+500); 429 excluded)",
			resp.Today.SuccessRateBP)
	}
	wantAlerts := adminOverviewAlerts{
		DanglingPrehold: 1,
		ReconDiff:       1,
		SLAOverdue:      1,
		ChannelAbnormal: 1,
	}
	if resp.Alerts != wantAlerts {
		t.Fatalf("alerts=%+v, want %+v", resp.Alerts, wantAlerts)
	}
	wantCounts := adminOverviewCounts{PendingReviews: 1, OpenTickets: 1}
	if resp.Counts != wantCounts {
		t.Fatalf("counts=%+v, want %+v", resp.Counts, wantCounts)
	}
}

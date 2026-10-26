package user

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/platform/clock"
)

func newOverviewUser(t *testing.T, recharge, bonus int64) int64 {
	t.Helper()
	ctx := context.Background()
	var uid int64
	var lastErr error
	for attempt := 0; attempt < 5 && uid == 0; attempt++ {
		var noise [4]byte
		_, _ = rand.Read(noise[:])
		userNo := fmt.Sprintf("U%s%03d", time.Now().Format("20060102"),
			int(binary.BigEndian.Uint32(noise[:]))%1000)
		email := fmt.Sprintf("t12-overview-%d-%d@test.local", time.Now().UnixNano(), attempt)
		res, err := testDB.ExecContext(ctx, `
			INSERT INTO t_user
				(user_no, email, password_hash, password_algo, password_updated_at,
				 nickname, role, status, created_at, updated_at)
			VALUES (?,?,'x','bcrypt',?,'t12-ov','PERSONAL','NORMAL',?,?)`,
			userNo, email, time.Now(), time.Now(), time.Now())
		if err != nil {
			lastErr = err
			if !strings.Contains(err.Error(), "uk_user_no") {
				t.Fatalf("insert user: %v", err)
			}
			continue
		}
		uid, err = res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
	}
	if uid == 0 {
		t.Fatalf("insert user: %v", lastErr)
	}
	if _, err := testDB.ExecContext(ctx, `
		INSERT INTO t_credit_account
			(user_id, balance_recharge_micro, balance_bonus_micro, balance_micro, created_at, updated_at)
		VALUES (?,?,?,?,?,?)`,
		uid, recharge, bonus, recharge+bonus, time.Now(), time.Now()); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_usage_record WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_credit_account WHERE user_id = ?`, uid)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_user WHERE id = ?`, uid)
	})
	return uid
}

func insertUsage(t *testing.T, uid int64, status, result string, consumed int64, at time.Time) {
	t.Helper()
	requestID := fmt.Sprintf("t12-%d-%d", time.Now().UnixNano(), consumed)
	if _, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_usage_record
			(request_id, user_id, api_key_id, api_key_name_snap, model_name_snap,
			 request_model_req, inbound_protocol, http_status, biz_code, result,
			 consumed_micro, total_ms, created_at)
		VALUES (?,?,1,'t12','local-qwen-plus','local-qwen-plus','OPENAI_COMPAT',?,'',?,?,10,?)`,
		requestID, uid, status, result, consumed, at.UTC()); err != nil {
		t.Fatalf("insert usage: %v", err)
	}
}

// F5-2 / F5-3: the overview is built from real aggregates, returns exactly 24
// hourly points, and has no source-channel field.
func TestOverviewAggregates(t *testing.T) {
	uid := newOverviewUser(t, 10_000, 5_000)
	now := time.Now()
	insertUsage(t, uid, "200", "SUCCESS", 100, now.Add(-10*time.Minute))
	insertUsage(t, uid, "500", "FAILED", 0, now.Add(-5*time.Minute))
	insertUsage(t, uid, "429", "FAILED", 0, now.Add(-time.Minute))

	svc := New(testDB, nil, clock.Real{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/console/overview", nil)
	req = req.WithContext(mw.WithSession(req.Context(), &mw.Session{UID: uid, Role: "PERSONAL"}))
	rec := httptest.NewRecorder()
	svc.Overview(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp overviewResponse
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Balance.Micro != 15_000 || resp.Balance.Credits != "1.5" || resp.Balance.CNY != "0.015" {
		t.Fatalf("balance = %+v", resp.Balance)
	}
	if resp.Today.Requests != 3 || resp.Today.ConsumedMicro != 100 || resp.Today.ConsumedCredits != "0.01" {
		t.Fatalf("today = %+v", resp.Today)
	}
	if resp.Today.SuccessRateBP == nil || *resp.Today.SuccessRateBP != 5000 {
		t.Fatalf("success_rate_bp = %v, want 5000 (1 success / (1 success + 1 upstream failure); 429 excluded)",
			resp.Today.SuccessRateBP)
	}
	if len(resp.Hourly) != 24 {
		t.Fatalf("hourly points = %d, want 24", len(resp.Hourly))
	}
	var hourlyRequests int64
	for _, point := range resp.Hourly {
		hourlyRequests += point.Requests
	}
	if hourlyRequests != 3 {
		t.Fatalf("hourly requests sum = %d, want 3", hourlyRequests)
	}
	if resp.Plan.Code != "FREE_DEFAULT" {
		t.Fatalf("plan = %+v", resp.Plan)
	}
	if resp.Quota.DailyQuotaMicro != nil || resp.Quota.RemainingMicro != nil {
		t.Fatalf("FREE_DEFAULT quota must be null, got %+v", resp.Quota)
	}
	if resp.ETA.Reachable || resp.ETA.Reason != "NO_DAILY_QUOTA" {
		t.Fatalf("eta = %+v", resp.ETA)
	}

	// F5-3: V1.0 has no source-channel label; the field must not exist.
	var raw map[string]any
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, exists := raw["source_channel"]; exists {
		t.Fatal("overview contains source_channel, which V1.0 does not implement")
	}
}

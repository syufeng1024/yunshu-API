package ops

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func channelRate(t *testing.T, svc *Service, uid, channelID int64) *int64 {
	t.Helper()
	rec := call(t, svc.GetChannel, http.MethodGet, "/api/admin/channels/"+itoa(channelID), nil,
		testCallOptions{uid: uid, role: "OPS", pathVal: map[string]string{"id": itoa(channelID)}})
	if rec.Code != http.StatusOK {
		t.Fatalf("get channel %d status=%d body=%s", channelID, rec.Code, rec.Body.String())
	}
	var resp channelDetailResponse
	decodeBody(t, rec, &resp)
	return resp.Channel.SuccessRateBP
}

// A6a: no attempts in the window -> null, never 10000.
func TestChannelSuccessRateNullWithoutAttempts(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	name := "t24-rate-none-" + uniqueSuffix()
	channel := createTestChannel(t, svc, uid, channelBody(t, name, "DOMESTIC"))

	rate := channelRate(t, svc, uid, channel.ID)
	if rate != nil {
		t.Fatalf("success_rate_bp=%d, want null without attempts", *rate)
	}
}

// A6b: two successes out of three attempts -> 6666 bp, and the list carries
// the same value.
func TestChannelSuccessRateComputed(t *testing.T) {
	svc := newTestService(t)
	uid := newTestUser(t, "OPS")
	name := "t24-rate-" + uniqueSuffix()
	channel := createTestChannel(t, svc, uid, channelBody(t, name, "DOMESTIC"))

	now := time.Now().UTC()
	requestID := fmt.Sprintf("%036x", time.Now().UnixNano())
	for i, result := range []string{"SUCCESS", "SUCCESS", "FAILED"} {
		if _, err := testDB.ExecContext(context.Background(), `
			INSERT INTO t_upstream_attempt
				(request_id, attempt_index, user_id, model_id, route_id, channel_id,
				 channel_name_snap, protocol_code, upstream_model, phase, result, total_ms, created_at)
			VALUES (?, ?, 1, 1, 1, ?, ?, 'OPENAI_COMPAT', 'm', 'COMPLETE', ?, 10, ?)`,
			requestID, i+1, channel.ID, name, result, now); err != nil {
			t.Fatalf("seed attempt %d: %v", i, err)
		}
	}
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(),
			`DELETE FROM t_upstream_attempt WHERE channel_id = ?`, channel.ID)
	})

	rate := channelRate(t, svc, uid, channel.ID)
	if rate == nil || *rate != 6666 {
		t.Fatalf("success_rate_bp=%v, want 6666", rate)
	}

	rec := call(t, svc.ListChannels, http.MethodGet,
		"/api/admin/channels?page=1&page_size=100", nil,
		testCallOptions{uid: uid, role: "OPS"})
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var list channelListResponse
	decodeBody(t, rec, &list)
	for _, item := range list.Items {
		if item.ID != channel.ID {
			continue
		}
		if item.SuccessRateBP == nil || *item.SuccessRateBP != 6666 {
			t.Fatalf("list success_rate_bp=%v, want 6666", item.SuccessRateBP)
		}
		return
	}
	t.Fatalf("channel %d not found in list", channel.ID)
}

package settle

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/jsonx"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

// A11: one request row and its attempts must share trace_id and request_id.
// The writer emits the log lines only after the INSERT succeeds, so this test
// drives the real MySQL fixtures from persist_test.go.
func TestA11RequestAndAttemptLogsShareTraceID(t *testing.T) {
	ctx := context.Background()
	rid := newRequestID(t)
	userID := newFundedUser(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	t.Cleanup(func() {
		_, _ = testDB.Exec(`DELETE FROM t_usage_record WHERE request_id = ?`, rid)
		_, _ = testDB.Exec(`DELETE FROM t_upstream_attempt WHERE request_id = ?`, rid)
	})

	var buf bytes.Buffer
	logger := obslog.NewWithWriter("gatewayd", "test", &buf, clock.Real{})
	w := NewUsageWriter(testDB, logger, clock.Real{})

	w.WriteUsage(UsageRecord{
		RequestID:     rid,
		UserID:        userID,
		APIKeyID:      5,
		ModelID:       0,
		ModelName:     "m",
		RequestModel:  "m",
		InboundProto:  "OPENAI_COMPAT",
		ChannelID:     10,
		ChannelName:   "ch",
		OutboundProto: "OPENAI_COMPAT",
		RouteID:       1,
		PriceRuleID:   2,
		GroupCode:     "standard",
		PriceRatioBP:  10000,
		BillingTier:   "BASE",
		HTTPStatus:    200,
		Result:        "SUCCESS",
		TokensIn:      5,
		TokensOut:     4,
		UsageSource:   "UPSTREAM_API",
		PreholdMicro:  1000,
		ConsumedMicro: 42,
		RefundedMicro: 958,
		TotalMS:       12,
		UpstreamMS:    11,
		CreatedAt:     now,
	})
	for i := 0; i < 2; i++ {
		w.WriteAttempt(AttemptRecord{
			RequestID:     rid,
			AttemptIndex:  i,
			UserID:        userID,
			ModelID:       1,
			RouteID:       1,
			ChannelID:     10,
			ChannelName:   "ch",
			ProtocolCode:  "OPENAI_COMPAT",
			UpstreamModel: "m",
			Phase:         "P17",
			Result:        "SUCCESS",
			HTTPStatus:    200,
			TotalMS:       11,
			CreatedAt:     now,
		})
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("log lines = %d, want 3:\n%s", len(lines), buf.String())
	}

	wantKinds := []string{obslog.KindRequest, obslog.KindAttempt, obslog.KindAttempt}
	for i, line := range lines {
		var got map[string]any
		if err := jsonx.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d unmarshal: %v (%q)", i, err, line)
		}
		if got["log_kind"] != wantKinds[i] {
			t.Fatalf("line %d log_kind = %v, want %q", i, got["log_kind"], wantKinds[i])
		}
		if got["trace_id"] != rid {
			t.Fatalf("line %d trace_id = %v, want %q", i, got["trace_id"], rid)
		}
		if got["request_id"] != rid {
			t.Fatalf("line %d request_id = %v, want %q", i, got["request_id"], rid)
		}
	}

	var request map[string]any
	if err := jsonx.Unmarshal([]byte(lines[0]), &request); err != nil {
		t.Fatalf("request line unmarshal: %v", err)
	}
	if request["stage"] != obslog.StageDone {
		t.Fatalf("stage = %v, want %q for a successful row", request["stage"], obslog.StageDone)
	}
	if request["biz_date"] != obslog.BizDate(now) {
		t.Fatalf("biz_date = %v, want %q", request["biz_date"], obslog.BizDate(now))
	}
	if request["attempt_count"] != float64(0) {
		t.Fatalf("attempt_count = %v, want the row value 0", request["attempt_count"])
	}
}

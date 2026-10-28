package metrics

import (
	"strings"
	"testing"
)

// tds53Metrics is the frozen TDS §5.3 list (23 items). The test below is the
// A8 gate: every name must either be registered (and therefore appear in the
// export text as a HELP/TYPE pair) or be documented in deferredMetrics.
var tds53Metrics = map[string]Kind{
	"gw_requests_total":                 KindCounter,
	"gw_request_duration_seconds":       KindHistogram,
	"gw_ttft_seconds":                   KindHistogram,
	"gw_upstream_attempts_total":        KindCounter,
	"gw_circuit_state":                  KindGauge,
	"gw_ratelimit_rejected_total":       KindCounter,
	"gw_ratelimit_degraded_total":       KindCounter,
	"gw_ratelimit_unavailable_total":    KindCounter,
	"gw_inflight":                       KindGauge,
	"gw_prehold_orphan_total":           KindCounter,
	"gw_settle_fallback_total":          KindCounter,
	"gw_ledger_reconcile_diff":          KindGauge,
	"gw_balance_cas_retry_total":        KindCounter,
	"gw_config_stale_seconds":           KindGauge,
	"gw_config_bootstrap_used":          KindCounter,
	"gw_strategy_fallback_total":        KindCounter,
	"gw_region_guard_violation_total":   KindCounter,
	"gw_backpressure_rejected_total":    KindCounter,
	"gw_upstream_frame_malformed_total": KindCounter,
	"gw_log_written_total":              KindCounter,
	"gw_log_dropped_total":              KindCounter,
	"gw_log_backlog":                    KindGauge,
	"gw_config_item_rejected_total":     KindCounter,
}

// deferredMetrics documents any TDS §5.3 metric that is deliberately not
// registered. It is empty: all 23 families are registered. The two metrics the
// TDS implementation-status table listed as deferred
// (gw_ledger_reconcile_diff and gw_config_bootstrap_used) now have real call
// sites in recon.Daily and gatewayd respectively. They stay lazy — no series
// until the first event — so a quiet process does not fabricate a zero that
// would disarm the "!= 0 即 P0" / "> 0 即 P1" alerts.
var deferredMetrics = map[string]string{}

func TestA8TDS53MetricSetIsExported(t *testing.T) {
	if got := len(tds53Metrics); got != 23 {
		t.Fatalf("TDS §5.3 list has %d items, want 23", got)
	}
	reg := NewRegistry()
	_ = NewRecorder(reg)
	text := string(reg.PrometheusText())

	for name, kind := range tds53Metrics {
		if reason, deferred := deferredMetrics[name]; deferred {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s is in deferredMetrics without a reason", name)
			}
			continue
		}
		typeLine := "# TYPE " + name + " " + kind.String() + "\n"
		if !strings.Contains(text, typeLine) {
			t.Errorf("%s is neither registered (missing %q) nor in deferredMetrics", name, typeLine)
		}
	}
}

func TestA8TriggerSamplesAreBounded(t *testing.T) {
	// A recorder call must never create a label outside the closed sets. This
	// is a direct guard on the cardinality contract, independent of the text
	// export.
	reg := NewRegistry()
	rec := NewRecorder(reg)
	rec.RatelimitRejected("rpm")
	rec.RatelimitRejected("not-a-dimension")
	rec.BackpressureRejected("custom-tier")
	rec.SettleFallback("operator-wrote-a-request-id")
	rec.LogDropped("upstream-body")
	rec.ConfigItemRejected("not-a-namespace", "not-a-reason")
	rec.StrategyFallback("not-a-kind", "prod-value-123")

	text := string(reg.PrometheusText())
	for _, forbidden := range []string{
		"not-a-dimension", "custom-tier", "operator-wrote-a-request-id",
		"upstream-body", "not-a-namespace", "not-a-reason",
		"not-a-kind", "prod-value-123",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("unbounded label value %q leaked into the export:\n%s", forbidden, text)
		}
	}
	if !strings.Contains(text, `dim="other"`) {
		t.Fatalf("unknown dimension was not mapped to other:\n%s", text)
	}
	if !strings.Contains(text, `configured="other"`) {
		t.Fatalf("unknown configured value was not mapped to other:\n%s", text)
	}
}

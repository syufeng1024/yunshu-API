package gw

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/breaker"
	"yunshu.dev/core/internal/jsonx"
	obslog "yunshu.dev/core/internal/obs/log"
	obsmetrics "yunshu.dev/core/internal/obs/metrics"
)

// keyedBreaker opens only the listed channels so a dedicated-channel outage
// does not also remove the shared candidates from the same request.
type keyedBreaker map[int64]bool

func (b keyedBreaker) State(_ context.Context, channelID int64) (breaker.State, int64, error) {
	if b[channelID] {
		return breaker.StateOpen, 1 << 62, nil
	}
	return breaker.StateClosed, 0, nil
}

// dedicatedCatalog models one model served by shared channel 1 and dedicated
// channels 3 (and 4 in the multi-candidate case). Dedicated routes carry the
// lower priority number, so a usable dedicated channel wins the first attempt.
func dedicatedCatalog(fallback flexBool, extraDedicated bool) *Catalog {
	routes := []RouteEntry{
		{ID: 1, ModelID: 1, ChannelID: 1, ProtocolID: 1, Region: RegionDomestic, Weight: 10, Priority: 100, Enabled: true, StreamSupported: true},
		{ID: 2, ModelID: 1, ChannelID: 3, ProtocolID: 1, Region: RegionDomestic, Weight: 10, Priority: 10, Enabled: true, StreamSupported: true},
	}
	channels := []ChannelEntry{
		{ID: 1, Region: RegionDomestic, GroupScopeMode: ScopeAll, Enabled: true, StreamSupported: true},
		{ID: 3, Region: RegionDomestic, GroupScopeMode: ScopeAll, DedicatedScopeMode: ScopeSelected,
			FallbackAllowed: fallback, Enabled: true, StreamSupported: true},
	}
	dedicatedUsers := map[string][]int64{"3": {7}}
	if extraDedicated {
		routes = append(routes,
			RouteEntry{ID: 3, ModelID: 1, ChannelID: 4, ProtocolID: 1, Region: RegionDomestic, Weight: 10, Priority: 10, Enabled: true, StreamSupported: true},
			RouteEntry{ID: 4, ModelID: 1, ChannelID: 5, ProtocolID: 1, Region: RegionDomestic, Weight: 10, Priority: 100, Enabled: true, StreamSupported: true},
			RouteEntry{ID: 5, ModelID: 1, ChannelID: 6, ProtocolID: 1, Region: RegionDomestic, Weight: 10, Priority: 100, Enabled: true, StreamSupported: true},
		)
		channels = append(channels,
			ChannelEntry{ID: 4, Region: RegionDomestic, GroupScopeMode: ScopeAll, DedicatedScopeMode: ScopeSelected,
				FallbackAllowed: fallback, Enabled: true, StreamSupported: true},
			ChannelEntry{ID: 5, Region: RegionDomestic, GroupScopeMode: ScopeAll, Enabled: true, StreamSupported: true},
			ChannelEntry{ID: 6, Region: RegionDomestic, GroupScopeMode: ScopeAll, Enabled: true, StreamSupported: true},
		)
		dedicatedUsers["4"] = []int64{7}
	}
	return NewStaticCatalog(&RoutingSnapshot{
		Models: []ModelEntry{{ID: 1, ModelName: "m", Region: RegionDomestic,
			ModelType: ModelTypeText, ListingState: ListingListed}},
		Routes:   routes,
		Channels: channels,
		Groups: map[string]GroupEntry{"standard": {
			Code: "standard", ModelScopeMode: ScopeAll, ChannelScopeMode: ScopeAll,
		}},
		KeyModels:      map[string]KeyModelGrant{"10": {Mode: ScopeAll}},
		DedicatedUsers: dedicatedUsers,
	})
}

func dedicatedDeps(catalog *Catalog) (*Deps, *obsmetrics.Registry, *bytes.Buffer) {
	deps := staticDeps(catalog)
	reg := obsmetrics.NewRegistry()
	deps.Metrics = obsmetrics.NewRecorder(reg)
	buf := &bytes.Buffer{}
	deps.Logger = obslog.NewWithWriter("gatewayd", "test", buf, nil)
	return deps, reg, buf
}

func dedicatedFallbackCount(t *testing.T, reg *obsmetrics.Registry) int64 {
	t.Helper()
	for _, f := range reg.Snapshot().Families {
		if f.Name != "gw_dedicated_fallback_total" {
			continue
		}
		var n int64
		for _, s := range f.Samples {
			n += s.Value
		}
		return n
	}
	return 0
}

func warningLines(buf *bytes.Buffer) []string {
	lines := make([]string, 0, 1)
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// D1: the dedicated channel is healthy; routing picks it and leaves no
// fallback evidence.
func TestT32D1DedicatedAvailableNoFallback(t *testing.T) {
	deps, reg, buf := dedicatedDeps(dedicatedCatalog(flexBool(false), false))
	c := &Ctx{
		RequestID: "req-t32-d1",
		Model:     &ModelEntry{ID: 1, ModelName: "m"},
		Auth:      &AuthContext{UserID: 7, KeyID: 10, GroupCode: "standard"},
	}
	if err := NewRouterStage(deps).Run(t.Context(), c); err != nil {
		t.Fatalf("router: %v", err)
	}
	if c.Route == nil || c.Route.ChannelID != 3 {
		t.Fatalf("route = %+v, want dedicated channel 3", c.Route)
	}
	if got := len(warningLines(buf)); got != 0 {
		t.Fatalf("warning lines = %d, want 0: %q", got, buf.String())
	}
	if got := dedicatedFallbackCount(t, reg); got != 0 {
		t.Fatalf("gw_dedicated_fallback_total = %d, want 0", got)
	}
}

// D2: the dedicated channel is open in the breaker and fallback is forbidden;
// the shared channel must not be selected and the existing degradation code
// is returned.
func TestT32D2DedicatedDownFallbackForbidden(t *testing.T) {
	deps, reg, buf := dedicatedDeps(dedicatedCatalog(flexBool(false), false))
	deps.Breaker = keyedBreaker{3: true}
	c := &Ctx{
		RequestID: "req-t32-d2",
		Model:     &ModelEntry{ID: 1, ModelName: "m"},
		Auth:      &AuthContext{UserID: 7, KeyID: 10, GroupCode: "standard"},
	}
	err := NewRouterStage(deps).Run(t.Context(), c)
	assertAPIError(t, err, apierr.CodeNoAvailableUpstream, http.StatusServiceUnavailable)
	if len(c.Routes) != 0 {
		t.Fatalf("routes = %+v, want no shared fallback", c.Routes)
	}
	if got := len(warningLines(buf)); got != 0 {
		t.Fatalf("warning lines = %d, want 0: %q", got, buf.String())
	}
	if got := dedicatedFallbackCount(t, reg); got != 0 {
		t.Fatalf("gw_dedicated_fallback_total = %d, want 0", got)
	}
}

// D3: the dedicated channel is down and fallback is allowed; the shared
// channel serves the request and exactly one evidence line/count is emitted.
func TestT32D3DedicatedDownFallbackAllowed(t *testing.T) {
	deps, reg, buf := dedicatedDeps(dedicatedCatalog(flexBool(true), false))
	deps.Breaker = keyedBreaker{3: true}
	c := &Ctx{
		RequestID: "req-t32-d3",
		Model:     &ModelEntry{ID: 1, ModelName: "m"},
		Auth:      &AuthContext{UserID: 7, KeyID: 10, GroupCode: "standard"},
	}
	if err := NewRouterStage(deps).Run(t.Context(), c); err != nil {
		t.Fatalf("router: %v", err)
	}
	if c.Route == nil || c.Route.ChannelID != 1 {
		t.Fatalf("route = %+v, want shared channel 1", c.Route)
	}

	lines := warningLines(buf)
	if len(lines) != 1 {
		t.Fatalf("warning lines = %d, want 1: %q", len(lines), buf.String())
	}
	var line map[string]any
	if err := jsonx.Unmarshal([]byte(lines[0]), &line); err != nil {
		t.Fatalf("unmarshal log line: %v (line=%q)", err, lines[0])
	}
	if line["level"] != "warn" || line["stage"] != "P12" {
		t.Fatalf("log level/stage = %v/%v, want warn/P12", line["level"], line["stage"])
	}
	if line["request_id"] != "req-t32-d3" || line["uid"] != "7" || line["model"] != "m" {
		t.Fatalf("correlation fields = %v, want req-t32-d3/7/m", line)
	}
	if line["reason"] != "breaker" {
		t.Fatalf("reason = %v, want breaker", line["reason"])
	}
	if line["excluded_channel_ids"] != "3" {
		t.Fatalf("excluded_channel_ids = %v, want 3", line["excluded_channel_ids"])
	}
	if got := dedicatedFallbackCount(t, reg); got != 1 {
		t.Fatalf("gw_dedicated_fallback_total = %d, want 1", got)
	}
}

// D4: a user on no dedicated list keeps the shared pool and produces no
// fallback evidence.
func TestT32D4NonDedicatedUserUnchanged(t *testing.T) {
	deps, reg, buf := dedicatedDeps(dedicatedCatalog(flexBool(false), false))
	c := &Ctx{
		RequestID: "req-t32-d4",
		Model:     &ModelEntry{ID: 1, ModelName: "m"},
		Auth:      &AuthContext{UserID: 8, KeyID: 10, GroupCode: "standard"},
	}
	if err := NewRouterStage(deps).Run(t.Context(), c); err != nil {
		t.Fatalf("router: %v", err)
	}
	if c.Route == nil || c.Route.ChannelID != 1 {
		t.Fatalf("route = %+v, want shared channel 1", c.Route)
	}
	for _, cand := range c.Routes {
		if cand.ChannelID == 3 {
			t.Fatalf("dedicated channel 3 leaked into a non-listed user's candidates: %+v", c.Routes)
		}
	}
	if got := len(warningLines(buf)); got != 0 {
		t.Fatalf("warning lines = %d, want 0: %q", got, buf.String())
	}
	if got := dedicatedFallbackCount(t, reg); got != 0 {
		t.Fatalf("gw_dedicated_fallback_total = %d, want 0", got)
	}
}

// D5: two dedicated channels down and three shared candidates remain; the
// request is counted once, not once per candidate or per excluded channel.
func TestT32D5MultipleCandidatesCountOnce(t *testing.T) {
	deps, reg, buf := dedicatedDeps(dedicatedCatalog(flexBool(true), true))
	deps.Breaker = keyedBreaker{3: true, 4: true}
	c := &Ctx{
		RequestID: "req-t32-d5",
		Model:     &ModelEntry{ID: 1, ModelName: "m"},
		Auth:      &AuthContext{UserID: 7, KeyID: 10, GroupCode: "standard"},
	}
	if err := NewRouterStage(deps).Run(t.Context(), c); err != nil {
		t.Fatalf("router: %v", err)
	}
	if len(c.Routes) != 3 {
		t.Fatalf("routes = %+v, want the three shared candidates", c.Routes)
	}
	for _, cand := range c.Routes {
		if cand.ChannelID == 3 || cand.ChannelID == 4 {
			t.Fatalf("unavailable dedicated channel %d selected: %+v", cand.ChannelID, c.Routes)
		}
	}
	lines := warningLines(buf)
	if len(lines) != 1 {
		t.Fatalf("warning lines = %d, want 1: %q", len(lines), buf.String())
	}
	var line map[string]any
	if err := jsonx.Unmarshal([]byte(lines[0]), &line); err != nil {
		t.Fatalf("unmarshal log line: %v (line=%q)", err, lines[0])
	}
	if line["excluded_channel_ids"] != "3,4" {
		t.Fatalf("excluded_channel_ids = %v, want 3,4", line["excluded_channel_ids"])
	}
	if got := dedicatedFallbackCount(t, reg); got != 1 {
		t.Fatalf("gw_dedicated_fallback_total = %d, want 1", got)
	}
}

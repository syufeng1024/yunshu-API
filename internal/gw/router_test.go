package gw

import (
	"net/http"
	"testing"

	"yunshu.dev/core/internal/apierr"
)

func TestA10RouterExcludesOverseasDedicatedAndGroupChannels(t *testing.T) {
	catalog := staticCatalog(
		[]ModelEntry{{ID: 1, ModelName: "m", Region: RegionDomestic,
			ModelType: ModelTypeText, ListingState: ListingListed}},
		[]RouteEntry{
			{ID: 1, ModelID: 1, ChannelID: 1, ProtocolID: 1, Region: RegionDomestic, Weight: 10, Priority: 100, Enabled: true, StreamSupported: true},
			{ID: 2, ModelID: 1, ChannelID: 2, ProtocolID: 1, Region: RegionOverseas, Weight: 10, Priority: 100, Enabled: true, StreamSupported: true},
			{ID: 3, ModelID: 1, ChannelID: 3, ProtocolID: 1, Region: RegionDomestic, Weight: 10, Priority: 100, Enabled: true, StreamSupported: true},
			{ID: 4, ModelID: 1, ChannelID: 4, ProtocolID: 1, Region: RegionDomestic, Weight: 10, Priority: 100, Enabled: true, StreamSupported: true},
			{ID: 5, ModelID: 1, ChannelID: 5, ProtocolID: 1, Region: RegionDomestic, Weight: 10, Priority: 100, Enabled: true, StreamSupported: true},
		},
		[]ChannelEntry{
			{ID: 1, Region: RegionDomestic, GroupScopeMode: ScopeAll, Enabled: true, StreamSupported: true},
			{ID: 2, Region: RegionOverseas, GroupScopeMode: ScopeAll, Enabled: true, StreamSupported: true},
			{ID: 3, Region: RegionDomestic, GroupScopeMode: ScopeAll, DedicatedScopeMode: ScopeSelected, Enabled: true, StreamSupported: true},
			{ID: 4, Region: RegionDomestic, GroupScopeMode: ScopeSelected, Enabled: true, StreamSupported: true},
			{ID: 5, Region: RegionDomestic, GroupScopeMode: ScopeAll, Enabled: true, StreamSupported: true},
		},
		map[string]GroupEntry{"standard": {
			Code: "standard", ModelScopeMode: ScopeAll, ChannelScopeMode: ScopeSelected,
			Channels: []int64{1, 2, 3, 5},
		}},
		map[string]KeyModelGrant{"10": {Mode: ScopeAll}},
	)
	deps := staticDeps(catalog)
	deps.Logger = nil
	stage := NewRouterStage(deps)
	c := &Ctx{
		Model: &ModelEntry{ID: 1},
		Auth:  &AuthContext{UserID: 7, KeyID: 10, GroupCode: "standard"},
	}
	if err := stage.Run(t.Context(), c); err != nil {
		t.Fatalf("router: %v", err)
	}
	if len(c.Routes) != 2 {
		t.Fatalf("routes = %+v, want channels 1 and 5", c.Routes)
	}
	for _, cand := range c.Routes {
		if cand.ChannelID == 2 || cand.ChannelID == 3 || cand.ChannelID == 4 {
			t.Fatalf("channel %d must be excluded", cand.ChannelID)
		}
	}
	t.Logf("candidate channel ids: %d,%d", c.Routes[0].ChannelID, c.Routes[1].ChannelID)
}

func TestRouterGroupEmptyUsesDistinctCode(t *testing.T) {
	catalog := staticCatalog(
		[]ModelEntry{{ID: 1, ModelName: "m", Region: RegionDomestic, ListingState: ListingListed}},
		[]RouteEntry{{ID: 1, ModelID: 1, ChannelID: 1, ProtocolID: 1, Region: RegionDomestic, Weight: 1, Priority: 100, Enabled: true, StreamSupported: true}},
		[]ChannelEntry{{ID: 1, Region: RegionDomestic, GroupScopeMode: ScopeSelected, Enabled: true, StreamSupported: true}},
		map[string]GroupEntry{"standard": {Code: "standard", ModelScopeMode: ScopeAll, ChannelScopeMode: ScopeSelected}},
		map[string]KeyModelGrant{"10": {Mode: ScopeAll}},
	)
	c := &Ctx{Model: &ModelEntry{ID: 1}, Auth: &AuthContext{UserID: 7, KeyID: 10, GroupCode: "standard"}}
	err := NewRouterStage(staticDeps(catalog)).Run(t.Context(), c)
	assertAPIError(t, err, apierr.CodeNoAvailableUpstreamForGroup, http.StatusServiceUnavailable)
}

func TestA11WeightDistribution(t *testing.T) {
	deps := staticDeps(nil)
	stage := NewRouterStage(deps)
	// Deterministic PCG so a failure is reproducible.
	stage.Rand = newSeededIntN(42)
	items := []RouteCandidate{
		{ChannelID: 1, Weight: 90, Priority: 100},
		{ChannelID: 2, Weight: 10, Priority: 100},
	}
	const draws = 100_000
	a := 0
	for i := 0; i < draws; i++ {
		order := stage.weightedPermutation(t.Context(), append([]RouteCandidate(nil), items...))
		if order[0].ChannelID == 1 {
			a++
		}
	}
	ratio := float64(a) / float64(draws)
	t.Logf("A=90/B=10 over %d draws: A share = %.4f", draws, ratio)
	if ratio < 0.89 || ratio > 0.91 {
		t.Fatalf("A share = %.4f, want 0.90±0.01", ratio)
	}
}

func TestRouterAllZeroWeightsAreEqual(t *testing.T) {
	stage := NewRouterStage(staticDeps(nil))
	stage.Rand = newSeededIntN(1)
	items := []RouteCandidate{
		{ChannelID: 1, Weight: 0, Priority: 100},
		{ChannelID: 2, Weight: 0, Priority: 100},
	}
	seen := map[int64]int{}
	for i := 0; i < 1000; i++ {
		order := stage.weightedPermutation(t.Context(), append([]RouteCandidate(nil), items...))
		seen[order[0].ChannelID]++
	}
	if seen[1] < 400 || seen[2] < 400 {
		t.Fatalf("zero-weight distribution = %v, want roughly equal", seen)
	}
}

func TestRouterZeroWeightIsSuspendedWhenOthersCarryWeight(t *testing.T) {
	stage := NewRouterStage(staticDeps(nil))
	stage.Rand = newSeededIntN(7)
	candidates := []RouteCandidate{
		{ChannelID: 1, Weight: 10, Priority: 100},
		{ChannelID: 2, Weight: 0, Priority: 100},
	}
	for i := 0; i < 100; i++ {
		order := stage.order(t.Context(), append([]RouteCandidate(nil), candidates...))
		if len(order) != 1 || order[0].ChannelID != 1 {
			t.Fatalf("order = %+v; a weight-0 route must not be a fallback", order)
		}
	}
}

func newSeededIntN(seed uint64) func(int) int {
	return newPCGRand(seed).IntN
}

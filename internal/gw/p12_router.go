package gw

import (
	"context"
	"math/rand/v2"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/breaker"
	obslog "yunshu.dev/core/internal/obs/log"
)

// RouteCandidate is one routable (route, channel) pair after filtering.
type RouteCandidate struct {
	RouteID           int64
	ChannelID         int64
	ProtocolID        int64
	UpstreamModelName string
	Priority          int64
	Weight            int64
	MaxTokensCeiling  int64
	StreamSupported   bool
}

// RouterStage performs P12 (and the P13 breaker filter folded into it, per
// TDS §3.1 step 4: the breaker state is one more exclusion in the same loop).
type RouterStage struct {
	d *Deps

	// Rand is injectable so the 100k-sample distribution test is deterministic.
	// It must return a value in [0,n).
	Rand func(n int) int
}

// NewRouterStage builds P12.
func NewRouterStage(d *Deps) *RouterStage {
	return &RouterStage{d: d, Rand: rand.IntN}
}

// Name implements Stage.
func (s *RouterStage) Name() string { return StageP12 }

// Run implements Stage.
//
// Filter order: model routes ∩ route enabled ∩ channel enabled ∩ route region
// DOMESTIC ∩ channel region DOMESTIC ∩ stream capability ∩ group channel scope
// ∩ dedicated list ∩ breaker not OPEN. The region filter is a hard exclusion,
// not a priority: an OVERSEAS row must never be selected even if misconfigured
// into the database (PRD §3.4).
func (s *RouterStage) Run(ctx context.Context, c *Ctx) error {
	if c.Model == nil || c.Auth == nil {
		return StageErr(StageP12, apierr.New(apierr.CodeInternalError))
	}
	snap, err := s.d.Catalog.Current()
	if err != nil {
		return StageErr(StageP12, apierr.New(apierr.CodeConfigUnavailable).WithCause(err))
	}
	group, _ := snap.Group(c.Auth.GroupCode)

	nowMS := s.d.ClockNowMS()
	var (
		candidates []RouteCandidate
		groupCut   bool
	)
	for _, route := range snap.RoutesFor(c.Model.ID) {
		ch, ok := snap.Channel(route.ChannelID)
		if !ok {
			continue
		}
		if !bool(route.Enabled) || !bool(ch.Enabled) {
			continue
		}
		if route.Region != RegionDomestic || ch.Region != RegionDomestic {
			continue
		}
		if c.Stream && (!bool(route.StreamSupported) || !bool(ch.StreamSupported)) {
			continue
		}
		if !group.HasChannel(ch.ID) {
			groupCut = true
			continue
		}
		if ch.DedicatedScopeMode == ScopeSelected && !snap.IsDedicated(ch.ID, c.Auth.UserID) {
			// A dedicated channel the user is not listed on is invisible. If
			// that empties the candidate set the result is the capacity code,
			// not the group code: the group is fine, the user simply has no
			// dedicated entitlement.
			continue
		}
		if s.breakerOpen(ctx, ch.ID, nowMS) {
			continue
		}
		candidates = append(candidates, RouteCandidate{
			RouteID:           route.ID,
			ChannelID:         ch.ID,
			ProtocolID:        route.ProtocolID,
			UpstreamModelName: route.UpstreamModelName,
			Priority:          route.Priority,
			Weight:            route.Weight,
			MaxTokensCeiling:  route.MaxTokensCeiling,
			StreamSupported:   bool(route.StreamSupported),
		})
	}

	if len(candidates) == 0 {
		if groupCut {
			return StageErr(StageP12, apierr.New(apierr.CodeNoAvailableUpstreamForGroup))
		}
		return StageErr(StageP12, apierr.New(apierr.CodeNoAvailableUpstream))
	}

	// Route-level max_tokens ceiling (E-23). Routes with ceiling 0 are
	// unlimited. Only when every candidate is too small does the request fail,
	// and the reported ceiling is the largest one, i.e. the best the platform
	// can do right now.
	if c.IR != nil && c.IR.MaxTokens != nil && *c.IR.MaxTokens > 0 {
		requested := *c.IR.MaxTokens
		capable := make([]RouteCandidate, 0, len(candidates))
		var bestCeiling int64
		for _, cand := range candidates {
			if cand.MaxTokensCeiling > bestCeiling {
				bestCeiling = cand.MaxTokensCeiling
			}
			if cand.MaxTokensCeiling == 0 || requested <= cand.MaxTokensCeiling {
				capable = append(capable, cand)
			}
		}
		if len(capable) == 0 {
			return StageErr(StageP12, apierr.Newf(apierr.CodeMaxTokensExceeded, bestCeiling, requested))
		}
		candidates = capable
	}

	ordered := s.order(ctx, candidates)
	c.Routes = ordered
	c.Route = &c.Routes[0]
	return nil
}

// breakerOpen reports whether the channel's K16 state is OPEN and its backoff
// has not elapsed. A Redis error is fail-open: the breaker protects upstream
// capacity, and treating an unreadable state as OPEN would reject every
// channel during a Redis outage (breaker package contract).
func (s *RouterStage) breakerOpen(ctx context.Context, channelID, nowMS int64) bool {
	if s.d.Breaker == nil {
		return false
	}
	state, openUntilMS, err := s.d.Breaker.State(ctx, channelID)
	if err != nil {
		if s.d.Logger != nil {
			s.d.Logger.Warn(ctx, "circuit_breaker_read_failed: admitting channel", obslog.Fields{Stage: "P12"})
		}
		return false
	}
	return state == breaker.StateOpen && openUntilMS > nowMS
}

// order groups by priority (ascending) and applies a weighted permutation
// inside each group. The first priority group supplies the initial attempts;
// later groups are fallbacks.
func (s *RouterStage) order(ctx context.Context, candidates []RouteCandidate) []RouteCandidate {
	// TDS §1.4 filters r.weight > 0 in SQL. When at least one route carries a
	// positive weight, zero-weight routes are suspended (graceful gating) and
	// must not appear even as fallbacks. Only an all-zero set falls back to
	// equal weights (card §4.13).
	var total int64
	for _, cand := range candidates {
		if cand.Weight > 0 {
			total += cand.Weight
		}
	}
	if total > 0 {
		positive := make([]RouteCandidate, 0, len(candidates))
		for _, cand := range candidates {
			if cand.Weight > 0 {
				positive = append(positive, cand)
			}
		}
		candidates = positive
	}

	// RoutesFor already sorted by priority, so a stable pass groups them
	// without a separate sort.
	out := make([]RouteCandidate, 0, len(candidates))
	for i := 0; i < len(candidates); {
		j := i + 1
		for j < len(candidates) && candidates[j].Priority == candidates[i].Priority {
			j++
		}
		bucket := append([]RouteCandidate(nil), candidates[i:j]...)
		out = append(out, s.weightedPermutation(ctx, bucket)...)
		i = j
	}
	return out
}

func (s *RouterStage) weightedPermutation(ctx context.Context, items []RouteCandidate) []RouteCandidate {
	if len(items) <= 1 {
		return items
	}
	weights := make([]int64, len(items))
	var sum int64
	for i, item := range items {
		if item.Weight > 0 {
			weights[i] = item.Weight
			sum += item.Weight
		}
	}
	if sum == 0 {
		for i := range weights {
			weights[i] = 1
		}
		sum = int64(len(items))
		if s.d.Logger != nil {
			s.d.Logger.Warn(ctx, "route_weights_all_zero: using equal weights",
				obslog.Fields{Stage: "P12"})
		}
	}

	intN := s.Rand
	if intN == nil {
		intN = rand.IntN
	}
	out := make([]RouteCandidate, 0, len(items))
	for len(items) > 0 {
		pick := intN(int(sum))
		if pick < 0 {
			pick = 0
		}
		acc := int64(0)
		idx := len(items) - 1
		for i, w := range weights {
			if w <= 0 {
				continue
			}
			acc += w
			if int64(pick) < acc {
				idx = i
				break
			}
		}
		out = append(out, items[idx])
		sum -= weights[idx]
		items = append(items[:idx], items[idx+1:]...)
		weights = append(weights[:idx], weights[idx+1:]...)
		if sum <= 0 && len(items) > 0 {
			for i := range weights {
				weights[i] = 1
			}
			sum = int64(len(items))
		}
	}
	return out
}

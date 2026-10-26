package gw

import (
	"context"
	"net/netip"

	"yunshu.dev/core/internal/apierr"
	obslog "yunshu.dev/core/internal/obs/log"
)

// IPStage performs P06. It is a pure in-memory check: the rule set travels in
// the routing snapshot, so no Redis or database read happens on this path.
type IPStage struct{ d *Deps }

// NewIPStage builds P06.
func NewIPStage(d *Deps) *IPStage { return &IPStage{d: d} }

// Name implements Stage.
func (s *IPStage) Name() string { return "P06" }

// Run implements Stage. The whitelist content is never echoed: the message is
// the fixed code template (PRD E.1).
func (s *IPStage) Run(ctx context.Context, c *Ctx) error {
	if c.Auth == nil {
		return StageErr("P06", apierr.New(apierr.CodeInternalError))
	}
	if s.d.Catalog == nil {
		return nil
	}
	snap, err := s.d.Catalog.Current()
	if err != nil {
		return StageErr("P06", apierr.New(apierr.CodeConfigUnavailable).WithCause(err))
	}
	rule, ok := snap.IPRuleFor(c.Auth.KeyID)
	if !ok || len(rule.CIDRs) == 0 {
		// No rule row / empty whitelist means ip_whitelist_mode=OFF.
		return nil
	}
	if rule.Version != c.Auth.IPMRuleVer {
		// K13.ipm and the snapshot disagree. The control plane writes both in
		// one operation, so this is a transient skew; deny until it converges
		// rather than applying a whitelist the key was not bound to.
		if s.d.Logger != nil {
			s.d.Logger.Warn(ctx, "ip_rule_version_mismatch: denying until cache and snapshot converge",
				obslog.Fields{Stage: "P06"})
		}
		return StageErr("P06", apierr.New(apierr.CodeIPNotAllowed))
	}
	if !c.ClientIP.IsValid() {
		return StageErr("P06", apierr.New(apierr.CodeIPNotAllowed))
	}
	for _, cidr := range rule.CIDRs {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			// A malformed rule is an operator error. Deny instead of skipping:
			// skipping the one broken entry could be the entry that matters.
			if s.d.Logger != nil {
				s.d.Logger.Error(ctx, "ip_rule_invalid_cidr", obslog.Fields{Stage: "P06"})
			}
			return StageErr("P06", apierr.New(apierr.CodeIPNotAllowed))
		}
		if prefix.Contains(c.ClientIP) {
			return nil
		}
	}
	return StageErr("P06", apierr.New(apierr.CodeIPNotAllowed))
}

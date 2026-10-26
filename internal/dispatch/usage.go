package dispatch

import (
	"strings"

	"yunshu.dev/core/internal/billing"
	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/stream"
	"yunshu.dev/relaykit/ir"
)

// availableBase prefers the ledger's authoritative balance at prehold time.
// K26 is a TTL cache that settlement does not invalidate, so using
// Ctx.AvailableMicro here would make consecutive responses drift by the
// previous request's charge.
func (d *Dispatcher) availableBase(c *gw.Ctx) int64 {
	if c != nil && c.Hold != nil && c.Hold.AvailableBeforeMicro > 0 {
		return c.Hold.AvailableBeforeMicro
	}
	if c != nil {
		return c.AvailableMicro
	}
	return 0
}

func (d *Dispatcher) promptTokens(c *gw.Ctx) int64 {
	if c == nil || c.Est == nil {
		return 0
	}
	return c.Est.Tokens.InputTokens
}

// unaryUsage prefers the upstream usage block. Missing, null or zero usage is
// estimated locally over the prompt and the produced text (TDS E-13). The split
// fields win over total_tokens (E-14).
func (d *Dispatcher) unaryUsage(c *gw.Ctx, resp *ir.Response) stream.Usage {
	prompt := d.promptTokens(c)
	if resp.Usage != nil && (resp.Usage.InputTokens > 0 || resp.Usage.OutputTokens > 0) {
		in := resp.Usage.InputTokens
		if in <= 0 {
			in = prompt
		}
		return stream.Usage{
			TokensIn: in, TokensOut: resp.Usage.OutputTokens,
			Source: stream.SourceUpstream, UpstreamOK: true,
		}
	}
	return stream.ResolveUsage(nil, prompt, collectResponseText(resp), d.opts.Tokens)
}

func collectResponseText(resp *ir.Response) string {
	if resp == nil {
		return ""
	}
	var b strings.Builder
	for _, ch := range resp.Choices {
		if ch.Message == nil {
			continue
		}
		if ch.Message.Content.Text != "" {
			b.WriteString(ch.Message.Content.Text)
		}
		for _, p := range ch.Message.Content.Parts {
			if p.Text != "" {
				b.WriteString(p.Text)
			}
		}
		for _, tc := range ch.Message.ToolCalls {
			if len(tc.Arguments) > 0 {
				b.Write(tc.Arguments)
			}
		}
	}
	return b.String()
}

// cost applies exactly one rounding point. The group ratio is applied to the
// unit prices; the long-context tier selects whole-request prices.
func (d *Dispatcher) cost(c *gw.Ctx, usage stream.Usage) (int64, error) {
	if c.Endpoint == gw.EndpointImages {
		perCall := c.Price.PerCallPriceMicro
		n := int64(c.IR.N)
		if n <= 0 {
			n = 1
		}
		amount, err := billing.ApplyRatioBp(perCall*n, c.Auth.GroupRatioBP)
		if err != nil {
			return 0, err
		}
		return amount, nil
	}
	result, err := billing.RequestCost(billing.CostParams{
		TokensIn:               usage.TokensIn,
		TokensOut:              usage.TokensOut,
		BaseInPriceMicroPerK:   c.Price.InPriceMicroPerK,
		BaseOutPriceMicroPerK:  c.Price.OutPriceMicroPerK,
		LongCtxThresholdTokens: c.Price.LongCtxThresholdTokens,
		LongCtxInPrice:         c.Price.LongCtxInPriceMicroPerK,
		LongCtxOutPrice:        c.Price.LongCtxOutPriceMicroPerK,
		GroupRatioBP:           c.Auth.GroupRatioBP,
		CacheBillingEnabled:    false,
	})
	if err != nil {
		return 0, err
	}
	return result.CostMicro, nil
}

func isChatEndpoint(endpoint gw.Endpoint) bool {
	return endpoint == gw.EndpointChat || endpoint == gw.EndpointMessages
}

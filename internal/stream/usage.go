package stream

import (
	"strings"

	"yunshu.dev/core/internal/billing"
	"yunshu.dev/relaykit/ir"
)

// Usage sources recorded in t_usage_record (TDS §4.2).
const (
	SourceUpstream = "UPSTREAM_STREAM"
	SourceEstimate = "LOCAL_ESTIMATE"
)

// Usage is the resolved accounting for one stream.
type Usage struct {
	TokensIn   int64
	TokensOut  int64
	Source     string
	UpstreamOK bool
}

// ResolveUsage prefers upstream usage and falls back to local estimation.
//
// It never returns a zero charge merely because the upstream omitted usage: a
// non-empty output text is always counted by the local tokenizer. The split
// fields win over total_tokens (TDS E-14), because only the split fields are
// attributable at reconciliation time.
func ResolveUsage(upstream *ir.Usage, promptTokens int64, outputText string, counter billing.InputCounter) Usage {
	if upstream != nil && (upstream.InputTokens > 0 || upstream.OutputTokens > 0) {
		in := upstream.InputTokens
		if in <= 0 {
			in = promptTokens
		}
		return Usage{
			TokensIn:   in,
			TokensOut:  upstream.OutputTokens,
			Source:     SourceUpstream,
			UpstreamOK: true,
		}
	}
	out := int64(0)
	if counter != nil && outputText != "" {
		if n, err := counter.CountInput(outputText); err == nil && n > 0 {
			out = n
		}
	}
	return Usage{TokensIn: promptTokens, TokensOut: out, Source: SourceEstimate}
}

// accumulatedOutput collects the text whose local token count is the fallback
// output charge. Tool-call arguments are included: the upstream produced them
// and they are billed as output.
type accumulatedOutput struct {
	b strings.Builder
}

func (a *accumulatedOutput) addText(text string) {
	if text != "" {
		a.b.WriteString(text)
	}
}

func (a *accumulatedOutput) addToolArguments(args string) {
	if args != "" {
		a.b.WriteString(args)
	}
}

func (a *accumulatedOutput) String() string { return a.b.String() }

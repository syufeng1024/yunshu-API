package dispatch

import (
	"time"

	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/jsonx"
)

// Fallback timeouts used only when neither the channel payload nor the
// configuration snapshot supplies a value. The channel payload is mandatory
// (D-14); these constants describe the documented platform defaults.
const (
	DefaultConnectTimeout   = 3 * time.Second
	DefaultFirstByteTimeout = 30 * time.Second
	DefaultTotalTimeout     = 50 * time.Second
	DefaultSettleReserve    = 15 * time.Second
	DefaultUnaryThreshold   = 65 * time.Second
	DefaultStreamThreshold  = 45 * time.Second
)

// connectTimeout returns the channel's connect timeout, falling back to
// gw.upstream_timeout_ms and then the 3s platform default.
func (d *Dispatcher) connectTimeout(ch *gw.ChannelEntry) time.Duration {
	if ch.TimeoutConnectMS > 0 {
		return time.Duration(ch.TimeoutConnectMS) * time.Millisecond
	}
	if d.opts.Config != nil {
		if v, ok := d.opts.Config.Int(config.KeyGwUpstreamTimeoutMs); ok && v > 0 {
			return time.Duration(v) * time.Millisecond
		}
	}
	return DefaultConnectTimeout
}

// firstByteTimeout returns the channel's first-byte timeout, falling back to
// gw.stream_first_byte_ms (30s per errata E-01).
func (d *Dispatcher) firstByteTimeout(ch *gw.ChannelEntry) time.Duration {
	if ch.TimeoutFirstByteMS > 0 {
		return time.Duration(ch.TimeoutFirstByteMS) * time.Millisecond
	}
	if d.opts.Config != nil {
		if v, ok := d.opts.Config.Int(config.KeyGwStreamFirstByteMs); ok && v > 0 {
			return time.Duration(v) * time.Millisecond
		}
	}
	return DefaultFirstByteTimeout
}

// totalTimeout returns the non-streaming per-attempt total. The platform value
// gw.upstream_timeout_ms (50s) is the single-attempt ceiling; a channel may
// lower it but not raise it, otherwise one attempt could consume the whole
// 120s request budget and leave no room for the documented retry.
func (d *Dispatcher) totalTimeout(ch *gw.ChannelEntry) time.Duration {
	global := DefaultTotalTimeout
	if d.opts.Config != nil {
		if v, ok := d.opts.Config.Int(config.KeyGwUpstreamTimeoutMs); ok && v > 0 {
			global = time.Duration(v) * time.Millisecond
		}
	}
	if ch.TimeoutTotalMS > 0 {
		channel := time.Duration(ch.TimeoutTotalMS) * time.Millisecond
		if channel < global {
			return channel
		}
	}
	return global
}

// streamTotalTimeout is the overall stream budget (P18/P22, 600s), not the
// channel's non-stream total.
func (d *Dispatcher) streamTotalTimeout() time.Duration {
	if d.opts.Config != nil {
		if v, ok := d.opts.Config.Int(config.KeyGwStreamTotalMs); ok && v > 0 {
			return time.Duration(v) * time.Millisecond
		}
	}
	return 600 * time.Second
}

// settleReserve is the tail kept free for settlement and logging.
func (d *Dispatcher) settleReserve() time.Duration {
	if d.opts.Config != nil {
		if v, ok := d.opts.Config.Int(config.KeyGwSettleReserveMs); ok && v > 0 {
			return time.Duration(v) * time.Millisecond
		}
	}
	return DefaultSettleReserve
}

// retryThreshold reads the two-value JSON key gw.retry_threshold_ms. The key is
// deliberately not an int: unary and streaming have different budgets.
func (d *Dispatcher) retryThreshold(stream bool) time.Duration {
	def := DefaultUnaryThreshold
	if stream {
		def = DefaultStreamThreshold
	}
	if d.opts.Config == nil {
		return def
	}
	v, ok := d.opts.Config.Value(config.KeyGwRetryThresholdMs)
	if !ok {
		return def
	}
	var pair struct {
		Unary  int64 `json:"unary"`
		Stream int64 `json:"stream"`
	}
	if err := jsonx.Unmarshal(v.Raw(), &pair); err != nil {
		return def
	}
	ms := pair.Unary
	if stream {
		ms = pair.Stream
	}
	if ms <= 0 {
		return def
	}
	return time.Duration(ms) * time.Millisecond
}

// maxAttempts is 1 + min(gw.retry_max, 1) and never exceeds MaxAttempts.
func (d *Dispatcher) maxAttempts(ch *gw.ChannelEntry) int {
	max := 1
	channelMax := 1
	if ch.RetryMax != nil && *ch.RetryMax >= 0 {
		channelMax = int(*ch.RetryMax)
	}
	if d.opts.Config != nil {
		if v, ok := d.opts.Config.Int(config.KeyGwRetryMax); ok && v >= 0 {
			channelMax = min(channelMax, int(v))
		}
	}
	max += min(channelMax, 1)
	if max > MaxAttempts {
		max = MaxAttempts
	}
	return max
}

package dispatch

import (
	"context"
	"strconv"
	"time"

	obslog "yunshu.dev/core/internal/obs/log"
)

// probeThreshold is the TDS §4.2 count: 20 consecutive streams without usage
// downgrade an UNKNOWN channel to ESTIMATE.
const probeThreshold = 20

// autoProbeEnabled defaults to true. E-14: the key is not one of the 45
// configuration keys, so the switch is injected by the constructor.
func (d *Dispatcher) autoProbeEnabled() bool {
	if d.opts.AutoProbeStreamUsage == nil {
		return true
	}
	return *d.opts.AutoProbeStreamUsage
}

// noteUsage implements the E-14 automatic downgrade. When the write-back
// interface is nil the channel is left unchanged and a WARN is emitted; the
// process never panics and never writes the database directly.
func (d *Dispatcher) noteUsage(ctx context.Context, channelID int64, sawUsage bool, mode string) {
	d.mu.Lock()
	state := d.probes[channelID]
	if state == nil {
		state = &probeState{}
		d.probes[channelID] = state
	}
	if sawUsage {
		state.misses = 0
		d.mu.Unlock()
		return
	}
	if mode != "UNKNOWN" || !d.autoProbeEnabled() {
		d.mu.Unlock()
		return
	}
	state.misses++
	misses := state.misses
	already := state.downgraded
	if misses >= probeThreshold {
		state.downgraded = true
		state.misses = 0
	}
	d.mu.Unlock()

	if misses < probeThreshold || already {
		return
	}
	if d.opts.UsageModeWriter == nil {
		if d.opts.Logger != nil {
			d.opts.Logger.Warn(ctx, "stream_usage_auto_downgrade_skipped: writer is nil channel="+strconv.FormatInt(channelID, 10), obslog.Fields{
				Stage: "P25",
			})
		}
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(contextOrBackground(ctx)), 2*time.Second)
	defer cancel()
	if err := d.opts.UsageModeWriter.SetStreamUsageMode(writeCtx, channelID, "ESTIMATE"); err != nil {
		if d.opts.Logger != nil {
			d.opts.Logger.Warn(ctx, "stream_usage_auto_downgrade_failed channel="+strconv.FormatInt(channelID, 10)+": "+err.Error(), obslog.Fields{
				Stage: "P25",
			})
		}
		return
	}
	if d.opts.Logger != nil {
		d.opts.Logger.Info(ctx, "stream_usage_auto_downgraded channel="+strconv.FormatInt(channelID, 10), obslog.Fields{
			Stage: "P25",
		})
	}
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

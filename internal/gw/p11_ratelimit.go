package gw

import (
	"context"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/config"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/ratelimit"
)

// RateLimitStage performs P11. Its execution slot is after P12 because the
// daily-quota dimension needs est_micro (see Pipeline.Run).
type RateLimitStage struct{ d *Deps }

// NewRateLimitStage builds P11.
func NewRateLimitStage(d *Deps) *RateLimitStage { return &RateLimitStage{d: d} }

// Name implements Stage.
func (s *RateLimitStage) Name() string { return StageP11 }

// Run implements Stage.
func (s *RateLimitStage) Run(ctx context.Context, c *Ctx) error {
	if c.Est == nil || c.Auth == nil {
		return StageErr(StageP11, apierr.New(apierr.CodeInternalError))
	}
	nowMS := s.d.ClockNowMS()
	minEpoch := minEpochOf(nowMS)
	leaseMS := s.leaseMS(c.Stream)

	estTokens := ratelimit.PlaceholderTokens(c.Est.Tokens.InputTokens, c.Est.Tokens.OutputTokens, 0)
	verdict, err := s.d.Limiter.Acquire(ctx, ratelimit.AcquireInput{
		KeyID:     c.Auth.KeyID,
		UserID:    c.Auth.UserID,
		NowMS:     nowMS,
		MinEpoch:  minEpoch,
		RequestID: c.RequestID,
		LeaseMS:   leaseMS,
		EstTokens: estTokens,
		EstMicro:  c.Est.EstMicro,
		Limits: ratelimit.Limits{
			RPM:             c.Limits.RPM,
			TPM:             c.Limits.TPM,
			Concurrency:     c.Limits.Concurrency,
			DailyQuotaMicro: c.Limits.DailyQuotaMicro,
			DailyEnabled:    c.Limits.DailyEnabled,
		},
	})
	if err != nil {
		if s.d.Logger != nil {
			s.d.Logger.Error(ctx, "rate_limiter_unavailable: "+err.Error(), obslog.Fields{Stage: "P11"})
		}
		return StageErr(StageP11, apierr.New(apierr.CodeRateLimiterUnavailable).WithCause(err))
	}

	if !verdict.Allowed {
		now := time.UnixMilli(nowMS)
		apiErr := apierr.FromVerdictAt(verdict, now)
		if snap, ok := s.headerSnapshot(ctx, c, nowMS); ok {
			apiErr = apiErr.WithRateLimitSnapshot(snap)
		}
		if verdict.OutwardCode() == ratelimit.CodeDailyQuota {
			// K5 is display-only and outside L1's atomic boundary. A failure
			// must not change the 429.
			_ = s.d.Limiter.RecordDailyRejection(ctx, c.Auth.UserID, ratelimit.Day(now))
		}
		return StageErr(StageP11, apiErr)
	}

	c.Verdict = &verdict
	c.AcquiredMin = minEpoch
	c.LeaseMS = leaseMS
	c.RateAcquired = true
	return nil
}

// headerSnapshot fills BOTH header groups required by PRD §3.3. The verdict
// only describes the main (latest-recovering) dimension; reading the two
// counters gives the other group without inventing numbers.
func (s *RateLimitStage) headerSnapshot(ctx context.Context, c *Ctx, nowMS int64) (apierr.RateLimitSnapshot, bool) {
	if s.d.CounterReader == nil {
		return apierr.RateLimitSnapshot{}, false
	}
	snap := apierr.RateLimitSnapshot{
		RPMLimit:     c.Limits.RPM,
		RPMResetUnix: (minEpochOf(nowMS) + 1) * 60,
	}
	if snap.RPMLimit > 0 {
		used := s.d.CounterReader.UsedRPM(ctx, c.Auth.KeyID, minEpochOf(nowMS))
		snap.RPMRemaining = nonNegativeInt(snap.RPMLimit - used)
	}
	if c.Limits.DailyEnabled && c.Limits.DailyQuotaMicro > 0 {
		snap.DailyLimit = c.Limits.DailyQuotaMicro
		day := ratelimit.Day(time.UnixMilli(nowMS))
		used := s.d.CounterReader.UsedDaily(ctx, c.Auth.UserID, day)
		snap.DailyRemaining = nonNegativeInt(snap.DailyLimit - used)
	}
	return snap, snap.RPMLimit > 0 || snap.DailyLimit > 0
}

func (s *RateLimitStage) leaseMS(stream bool) int64 {
	key := config.KeyGwClientTimeoutBudget
	def := int64(120_000)
	if stream {
		key = config.KeyGwStreamTotalMs
		def = 600_000
	}
	if s.d.Config != nil {
		if v, ok := s.d.Config.Int(key); ok && v > 0 {
			return v
		}
	}
	return def
}

func minEpochOf(nowMS int64) int64 { return nowMS / 60_000 }

func nonNegativeInt(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

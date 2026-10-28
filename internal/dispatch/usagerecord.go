package dispatch

import (
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/settle"
)

// emitUsage writes exactly one t_usage_record row for a terminal call (P28).
// It is called from a deferred function in Serve so every return path,
// including the ones that never opened an upstream connection, produces one
// row. A nil sink means T08 behaviour: no log rows, no error.
func (d *Dispatcher) emitUsage(c *gw.Ctx, out outcome, started time.Time) {
	if d == nil || d.opts.UsageSink == nil || c == nil || c.Auth == nil || c.RequestID == "" {
		return
	}

	status := 200
	switch {
	case out.clientAborted:
		status = apierr.New(apierr.CodeClientAborted).HTTPStatus
	case out.apiErr != nil:
		status = out.apiErr.HTTPStatus
	case !out.finished:
		status = 503
	}

	result := out.result
	if result == "" {
		if out.apiErr != nil || out.clientAborted {
			result = "FAILED"
		} else {
			result = "SUCCESS"
		}
	}

	bizCode := ""
	if out.apiErr != nil {
		bizCode = out.apiErr.Code
	}
	failStage, failOwner := attribution(out)

	modelID, modelName, requestModel := int64(0), "", ""
	if c.Model != nil {
		modelID = c.Model.ID
		modelName = c.Model.ModelName
	}
	if c.IR != nil {
		requestModel = c.IR.Model
	}
	if requestModel == "" {
		requestModel = modelName
	}

	var (
		priceRuleID   int64
		groupCode     = "standard"
		priceRatioBP  = int64(10000)
		clientIP      []byte
		userAgent     string
		channelID     int64
		channelName   string
		outboundProto string
		routeID       int64
	)
	if c.Price != nil {
		priceRuleID = c.Price.ID
	}
	if c.Auth.GroupCode != "" {
		groupCode = c.Auth.GroupCode
	}
	if c.Auth.GroupRatioBP > 0 {
		priceRatioBP = c.Auth.GroupRatioBP
	}
	if c.ClientIP.IsValid() {
		clientIP = c.ClientIP.AsSlice()
	}
	if c.Header != nil {
		userAgent = c.Header.Get("User-Agent")
	}
	if out.attempt.ChannelID != 0 {
		channelID = out.attempt.ChannelID
		channelName = out.attempt.ChannelNameSnap
		outboundProto = out.attempt.ProtocolCode
		routeID = out.attempt.RouteID
	}

	createdAt := c.StartedAt
	if createdAt.IsZero() {
		createdAt = started
	}

	tokensIn, tokensOut := out.tokenIn, out.tokenOut
	if tokensIn == 0 && out.attempt.TokensIn != 0 {
		tokensIn = out.attempt.TokensIn
	}
	if tokensOut == 0 && out.attempt.TokensOut != 0 {
		tokensOut = out.attempt.TokensOut
	}

	usageSource := out.usageSource
	if usageSource == "" {
		usageSource = "NONE"
	}
	var ttft *int64
	if c.Stream && out.attempt.FirstByteMS > 0 {
		v := out.attempt.FirstByteMS
		ttft = &v
	}
	refunded := int64(0)
	if c.Hold != nil {
		refunded = c.HoldMicro - out.consumedMicro
	}

	d.opts.UsageSink(settle.UsageRecord{
		RequestID:        c.RequestID,
		UserID:           c.Auth.UserID,
		APIKeyID:         c.Auth.KeyID,
		APIKeyName:       c.Auth.KeyName,
		ModelID:          modelID,
		ModelName:        modelName,
		RequestModel:     requestModel,
		InboundProto:     string(c.Protocol),
		ChannelID:        channelID,
		ChannelName:      channelName,
		OutboundProto:    outboundProto,
		RouteID:          routeID,
		PriceRuleID:      priceRuleID,
		GroupCode:        groupCode,
		PriceRatioBP:     priceRatioBP,
		BillingTier:      "BASE",
		IsStream:         c.Stream,
		HTTPStatus:       status,
		BizCode:          bizCode,
		Result:           result,
		FailStage:        failStage,
		FailOwner:        failOwner,
		TokensIn:         tokensIn,
		TokensOut:        tokensOut,
		UsageSource:      usageSource,
		PreholdMicro:     c.HoldMicro,
		ConsumedMicro:    out.consumedMicro,
		RefundedMicro:    refunded,
		UpstreamAttempts: int64(out.attempts),
		TTFTMS:           ttft,
		TotalMS:          time.Since(started).Milliseconds(),
		UpstreamMS:       out.upstreamMS,
		ClientIP:         clientIP,
		UserAgent:        userAgent,
		ClientAborted:    out.clientAborted,
		CreatedAt:        createdAt,
		// The body is handed over by reference; WriteUsage moves it to its own
		// queue and the async flush seals it. No cryptobox call happens here.
		RawBody: c.RawBody,
	})

	if d.opts.MetricSink != nil {
		ok, failed := int64(0), int64(1)
		if result == "SUCCESS" {
			ok, failed = 1, 0
		}
		base := settle.MetricDelta{
			Requests:      1,
			Success:       ok,
			Failed:        failed,
			TokensIn:      tokensIn,
			TokensOut:     tokensOut,
			ConsumedMicro: out.consumedMicro,
		}
		userDelta := base
		userDelta.ScopeType = "USER"
		userDelta.ScopeID = c.Auth.UserID
		userDelta.ChannelID = channelID
		d.opts.MetricSink(userDelta)

		keyDelta := base
		keyDelta.ScopeType = "KEY"
		keyDelta.ScopeID = c.Auth.KeyID
		d.opts.MetricSink(keyDelta)
	}
}

// attribution maps the terminal error to the two T-35 columns. A successful
// row leaves both NULL.
func attribution(out outcome) (stage, owner string) {
	if out.clientAborted {
		return "CLIENT_ABORT", "CLIENT"
	}
	if out.apiErr == nil {
		return "", ""
	}
	switch out.apiErr.Code {
	case apierr.CodeSettlementUnavailable:
		return "SETTLE", "PLATFORM"
	case apierr.CodeNoAvailableUpstream, apierr.CodeNoAvailableUpstreamForGroup,
		apierr.CodeConfigUnavailable:
		return "ROUTE", "PLATFORM"
	case apierr.CodeConvertFailed:
		return "CONVERT", "PLATFORM"
	case apierr.CodeClientAborted:
		return "CLIENT_ABORT", "CLIENT"
	default:
		return "UPSTREAM", "UPSTREAM"
	}
}

package settle

import (
	"strconv"
	"time"

	obslog "yunshu.dev/core/internal/obs/log"
)

// requestLogRecord maps a t_usage_record row to the request-level log line
// (T22 §12.2). trace_id and request_id are the same value by design: the
// gateway writes the resolved entry id into Ctx.RequestID, and the settle row
// carries it forward unchanged.
func requestLogRecord(r UsageRecord, created time.Time) obslog.RequestRecord {
	return obslog.RequestRecord{
		TraceID:         r.RequestID,
		SpanID:          "",
		RequestID:       r.RequestID,
		UserID:          logID(r.UserID),
		KeyID:           logID(r.APIKeyID),
		ModelRequested:  r.RequestModel,
		ModelResolved:   r.ModelName,
		ProtocolIn:      r.InboundProto,
		ProtocolOut:     r.OutboundProto,
		ChannelID:       logID(r.ChannelID),
		Stream:          r.IsStream,
		BizDate:         obslog.BizDate(created),
		Stage:           obslog.StageOf(r.FailStage),
		ErrCode:         r.BizCode,
		HTTPStatus:      r.HTTPStatus,
		InTokens:        r.TokensIn,
		OutTokens:       r.TokensOut,
		UsageSource:     r.UsageSource,
		PlatformInEst:   r.TokensInEst,
		PlatformOutEst:  r.TokensOutEst,
		CreditsPreheld:  r.PreholdMicro,
		CreditsSettled:  r.ConsumedMicro,
		CreditsRefunded: r.RefundedMicro,
		AttemptCount:    r.UpstreamAttempts,
		TTFTMS:          r.TTFTMS,
		LatencyMS:       r.TotalMS,
		UpstreamMS:      r.UpstreamMS,
		ConfigVersion:   r.ConfigVersion,
		GroupCode:       r.GroupCode,
		DegradeFlags:    []string{},
	}
}

// attemptLogRecord maps a t_upstream_attempt row to the attempt-level log line.
// HTTPStatus, ConnectMS and FirstByteMS are pointers because the row stores
// NULL for "not observed"; a 0 would be a fake measurement.
func attemptLogRecord(r AttemptRecord) obslog.AttemptRecord {
	return obslog.AttemptRecord{
		TraceID:       r.RequestID,
		SpanID:        "",
		RequestID:     r.RequestID,
		UserID:        logID(r.UserID),
		AttemptNo:     r.AttemptIndex,
		ChannelID:     logID(r.ChannelID),
		ChannelName:   r.ChannelName,
		UpstreamModel: r.UpstreamModel,
		ProtocolOut:   r.ProtocolCode,
		Phase:         r.Phase,
		Outcome:       r.Result,
		HTTPStatus:    positiveInt64(int64(r.HTTPStatus)),
		UpstreamCode:  r.UpstreamCode,
		ConnectMS:     positiveInt64(r.ConnectMS),
		FirstByteMS:   positiveInt64(r.FirstByteMS),
		TotalMS:       r.TotalMS,
		InTokens:      r.TokensIn,
		OutTokens:     r.TokensOut,
		ErrorClass:    r.ErrorClass,
	}
}

func logID(v int64) string {
	if v <= 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}

func positiveInt64(v int64) *int64 {
	if v <= 0 {
		return nil
	}
	return &v
}

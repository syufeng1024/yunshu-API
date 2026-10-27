package logq

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
)

// detailView is one request's metadata. It carries snapshot columns only; the
// model/channel/key names are whatever was true when the request was served,
// not what the master tables say today.
type detailView struct {
	RequestID             string    `json:"request_id"`
	APIKeyID              int64     `json:"api_key_id"`
	APIKeyName            string    `json:"api_key_name"`
	ModelID               *int64    `json:"model_id"`
	ModelName             string    `json:"model_name"`
	RequestModel          string    `json:"request_model"`
	InboundProtocol       string    `json:"inbound_protocol"`
	ChannelID             *int64    `json:"channel_id"`
	ChannelName           *string   `json:"channel_name"`
	OutboundProtocol      *string   `json:"outbound_protocol"`
	GroupCode             string    `json:"group_code"`
	IsStream              bool      `json:"is_stream"`
	HTTPStatus            int64     `json:"http_status"`
	BizCode               string    `json:"biz_code"`
	Result                string    `json:"result"`
	FailStage             *string   `json:"fail_stage"`
	FailOwner             *string   `json:"fail_owner"`
	TokensIn              int64     `json:"tokens_in"`
	TokensOut             int64     `json:"tokens_out"`
	PreholdMicro          int64     `json:"prehold_micro"`
	ConsumedMicro         int64     `json:"consumed_micro"`
	ConsumedRechargeMicro int64     `json:"consumed_recharge_micro"`
	ConsumedBonusMicro    int64     `json:"consumed_bonus_micro"`
	UpstreamAttempts      int64     `json:"upstream_attempts"`
	TTFTMs                *int64    `json:"ttft_ms"`
	TotalMs               int64     `json:"total_ms"`
	UpstreamMs            int64     `json:"upstream_ms"`
	ClientIP              string    `json:"client_ip"`
	ClientAborted         bool      `json:"client_aborted"`
	FullLogSaved          bool      `json:"full_log_saved"`
	CreatedAt             time.Time `json:"created_at"`
}

// attemptView is one row of the retry timeline (t_upstream_attempt). Ordering
// is attempt_index ascending, so attempt 1 is the first upstream try.
type attemptView struct {
	AttemptIndex  int64     `json:"attempt_index"`
	Result        string    `json:"result"`
	HTTPStatus    *int64    `json:"http_status"`
	Phase         string    `json:"phase"`
	ChannelID     int64     `json:"channel_id"`
	ChannelName   string    `json:"channel_name"`
	ProtocolCode  string    `json:"protocol_code"`
	UpstreamModel string    `json:"upstream_model"`
	ConnectMs     *int64    `json:"connect_ms"`
	FirstByteMs   *int64    `json:"first_byte_ms"`
	TotalMs       int64     `json:"total_ms"`
	TokensIn      int64     `json:"tokens_in"`
	TokensOut     int64     `json:"tokens_out"`
	IsRetryable   bool      `json:"is_retryable"`
	RetryDecision *string   `json:"retry_decision"`
	ErrorClass    *string   `json:"error_class"`
	UpstreamCode  *string   `json:"upstream_code"`
	UpstreamMsg   *string   `json:"upstream_msg"`
	CreatedAt     time.Time `json:"created_at"`
}

type detailResponse struct {
	Request  detailView    `json:"request"`
	Timeline []attemptView `json:"timeline"`
}

// Detail implements GET /api/console/logs/{request_id}.
//
// The lookup is by request_id AND user_id. There is no 365-day predicate
// here: the online window bounds a range query, while a request_id is an
// exact key. A metadata row still inside its retention answers even at 366
// days (A7); the body endpoint applies its own 180-day rule.
func (s *Service) Detail(w http.ResponseWriter, r *http.Request) {
	uid, apiErr := sessionUID(r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	requestID := r.PathValue("request_id")
	ctx := r.Context()

	req, found, apiErr := s.loadRequest(ctx, uid, requestID)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	if !found {
		mw.WriteError(w, r, requestNotFound())
		return
	}

	timeline, apiErr := s.loadTimeline(ctx, uid, requestID)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	_ = mw.WriteJSON(w, http.StatusOK, detailResponse{Request: req, Timeline: timeline})
}

// loadRequest reads the metadata row. user_id is in the WHERE clause, so a
// row owned by another account is reported exactly like an absent one.
func (s *Service) loadRequest(ctx context.Context, uid int64, requestID string) (detailView, bool, *apierr.APIError) {
	var (
		v         detailView
		modelID   sql.NullInt64
		channelID sql.NullInt64
		channel   sql.NullString
		outbound  sql.NullString
		failStage sql.NullString
		failOwner sql.NullString
		ttft      sql.NullInt64
	)
	err := s.logs().QueryRowContext(ctx, `
		SELECT request_id, api_key_id, api_key_name_snap, model_id,
		       model_name_snap, request_model_req, inbound_protocol,
		       channel_id, channel_name_snap, outbound_protocol,
		       group_code_snap, is_stream, http_status, biz_code, result,
		       fail_stage, fail_owner, tokens_in, tokens_out,
		       prehold_micro, consumed_micro, consumed_recharge_micro,
		       consumed_bonus_micro, upstream_attempts, ttft_ms, total_ms,
		       upstream_ms, COALESCE(INET6_NTOA(client_ip), ''),
		       client_aborted, full_log_saved, created_at
		  FROM t_usage_record
		 WHERE request_id = ? AND user_id = ?
		 LIMIT 1`, requestID, uid).Scan(
		&v.RequestID, &v.APIKeyID, &v.APIKeyName, &modelID,
		&v.ModelName, &v.RequestModel, &v.InboundProtocol,
		&channelID, &channel, &outbound,
		&v.GroupCode, &v.IsStream, &v.HTTPStatus, &v.BizCode, &v.Result,
		&failStage, &failOwner, &v.TokensIn, &v.TokensOut,
		&v.PreholdMicro, &v.ConsumedMicro, &v.ConsumedRechargeMicro,
		&v.ConsumedBonusMicro, &v.UpstreamAttempts, &ttft, &v.TotalMs,
		&v.UpstreamMs, &v.ClientIP, &v.ClientAborted, &v.FullLogSaved,
		&v.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return detailView{}, false, nil
	}
	if err != nil {
		s.logError(ctx, "logq_detail_query_failed: "+err.Error())
		return detailView{}, false, apierr.New(apierr.CodeInternalError)
	}
	v.ModelID = nullInt64Ptr(modelID)
	v.ChannelID = nullInt64Ptr(channelID)
	v.ChannelName = nullStringPtr(channel)
	v.OutboundProtocol = nullStringPtr(outbound)
	v.FailStage = nullStringPtr(failStage)
	v.FailOwner = nullStringPtr(failOwner)
	v.TTFTMs = nullInt64Ptr(ttft)
	v.CreatedAt = v.CreatedAt.UTC()
	return v, true, nil
}

// loadTimeline returns every upstream attempt, ordered by attempt_index. A
// retried request therefore shows two rows even when only the second one
// produced a body (T14 A6).
func (s *Service) loadTimeline(ctx context.Context, uid int64, requestID string) ([]attemptView, *apierr.APIError) {
	rows, err := s.logs().QueryContext(ctx, `
		SELECT attempt_index, result, http_status, phase, channel_id,
		       channel_name_snap, protocol_code, upstream_model,
		       connect_ms, first_byte_ms, total_ms, tokens_in, tokens_out,
		       is_retryable, retry_decision, error_class, upstream_code,
		       upstream_msg, created_at
		  FROM t_upstream_attempt
		 WHERE request_id = ? AND user_id = ?
		 ORDER BY attempt_index ASC`, requestID, uid)
	if err != nil {
		s.logError(ctx, "logq_timeline_query_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeInternalError)
	}
	defer rows.Close()

	// Initialised to an empty slice: an API consumer distinguishes "no
	// attempts" from a missing field, and null would make it guess.
	out := make([]attemptView, 0, 2)
	for rows.Next() {
		var (
			v         attemptView
			httpCode  sql.NullInt64
			connect   sql.NullInt64
			firstByte sql.NullInt64
			decision  sql.NullString
			errClass  sql.NullString
			upCode    sql.NullString
			upMsg     sql.NullString
		)
		if err := rows.Scan(
			&v.AttemptIndex, &v.Result, &httpCode, &v.Phase, &v.ChannelID,
			&v.ChannelName, &v.ProtocolCode, &v.UpstreamModel,
			&connect, &firstByte, &v.TotalMs, &v.TokensIn, &v.TokensOut,
			&v.IsRetryable, &decision, &errClass, &upCode,
			&upMsg, &v.CreatedAt,
		); err != nil {
			s.logError(ctx, "logq_timeline_scan_failed: "+err.Error())
			return nil, apierr.New(apierr.CodeInternalError)
		}
		v.HTTPStatus = nullInt64Ptr(httpCode)
		v.ConnectMs = nullInt64Ptr(connect)
		v.FirstByteMs = nullInt64Ptr(firstByte)
		v.RetryDecision = nullStringPtr(decision)
		v.ErrorClass = nullStringPtr(errClass)
		v.UpstreamCode = nullStringPtr(upCode)
		v.UpstreamMsg = nullStringPtr(upMsg)
		v.CreatedAt = v.CreatedAt.UTC()
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		s.logError(ctx, "logq_timeline_rows_failed: "+err.Error())
		return nil, apierr.New(apierr.CodeInternalError)
	}
	return out, nil
}

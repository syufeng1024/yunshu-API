// Package mw holds the control-plane middleware: session, authorization,
// panic recovery, error mapping and pagination.
package mw

import (
	"net/http"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/jsonx"
	obslog "yunshu.dev/core/internal/obs/log"
)

// WriteError writes an apierr body with the request id already attached by the
// httpsrv middleware chain. It is the only error writer for control handlers.
func WriteError(w http.ResponseWriter, r *http.Request, e *apierr.APIError) {
	if e == nil {
		e = apierr.New(apierr.CodeInternalError)
	}
	if rid := obslog.MetaFrom(r.Context()).RequestID; rid != "" {
		e = e.WithRequestID(rid)
	}
	_ = apierr.Write(w, e)
}

// WriteJSON writes a success body with the control-plane headers.
func WriteJSON(w http.ResponseWriter, status int, body any) error {
	payload, err := jsonx.Marshal(body)
	if err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, err = w.Write(payload)
	return err
}

// SessionMissing is the 401 used when the sid cookie is absent, malformed or
// no longer present in K20.
//
// Erratum E-21 added `session_expired` for exactly this condition. Using
// invalid_api_key (the T11 transitional mapping) told a console user their API
// key was wrong when the correct action is "log in again".
func SessionMissing() *apierr.APIError { return apierr.New(apierr.CodeSessionExpired) }

// PermissionDenied is the 403 for an authenticated principal whose role lacks
// the route's declared permission.
func PermissionDenied() *apierr.APIError { return apierr.New(apierr.CodePermissionDenied) }

// InvalidField maps a bad body field to invalid_request_body. The field path
// travels in the message so the client can point at the exact member.
//
// This is a body-shape exit only. Use it when correcting the named field would
// make the request succeed: a missing confirm, a wrong type, a count outside
// its 1–10 range, an expire_at in the past. A refusal the caller cannot fix by
// editing the body — a quota ceiling, a lifecycle state, an entitlement already
// consumed — must use its own code (apierr.CodeKeyLimitReached,
// apierr.CodeResourceStateConflict, apierr.CodeTrialAlreadyClaimed,
// apierr.CodeInvoiceAlreadyIssued), because pointing at a field that is not the
// problem (and cannot be the fix) sends users to edit the wrong thing.
func InvalidField(path string) *apierr.APIError {
	return apierr.Newf(apierr.CodeInvalidRequestBody, path)
}

// InvalidQueryField maps a bad query parameter to invalid_query_parameter
// (errata E-21). Body validation and URL validation are separate codes so a
// client can tell "fix the JSON" from "fix the URL".
func InvalidQueryField(name string) *apierr.APIError {
	return apierr.Newf(apierr.CodeInvalidQueryParameter, name)
}

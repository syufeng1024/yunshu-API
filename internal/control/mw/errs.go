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
// E.1 has no session-specific code. invalid_api_key is the only 401
// authentication_error available, so it is reused rather than inventing a
// 66th code; the receipt records a proposed `session_expired` code for the
// next PRD/TDS revision. account_disabled is deliberately not used: that is
// "the platform disabled this account", which is a different condition.
func SessionMissing() *apierr.APIError { return apierr.New(apierr.CodeInvalidAPIKey) }

// PermissionDenied is the 403 for an authenticated principal whose role lacks
// the route's declared permission.
func PermissionDenied() *apierr.APIError { return apierr.New(apierr.CodePermissionDenied) }

// InvalidField maps a bad query/body field to the closest existing 400 code.
// E.1 has no invalid_query_parameter; invalid_request_body is the nearest
// invalid_request_error and carries the field path in its message.
func InvalidField(path string) *apierr.APIError {
	return apierr.Newf(apierr.CodeInvalidRequestBody, path)
}

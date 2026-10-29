// Package console implements account-console endpoints that call the data
// plane on the user's behalf. It holds no billing code: the gateway pipeline
// (P10 prehold, P14 settlement, t_usage_record) remains the only money path.
package console

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/control/mw"
	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/jsonx"
	obslog "yunshu.dev/core/internal/obs/log"
	"yunshu.dev/core/internal/platform/clock"
)

const (
	// MinSecretBytes is the shortest INTERNAL_CALL_SECRET accepted. It mirrors
	// the gateway's check; a shorter value leaves the relay disabled.
	MinSecretBytes = gw.InternalMinSecretBytes

	// maxEnvelopeBytes bounds the console request. It is the data-plane body
	// ceiling plus room for the {"key_id","body"} envelope, so anything the
	// gateway would accept fits.
	maxEnvelopeBytes = gw.MaxBodyBytes + 64*1024

	// relayChunkBytes is one read/write step of the SSE relay. Frames are often
	// smaller than this; each write is flushed immediately regardless of size.
	relayChunkBytes = 32 * 1024

	// responseHeaderTimeout must exceed the gateway's non-stream P01 budget
	// (default 120s) so controld never times out a request the gateway is still
	// working on.
	responseHeaderTimeout = 130 * time.Second
)

// Deps is the playground wiring. GatewayURL and Secret come from
// GATEWAY_INTERNAL_URL and INTERNAL_CALL_SECRET; either being unusable turns
// every call into 503 config_unavailable rather than a degraded mode.
type Deps struct {
	DB         *sql.DB
	GatewayURL string
	Secret     string
	Client     *http.Client
	Logger     *obslog.Logger
	Now        clock.Clock
}

// Service proxies one console call per request.
type Service struct {
	db       *sql.DB
	endpoint string
	secret   []byte
	client   *http.Client
	logger   *obslog.Logger
	now      clock.Clock

	// disabled names why the relay is off. It is logged once at startup; the
	// request path only needs to know that it is non-empty.
	disabled string
}

// New builds the service. It never returns nil: a misconfigured relay must
// answer 503 from the registered route, not vanish into a nil handler.
func New(d Deps) *Service {
	s := &Service{
		db:     d.DB,
		client: d.Client,
		logger: d.Logger,
		now:    d.Now,
	}
	if s.now == nil {
		s.now = clock.Real{}
	}
	if s.client == nil {
		s.client = newRelayClient()
	}

	raw := strings.TrimSpace(d.GatewayURL)
	secret := strings.TrimSpace(d.Secret)
	switch {
	case len(secret) < MinSecretBytes:
		s.disabled = "INTERNAL_CALL_SECRET missing or shorter than 32 bytes"
	case raw == "":
		s.disabled = "GATEWAY_INTERNAL_URL missing"
	default:
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			s.disabled = "GATEWAY_INTERNAL_URL is not an absolute http(s) URL"
			break
		}
		s.endpoint = strings.TrimRight(raw, "/") + gw.InternalPath
		s.secret = []byte(secret)
	}
	if s.disabled != "" && s.logger != nil {
		s.logger.Warn(context.TODO(),
			"playground relay disabled: "+s.disabled+"; /api/console/playground/chat will answer 503 config_unavailable",
			obslog.Fields{Stage: "console"})
	}
	return s
}

func newRelayClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		// No proxy: the target is the gateway's compose service name. An
		// environment proxy would either break the call or route key metadata
		// through a third party.
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		// The gateway answers identity; refusing transport compression keeps
		// the SSE bytes opaque end to end.
		DisableCompression: true,
	}}
}

// chatEnvelope is the console-side request. Body is forwarded byte for byte to
// the gateway, where P04 is the only validator (T27 §8 ruling 2).
type chatEnvelope struct {
	KeyID int64            `json:"key_id"`
	Body  jsonx.RawMessage `json:"body"`
}

// keyRow is the DB-authoritative view of one key. The gateway has no main pool,
// so these values are what controld signs; they are read fresh on every call
// rather than cached, so a disable or group change applies immediately.
type keyRow struct {
	name    string
	status  string
	group   string
	ratioBP sql.NullInt64
	expire  sql.NullTime
	ipm     int64
}

// Chat implements POST /api/console/playground/chat.
func (s *Service) Chat(w http.ResponseWriter, r *http.Request) {
	sess, ok := mw.SessionFrom(r.Context())
	if !ok {
		mw.WriteError(w, r, mw.SessionMissing())
		return
	}

	// controld's listener has a 30s WriteTimeout. A stream (and a slow
	// non-stream upstream) must outlive it, so the deadline is cleared for this
	// request only. Every other control route keeps the server-level cap.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	if s.disabled != "" {
		mw.WriteError(w, r, apierr.New(apierr.CodeConfigUnavailable))
		return
	}

	env, apiErr := readEnvelope(w, r)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	row, apiErr := s.loadKey(r.Context(), sess.UID, env.KeyID)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}
	claims, apiErr := s.claims(row, sess.UID, env.KeyID)
	if apiErr != nil {
		mw.WriteError(w, r, apiErr)
		return
	}

	rid := obslog.MetaFrom(r.Context()).RequestID
	if rid == "" {
		rid = apierr.ResolveRequestID(r.Header.Get("X-Request-Id"), "")
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.endpoint, bytes.NewReader(env.Body))
	if err != nil {
		s.logError(r.Context(), "playground_request_build_failed: "+err.Error(), rid)
		mw.WriteError(w, r, apierr.New(apierr.CodeConfigUnavailable))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("X-Request-Id", rid)
	if idem := strings.TrimSpace(r.Header.Get("Idempotency-Key")); idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	if err := gw.SetInternalHeaders(req.Header, s.secret, s.now.Now().UTC().Unix(), env.Body, claims); err != nil {
		// A claim the gateway would reject is a data fault on this side (a key
		// name with a newline, a broken group code). Never send a request that
		// is guaranteed to fail and log an opaque 401.
		s.logError(r.Context(), "playground_claims_invalid: "+err.Error(), rid)
		mw.WriteError(w, r, apierr.New(apierr.CodeConfigUnavailable))
		return
	}

	resp, err := s.client.Do(req)
	if err != nil {
		if r.Context().Err() != nil {
			// The browser went away; there is no one to write to.
			s.logWarn(r.Context(), "playground_client_aborted", rid)
			return
		}
		s.logError(r.Context(), "playground_gateway_unreachable: "+err.Error(), rid)
		mw.WriteError(w, r, apierr.Newf(apierr.CodeUpstreamAllFailed, 0).WithCause(err))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if err := relay(w, resp); err != nil && r.Context().Err() == nil {
		// Headers are already committed; only the log can report a broken
		// stream. A client disconnect lands here too and is not an error.
		s.logWarn(r.Context(), "playground_relay_interrupted: "+err.Error(), rid)
	}
}

// readEnvelope reads and parses the console request. It buffers the envelope
// with io.Copy into a bounded buffer because jsonx intentionally exposes no
// streaming decoder; the response side is the one that must not buffer.
func readEnvelope(w http.ResponseWriter, r *http.Request) (chatEnvelope, *apierr.APIError) {
	var buf bytes.Buffer
	limited := http.MaxBytesReader(w, r.Body, maxEnvelopeBytes)
	defer func() { _ = limited.Close() }()
	if _, err := io.Copy(&buf, limited); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return chatEnvelope{}, apierr.New(apierr.CodeRequestTooLarge)
		}
		return chatEnvelope{}, apierr.Newf(apierr.CodeInvalidRequestBody, "读取请求体失败").WithCause(err)
	}

	var env chatEnvelope
	if err := jsonx.Unmarshal(buf.Bytes(), &env); err != nil {
		return chatEnvelope{}, apierr.Newf(apierr.CodeInvalidRequestBody, "无法解析为 JSON 对象").WithCause(err)
	}
	if env.KeyID <= 0 {
		return chatEnvelope{}, mw.InvalidField("key_id")
	}
	body := bytes.TrimSpace(env.Body)
	if len(body) == 0 {
		return chatEnvelope{}, mw.InvalidField("body")
	}
	env.Body = append(jsonx.RawMessage(nil), body...)
	return env, nil
}

// loadKey resolves an owned, non-deleted key. The user_id predicate is part of
// the WHERE clause, so another account's key is indistinguishable from a
// nonexistent one (404 both ways).
func (s *Service) loadKey(ctx context.Context, uid, keyID int64) (keyRow, *apierr.APIError) {
	var row keyRow
	if s.db == nil {
		return row, apierr.New(apierr.CodeInternalError)
	}
	err := s.db.QueryRowContext(ctx, `
		SELECT k.key_name, k.status, k.group_code, g.ratio_bp, k.expire_at,
		       COALESCE((SELECT MAX(r.id) FROM t_api_key_ip_rule r WHERE r.api_key_id = k.id), 0)
		  FROM t_api_key k
		  LEFT JOIN t_price_group g ON g.code = k.group_code
		 WHERE k.id = ? AND k.user_id = ? AND k.deleted_at IS NULL`,
		keyID, uid).Scan(&row.name, &row.status, &row.group, &row.ratioBP, &row.expire, &row.ipm)
	if errors.Is(err, sql.ErrNoRows) {
		return keyRow{}, apierr.New(apierr.CodeKeyNotFound)
	}
	if err != nil {
		if s.logger != nil {
			s.logger.Error(ctx, "playground_key_query_failed: "+err.Error(), obslog.Fields{Stage: "console"})
		}
		return keyRow{}, apierr.New(apierr.CodeInternalError).WithCause(err)
	}
	return row, nil
}

// claims turns the key row into the signed identity. Expiry and status are
// checked here so the console fails fast with a precise code; the gateway
// re-checks expiry from the signed claim (defence in depth).
func (s *Service) claims(row keyRow, uid, keyID int64) (gw.InternalClaims, *apierr.APIError) {
	var zero gw.InternalClaims
	if !strings.EqualFold(strings.TrimSpace(row.status), "ENABLED") {
		return zero, apierr.Newf(apierr.CodeResourceStateConflict, strings.TrimSpace(row.status)).
			WithHint("控制台 → 密钥管理页启用该密钥后再调试")
	}
	now := s.now.Now().UTC()
	if row.expire.Valid && !row.expire.Time.After(now) {
		return zero, apierr.Newf(apierr.CodeKeyExpired, row.expire.Time.UTC().Format(time.RFC3339))
	}

	// Mirror P05: an empty group_code falls back to standard, and a zero/absent
	// ratio falls back to 10000. Signing the raw zero would make the playground
	// reject its own claim (ratio 0 is refused) and, worse, would describe a
	// free call the public path never gives.
	group := strings.TrimSpace(row.group)
	if group == "" {
		group = "standard"
	}
	ratio := int64(10000)
	if row.ratioBP.Valid && row.ratioBP.Int64 > 0 {
		ratio = row.ratioBP.Int64
	}
	if ratio > 10000 {
		if s.logger != nil {
			s.logger.Error(context.TODO(), fmt.Sprintf("playground_group_ratio_invalid: %d", ratio),
				obslog.Fields{Stage: "console"})
		}
		return zero, apierr.New(apierr.CodeConfigUnavailable)
	}

	expiryMS := int64(0)
	if row.expire.Valid {
		expiryMS = row.expire.Time.UTC().UnixMilli()
	}
	return gw.InternalClaims{
		// KeyID is the real t_api_key.id, never 0/-1: P06, P07, P08, P10, P11,
		// the model list, the log rows and the connection slot all key off it.
		KeyID:        keyID,
		UserID:       uid,
		KeyName:      row.name,
		GroupCode:    group,
		GroupRatioBP: ratio,
		IPMRuleVer:   row.ipm,
		KeyExpiryMS:  expiryMS,
	}, nil
}

// hopByHopHeaders are never forwarded. Connection management belongs to the
// console connection, not to the gateway connection it was read from.
var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Proxy-Connection":    true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

// relay copies the gateway response to the browser. Status and every
// end-to-end header are preserved, so a 402 keeps its X-Credits-Remaining and
// a 429 keeps Retry-After and the rate-limit quartet.
func relay(w http.ResponseWriter, resp *http.Response) error {
	h := w.Header()
	copyHeaders(h, resp.Header)

	sse := isEventStream(resp.Header.Get("Content-Type"))
	if sse {
		// The gateway already sends these; setting them again is the console's
		// own guarantee, because a future proxy must not buffer or compress.
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		h.Del("Content-Length")
	}

	w.WriteHeader(resp.StatusCode)
	if sse {
		// Commit the headers before waiting for the first upstream frame: an SSE
		// client needs 200 + text/event-stream immediately, not after the model
		// emits its first token.
		if err := flushChunk(w); err != nil {
			return err
		}
		return copyFlushing(w, resp.Body)
	}
	_, err := io.Copy(w, resp.Body)
	return err
}

// singleValuedHeaders must be overwritten, not appended. The console's own
// middleware (httpsrv.RequestID) has already written X-Request-Id and
// X-Trace-Id on this response; the gateway writes them too, so Add would leave
// two values and `curl -D` prints each header twice. Both values describe one
// request, and the gateway's is the one the upstream attempt was logged under.
//
// Everything else keeps Add semantics: Set-Cookie is legitimately multi-valued
// and collapsing it would drop a cookie.
var singleValuedHeaders = map[string]bool{
	"X-Request-Id": true,
	"X-Trace-Id":   true,
}

func copyHeaders(dst, src http.Header) {
	for name, values := range src {
		key := http.CanonicalHeaderKey(name)
		if hopByHopHeaders[key] {
			continue
		}
		if singleValuedHeaders[key] {
			for _, v := range values {
				dst.Set(name, v)
			}
			continue
		}
		for _, v := range values {
			dst.Add(name, v)
		}
	}
}

func isEventStream(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "text/event-stream")
}

// copyFlushing relays one read at a time and flushes after each write. There is
// deliberately no whole-body read: buffering the stream would defeat SSE.
func copyFlushing(w http.ResponseWriter, body io.Reader) error {
	buf := make([]byte, relayChunkBytes)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return writeErr
			}
			if flushErr := flushChunk(w); flushErr != nil {
				return flushErr
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

// flushChunk prefers the Flusher interface (mw.recoverWriter implements it) and
// falls back to ResponseController, which unwraps middleware writers.
func flushChunk(w http.ResponseWriter) error {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
		return nil
	}
	return http.NewResponseController(w).Flush()
}

func (s *Service) logError(ctx context.Context, msg, rid string) {
	if s.logger == nil {
		return
	}
	s.logger.Error(ctx, msg, obslog.Fields{Stage: "console", RequestID: rid})
}

func (s *Service) logWarn(ctx context.Context, msg, rid string) {
	if s.logger == nil {
		return
	}
	s.logger.Warn(ctx, msg, obslog.Fields{Stage: "console", RequestID: rid})
}

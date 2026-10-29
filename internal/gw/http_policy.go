package gw

// T42 segment 2: the external half of decision C. KeywordChecker stays the
// local fallback; this file adds a vendor-neutral HTTP adapter behind the same
// PolicyChecker interface. There is no provider SDK and no provider-specific
// field: operations adapt a vendor by implementing the contract below.
//
// Wire contract (both directions are UTF-8 JSON, one object each):
//
//	POST <GW_POLICY_ENDPOINT>
//	Content-Type: application/json
//	Authorization: Bearer <GW_POLICY_TOKEN>   (only when the token is set)
//
//	{"text":"<the decoded request text, see policyScanText>"}
//
//	2xx response:
//	{"blocked":false}
//	 or
//	{"blocked":true,
//	 "rule_id":"vendor-rule-7",     optional; defaults to "ext_policy"
//	 "rule_version":"2026-10",      optional; defaults to "external"
//	 "category":"VIOLENCE",         optional; one of the six category constants,
//	                                anything else is stored as OTHER
//	 "offset":12,                   optional; rune offset into "text"
//	 "length":2}                    optional; rune length of the match
//
// blocked is required. A response without it is a bad_response, not an
// implicit allow. offset/length only position the redacted excerpt that is
// stored in t_content_policy_hit.matched_excerpt; the service never receives
// or returns the excerpt itself, so a vendor cannot inject text into the
// evidence column. severity in the response is ignored: V1.0 enforces BLOCK
// only, and a reported WARN would otherwise create a row that was not refused.
//
// Failure policy (T42 §3.3, confirmed by the requester): fail-open. A timeout,
// a non-2xx status, a malformed body or a transport error returns an error and
// the stage lets the request through with a WARN and a metric. The error is a
// *PolicyCheckError whose Reason is one of the bounded fail-open reasons.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"yunshu.dev/core/internal/jsonx"
)

// Environment variables. They are deliberately not t_config_item keys: D-08
// freezes that set at 45, and an endpoint plus a bearer token is a deployment
// secret rather than a hot-reloadable strategy value.
const (
	PolicyEnvEndpoint  = "GW_POLICY_ENDPOINT"
	PolicyEnvToken     = "GW_POLICY_TOKEN"
	PolicyEnvTimeoutMS = "GW_POLICY_TIMEOUT_MS"
)

const (
	// DefaultPolicyTimeoutMS is the per-call ceiling when the environment does
	// not override it. 800ms is short enough that a slow vendor cannot turn
	// into visible request latency.
	DefaultPolicyTimeoutMS = 800
	// MaxPolicyTimeoutMS is the hard cap. The call is charged against the
	// request's P01 budget, so an operator cannot configure 120000 and consume
	// the whole request deadline before P09.
	MaxPolicyTimeoutMS = 5000

	// policyMaxResponseBytes bounds the response read. The contract is one
	// small object; anything larger is a bad_response, not something to buffer.
	policyMaxResponseBytes = 64 << 10

	// policyDefaultRuleID and policyDefaultRuleVersion keep the NOT NULL
	// columns explainable when a vendor omits them.
	policyDefaultRuleID      = "ext_policy"
	policyDefaultRuleVersion = "external"
)

// Bounded fail-open reasons. They are the value of the WARN "reason" field and
// of the gw_content_policy_fail_open_total{reason} label, so the set must stay
// closed.
const (
	PolicyFailOpenTimeout         = "timeout"
	PolicyFailOpenHTTPStatus      = "http_status"
	PolicyFailOpenBadResponse     = "bad_response"
	PolicyFailOpenTransport       = "transport"
	PolicyFailOpenBudgetExhausted = "budget_exhausted"
	PolicyFailOpenOther           = "other"
)

// PolicyCheckError is a checker failure that the stage turns into fail-open.
// Reason is always one of the bounded constants above.
type PolicyCheckError struct {
	Reason     string
	HTTPStatus int
	Err        error
}

// Error implements error.
func (e *PolicyCheckError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.HTTPStatus != 0 {
		return fmt.Sprintf("policy checker %s (http %d): %v", e.Reason, e.HTTPStatus, e.Err)
	}
	if e.Err != nil {
		return "policy checker " + e.Reason + ": " + e.Err.Error()
	}
	return "policy checker " + e.Reason
}

// Unwrap exposes the transport error to errors.Is.
func (e *PolicyCheckError) Unwrap() error { return e.Err }

// policyFailOpenReason maps any checker error to the bounded reason set. A
// third-party checker that returns a bare error is reported as "other".
func policyFailOpenReason(err error) string {
	var pe *PolicyCheckError
	if errors.As(err, &pe) && pe != nil {
		switch pe.Reason {
		case PolicyFailOpenTimeout, PolicyFailOpenHTTPStatus, PolicyFailOpenBadResponse,
			PolicyFailOpenTransport, PolicyFailOpenBudgetExhausted, PolicyFailOpenOther:
			return pe.Reason
		}
	}
	return PolicyFailOpenOther
}

// HTTPPolicyOptions configures the adapter. A zero Timeout means the default;
// a negative value is rejected. Client is optional and only exists so a test
// can inject a transport without an httptest server.
type HTTPPolicyOptions struct {
	Endpoint string
	Token    string
	Timeout  time.Duration
	Client   *http.Client
}

// HTTPPolicyChecker is the vendor-neutral external PolicyChecker.
type HTTPPolicyChecker struct {
	endpoint string
	token    string
	timeout  time.Duration
	client   *http.Client
}

// NewHTTPPolicyChecker validates the endpoint and builds the adapter. An empty
// endpoint returns (nil, nil): the caller must then keep the keyword checker or
// leave Deps.PolicyChecker nil. There is deliberately no permissive stand-in.
func NewHTTPPolicyChecker(o HTTPPolicyOptions) (*HTTPPolicyChecker, error) {
	endpoint := strings.TrimSpace(o.Endpoint)
	if endpoint == "" {
		return nil, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("gw: %s must be an absolute http(s) URL", PolicyEnvEndpoint)
	}

	timeout := o.Timeout
	if timeout == 0 {
		timeout = DefaultPolicyTimeoutMS * time.Millisecond
	}
	if timeout < 0 {
		return nil, fmt.Errorf("gw: policy timeout must be positive")
	}
	if timeout > MaxPolicyTimeoutMS*time.Millisecond {
		return nil, fmt.Errorf("gw: policy timeout %s exceeds the %dms cap",
			timeout, MaxPolicyTimeoutMS)
	}

	client := o.Client
	if client == nil {
		client = &http.Client{}
	} else {
		// Copy so a caller-owned client is not mutated. A redirect would replay
		// the bearer token to a host the operator did not configure, so the
		// adapter never follows one: 3xx is a non-2xx failure and fails open.
		clone := *client
		client = &clone
	}
	if client.CheckRedirect == nil {
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	return &HTTPPolicyChecker{
		endpoint: endpoint,
		token:    strings.TrimSpace(o.Token),
		timeout:  timeout,
		client:   client,
	}, nil
}

// NewHTTPPolicyCheckerFromEnv returns nil when GW_POLICY_ENDPOINT is unset or
// blank. A malformed URL, a non-numeric/non-positive timeout or a timeout above
// MaxPolicyTimeoutMS is a startup error rather than a silent fallback: the
// operator asked for an external check and a typo must not quietly disable it.
func NewHTTPPolicyCheckerFromEnv() (*HTTPPolicyChecker, error) {
	endpoint := strings.TrimSpace(os.Getenv(PolicyEnvEndpoint))
	if endpoint == "" {
		return nil, nil
	}

	timeout := DefaultPolicyTimeoutMS * time.Millisecond
	if raw := strings.TrimSpace(os.Getenv(PolicyEnvTimeoutMS)); raw != "" {
		ms, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || ms <= 0 || ms > MaxPolicyTimeoutMS {
			return nil, fmt.Errorf("gw: %s must be an integer in 1..%d, got %q",
				PolicyEnvTimeoutMS, MaxPolicyTimeoutMS, raw)
		}
		timeout = time.Duration(ms) * time.Millisecond
	}

	return NewHTTPPolicyChecker(HTTPPolicyOptions{
		Endpoint: endpoint,
		Token:    os.Getenv(PolicyEnvToken),
		Timeout:  timeout,
	})
}

// httpPolicyRequest is the request contract.
type httpPolicyRequest struct {
	Text string `json:"text"`
}

// httpPolicyResponse is the response contract. Blocked is a pointer so a body
// that omits it is a decode failure instead of a zero-value allow.
type httpPolicyResponse struct {
	Blocked     *bool  `json:"blocked"`
	RuleID      string `json:"rule_id"`
	RuleVersion string `json:"rule_version"`
	Category    string `json:"category"`
	Offset      int    `json:"offset"`
	Length      int    `json:"length"`
}

// Check implements PolicyChecker. It never retains text: the only derived value
// is the redacted excerpt, which the caller may persist.
func (c *HTTPPolicyChecker) Check(ctx context.Context, text string) (Verdict, error) {
	if c == nil || c.endpoint == "" || c.client == nil {
		return Verdict{}, &PolicyCheckError{
			Reason: PolicyFailOpenTransport,
			Err:    errors.New("policy checker is not configured"),
		}
	}

	callTimeout, ok := c.callBudget(ctx)
	if !ok {
		return Verdict{}, &PolicyCheckError{
			Reason: PolicyFailOpenBudgetExhausted,
			Err:    errors.New("request budget exhausted before the external policy check"),
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	payload, err := jsonx.Marshal(httpPolicyRequest{Text: text})
	if err != nil {
		return Verdict{}, &PolicyCheckError{Reason: PolicyFailOpenTransport, Err: err}
	}
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		// The raw error embeds the endpoint; it may carry userinfo or a query
		// token, so only the class of failure is kept.
		return Verdict{}, &PolicyCheckError{
			Reason: PolicyFailOpenTransport,
			Err:    errors.New("could not build the external policy request"),
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		err = policyTransportError(err)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return Verdict{}, &PolicyCheckError{Reason: PolicyFailOpenTimeout, Err: err}
		}
		return Verdict{}, &PolicyCheckError{Reason: PolicyFailOpenTransport, Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Verdict{}, &PolicyCheckError{
			Reason:     PolicyFailOpenHTTPStatus,
			HTTPStatus: resp.StatusCode,
			Err:        errors.New("external policy service answered a non-2xx status"),
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, policyMaxResponseBytes+1))
	if err != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return Verdict{}, &PolicyCheckError{Reason: PolicyFailOpenTimeout, Err: err}
		}
		return Verdict{}, &PolicyCheckError{Reason: PolicyFailOpenBadResponse, Err: err}
	}
	if len(body) > policyMaxResponseBytes {
		return Verdict{}, &PolicyCheckError{
			Reason: PolicyFailOpenBadResponse,
			Err:    fmt.Errorf("external policy response exceeds %d bytes", policyMaxResponseBytes),
		}
	}

	var out httpPolicyResponse
	if err := jsonx.Unmarshal(body, &out); err != nil {
		return Verdict{}, &PolicyCheckError{Reason: PolicyFailOpenBadResponse, Err: err}
	}
	if out.Blocked == nil {
		return Verdict{}, &PolicyCheckError{
			Reason: PolicyFailOpenBadResponse,
			Err:    errors.New("external policy response has no blocked field"),
		}
	}
	if !*out.Blocked {
		return Verdict{}, nil
	}

	ruleID := strings.TrimSpace(out.RuleID)
	if ruleID == "" {
		ruleID = policyDefaultRuleID
	}
	ruleVersion := strings.TrimSpace(out.RuleVersion)
	if ruleVersion == "" {
		ruleVersion = policyDefaultRuleVersion
	}

	// Clamp the vendor offset/length to the rune space of the text we sent.
	// A vendor that answers in bytes would otherwise point the excerpt at a
	// broken UTF-8 boundary; policyExcerpt re-slices []rune, so clamping here
	// keeps both the excerpt and its offset consistent with the stored row.
	total := utf8.RuneCountInString(text)
	offset := out.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	length := out.Length
	if length < 0 {
		length = 0
	}
	if offset+length > total {
		length = total - offset
	}

	excerpt := policyExcerpt(text, offset, length)
	sum := sha256.Sum256([]byte(excerpt))
	return Verdict{
		Blocked:        true,
		RuleID:         ruleID,
		RuleVersion:    ruleVersion,
		Category:       normalizePolicyCategory(out.Category),
		Severity:       PolicySeverityBlock,
		MatchedExcerpt: excerpt,
		SnippetSHA256:  hex.EncodeToString(sum[:]),
		SnippetOffset:  offset,
	}, nil
}

// policyTransportError strips the *url.Error wrapper. That type formats the
// request URL into Error(), and an operator may have put credentials in the
// endpoint (userinfo or a query token); the WARN line must not copy them.
func policyTransportError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// callBudget returns min(time left in the P01 context, configured timeout).
//
// The external call gets its own context.WithTimeout, so it is a sub-budget of
// P01 rather than a second budget: it can never outlive the request deadline,
// and it normally consumes at most GW_POLICY_TIMEOUT_MS (800ms by default) of
// the 120s P01 window. If the request has already exhausted its budget there is
// nothing to spend, so the check is skipped and fails open with a distinct
// reason instead of issuing a request with a zero deadline.
func (c *HTTPPolicyChecker) callBudget(ctx context.Context) (time.Duration, bool) {
	if c.timeout <= 0 {
		return 0, false
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0, false
		}
		if remaining < c.timeout {
			return remaining, true
		}
	}
	return c.timeout, true
}

// normalizePolicyCategory maps vendor input onto the six categories the table
// declares. Unknown values are OTHER, never a new free-form string: the column
// is varchar(32) and the console/retention queries group by this value.
func normalizePolicyCategory(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case PolicyCategoryViolence:
		return PolicyCategoryViolence
	case PolicyCategorySexual:
		return PolicyCategorySexual
	case PolicyCategoryPolitical:
		return PolicyCategoryPolitical
	case PolicyCategoryIllegal:
		return PolicyCategoryIllegal
	case PolicyCategoryPII:
		return PolicyCategoryPII
	case PolicyCategoryOther:
		return PolicyCategoryOther
	default:
		return PolicyCategoryOther
	}
}

// ChainPolicyChecker runs checkers in order and stops at the first block.
//
// Decision C's order is keyword first, external second, for two reasons:
//
//   - the keyword set is in-process and has no I/O, so a literal hit is refused
//     without paying a network round trip or a fake-service timeout;
//   - if the external service is down, the local set has still run. External
//     first would return its fail-open error before the keyword scan, which
//     would silently disable the local fallback exactly when it is needed.
type ChainPolicyChecker struct {
	checkers []PolicyChecker
}

// NewChainPolicyChecker drops nil checkers and returns nil when none remain.
// nil therefore keeps its meaning in Deps: the stage is explicitly disabled,
// not protected by an always-allow implementation.
func NewChainPolicyChecker(checkers ...PolicyChecker) PolicyChecker {
	kept := make([]PolicyChecker, 0, len(checkers))
	for _, c := range checkers {
		if c != nil {
			kept = append(kept, c)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	if len(kept) == 1 {
		return kept[0]
	}
	return &ChainPolicyChecker{checkers: kept}
}

// Check implements PolicyChecker. An error stops the chain and is returned
// untouched: the stage maps it to fail-open and the request proceeds.
func (c *ChainPolicyChecker) Check(ctx context.Context, text string) (Verdict, error) {
	if c == nil {
		return Verdict{}, nil
	}
	for _, checker := range c.checkers {
		if checker == nil {
			continue
		}
		verdict, err := checker.Check(ctx, text)
		if err != nil {
			return Verdict{}, err
		}
		if verdict.Blocked {
			return verdict, nil
		}
	}
	return Verdict{}, nil
}

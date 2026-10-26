package apierr

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fixtureRow is a hand-copied snapshot of PRD V1.2 appendix E.1. It is
// intentionally independent of `specs`: if the registry drifts, this test
// fails instead of agreeing with itself.
type fixtureRow struct {
	code  string
	typ   string
	http  int
	retry int
	class RetryClass
	owner Owner
}

var prdE1Fixture = []fixtureRow{
	// E.1.1 鉴权与账号 (7)
	{CodeInvalidAPIKey, TypeAuthentication, 401, 0, RetryNo, OwnerBusiness},
	{CodeKeyExpired, TypeAuthentication, 401, 0, RetryNo, OwnerBusiness},
	{CodeKeyDisabled, TypeAuthorization, 403, 0, RetryNo, OwnerBusiness},
	{CodeAccountDisabled, TypeAuthorization, 403, 0, RetryNo, OwnerBusiness},
	{CodeAccountFrozen, TypeAuthorization, 403, 0, RetryNo, OwnerBusiness},
	{CodeIPNotAllowed, TypeAuthorization, 403, 0, RetryNo, OwnerBusiness},
	{CodeConflictingAuthHeaders, TypeInvalidRequest, 400, 0, RetryNo, OwnerBusiness},

	// E.1.2 模型可见性 (4)
	{CodeModelNotFound, TypeInvalidRequest, 400, 0, RetryNo, OwnerBusiness},
	{CodeModelNotAvailable, TypeUpstream, 503, 30, RetryYes, OwnerBusiness},
	{CodeModelNotInGroup, TypeAuthorization, 403, 0, RetryNo, OwnerBusiness},
	{CodeModelNotAuthorized, TypeAuthorization, 403, 0, RetryNo, OwnerBusiness},

	// E.1.3 请求校验 (11)
	{CodeInvalidRequestBody, TypeInvalidRequest, 400, 0, RetryNo, OwnerBusiness},
	{CodeInvalidMessages, TypeInvalidRequest, 400, 0, RetryNo, OwnerBusiness},
	{CodeInvalidTemperature, TypeInvalidRequest, 400, 0, RetryNo, OwnerBusiness},
	{CodeMaxTokensExceeded, TypeInvalidRequest, 400, 0, RetryNo, OwnerBusiness},
	{CodeInvalidTools, TypeInvalidRequest, 400, 0, RetryNo, OwnerBusiness},
	{CodeInvalidImageCount, TypeInvalidRequest, 400, 0, RetryNo, OwnerBusiness},
	{CodeInvalidImageSize, TypeInvalidRequest, 400, 0, RetryNo, OwnerBusiness},
	{CodeInvalidImageURL, TypeInvalidRequest, 400, 0, RetryNo, OwnerBusiness},
	{CodeRequestTooLarge, TypeInvalidRequest, 413, 0, RetryNo, OwnerBusiness},
	{CodeRequestReadTimeout, TypeInvalidRequest, 408, 5, RetryYes, OwnerBusiness},
	{CodeUnknownEndpoint, TypeInvalidRequest, 404, 0, RetryNo, OwnerBusiness},

	// E.1.4 计费与余额 (3)
	{CodeInsufficientCredits, TypeBilling, 402, 0, RetryNo, OwnerBusiness},
	{CodeAccountFrozenDueToOverdraft, TypeBilling, 402, 0, RetryNo, OwnerBusiness},
	{CodePriceNotConfigured, TypeInternal, 500, 0, RetryNo, OwnerPlatform},

	// E.1.5 限流 (5)
	{CodeRPMLimitExceeded, TypeRateLimit, 429, 60, RetryDuration, OwnerBusiness},
	{CodeTPMLimitExceeded, TypeRateLimit, 429, 60, RetryDuration, OwnerBusiness},
	{CodeConcurrencyLimitExceeded, TypeRateLimit, 429, 3, RetryDuration, OwnerBusiness},
	{CodeDailyQuotaExceeded, TypeRateLimit, 429, 86400, RetryDuration, OwnerBusiness},
	{CodeRateLimiterUnavailable, TypeServiceUnavailable, 503, 5, RetryYes, OwnerPlatform},

	// E.1.6 幂等 (5)
	{CodeRequestInFlight, TypeConflict, 409, 1, RetryDuration, OwnerBusiness},
	{CodeIdempotencyKeyReused, TypeConflict, 409, 0, RetryNo, OwnerBusiness},
	{CodeIdempotentReplayUnsupported, TypeConflict, 409, 0, RetryNo, OwnerBusiness},
	{CodeDuplicateRequestInFlight, TypeConflict, 409, 1, RetryDuration, OwnerBusiness},
	{CodeDuplicateRequestID, TypeConflict, 409, 0, RetryNo, OwnerBusiness},

	// E.1.7 上游与平台 (13)
	{CodeNoAvailableUpstream, TypeUpstream, 503, 30, RetryYes, OwnerConfig},
	{CodeNoAvailableUpstreamForGroup, TypeUpstream, 503, 0, RetryNo, OwnerConfig},
	{CodeUpstreamCapacityExceeded, TypeUpstream, 503, 30, RetryYes, OwnerUpstream},
	{CodeUpstreamTimeout, TypeUpstream, 504, 30, RetryYes, OwnerUpstream},
	{CodeUpstreamDNSFailure, TypeUpstream, 502, 0, RetryNo, OwnerUpstream},
	{CodeUpstreamTLSFailure, TypeUpstream, 502, 0, RetryNo, OwnerUpstream},
	{CodeUpstreamAuthFailed, TypeUpstream, 502, 0, RetryNo, OwnerPlatform},
	{CodeUpstreamModelNotFound, TypeUpstream, 502, 0, RetryNo, OwnerPlatform},
	{CodeUpstreamInvalidRequest, TypeUpstream, 400, 0, RetryNo, OwnerBusiness},
	{CodeUpstreamInvalidResponse, TypeUpstream, 502, 10, RetryYes, OwnerUpstream},
	{CodeUpstreamEmptyResponse, TypeUpstream, 502, 0, RetryNo, OwnerUpstream},
	{CodeUpstreamKeyUnavailable, TypeUpstream, 503, 60, RetryYes, OwnerPlatform},
	{CodeUpstreamAllFailed, TypeUpstream, 502, 30, RetryYes, OwnerUpstream},

	// E.1.8 生命周期与背压 (8)
	{CodeClientAborted, TypeClient, 499, 0, RetryNotApplicable, OwnerBusiness},
	{CodeConvertFailed, TypeInternal, 500, 0, RetryNo, OwnerPlatform},
	{CodeSettlementUnavailable, TypeServiceUnavailable, 503, 5, RetryYes, OwnerPlatform},
	{CodeBackpressureStreamRejected, TypeServiceUnavailable, 503, 5, RetryYes, OwnerPlatform},
	{CodeBackpressureRejected, TypeServiceUnavailable, 503, 2, RetryYes, OwnerPlatform},
	{CodeConfigUnavailable, TypeServiceUnavailable, 503, 10, RetryYes, OwnerPlatform},
	{CodeAuthUnavailable, TypeServiceUnavailable, 503, 5, RetryYes, OwnerPlatform},
	{CodeInternalError, TypeInternal, 500, 0, RetryNo, OwnerPlatform},

	// E.1.9 控制面 (7)
	{CodeKeyNotFound, TypeInvalidRequest, 404, 0, RetryNo, OwnerBusiness},
	{CodeAccountNotVerified, TypeAuthorization, 403, 0, RetryNo, OwnerBusiness},
	{CodePermissionDenied, TypeAuthorization, 403, 0, RetryNo, OwnerBusiness},
	{CodeChannelNotFound, TypeInvalidRequest, 404, 0, RetryNo, OwnerBusiness},
	{CodeChannelInUse, TypeConflict, 409, 0, RetryNo, OwnerBusiness},
	{CodeConfigItemNotFound, TypeInvalidRequest, 404, 0, RetryNo, OwnerBusiness},
	{CodeConfigValueInvalid, TypeInvalidRequest, 400, 0, RetryNo, OwnerBusiness},

	// E.1.10 登记但不实现 (1)
	{CodeAccountRPMExceeded, TypeRateLimit, 429, 60, RetryDuration, OwnerBusiness},

	// E.1.11 内容安全 (1)
	{CodeContentPolicyViolation, TypeInvalidRequest, 403, 0, RetryNo, OwnerBusiness},
}

func TestCodeCountIs65(t *testing.T) {
	if got := len(Codes()); got != 65 {
		t.Fatalf("len(Codes()) = %d, want 65", got)
	}
	if got := len(Specs()); got != 65 {
		t.Fatalf("len(Specs()) = %d, want 65", got)
	}
	if got := len(prdE1Fixture); got != 65 {
		t.Fatalf("fixture rows = %d, want 65", got)
	}
	rows := strings.Split(strings.TrimSpace(MarkdownTable()), "\n")
	if len(rows) != 2+65 {
		t.Fatalf("markdown rows = %d, want 2 header + 65 = 67", len(rows))
	}
}

// counting the constants from source, not from the registry, catches a code
// that was added as a constant but never entered specs (or vice versa).
func TestCodeConstantsCountIs65(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(filepath.Dir(thisFile), "codes.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, s := range gd.Specs {
			vs, ok := s.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				if strings.HasPrefix(name.Name, "Code") {
					count++
				}
			}
		}
	}
	if count != 65 {
		t.Fatalf("Code* constants = %d, want 65", count)
	}
}

func TestEveryCodeMatchesPRDE1(t *testing.T) {
	seen := make(map[string]bool, len(prdE1Fixture))
	for _, row := range prdE1Fixture {
		seen[row.code] = true
		spec, ok := Lookup(row.code)
		if !ok {
			t.Errorf("%s: fixture code missing from registry", row.code)
			continue
		}
		if spec.Type != row.typ {
			t.Errorf("%s: type = %q, PRD E.1 = %q", row.code, spec.Type, row.typ)
		}
		if spec.HTTPStatus != row.http {
			t.Errorf("%s: HTTP = %d, PRD E.1 = %d", row.code, spec.HTTPStatus, row.http)
		}
		if spec.RetryAfterSeconds != row.retry {
			t.Errorf("%s: retry_after_seconds = %d, PRD E.1 = %d",
				row.code, spec.RetryAfterSeconds, row.retry)
		}
		if spec.Retry != row.class {
			t.Errorf("%s: retry class = %q, PRD E.1 = %q", row.code, spec.Retry, row.class)
		}
		if spec.Owner != row.owner {
			t.Errorf("%s: owner = %q, PRD E.1 = %q", row.code, spec.Owner, row.owner)
		}
	}
	for _, code := range Codes() {
		if !seen[code] {
			t.Errorf("%s: registry code missing from PRD E.1 fixture", code)
		}
	}
}

func TestEveryCodeHasOwner(t *testing.T) {
	owners := map[Owner]bool{
		OwnerPlatform: true, OwnerUpstream: true, OwnerConfig: true, OwnerBusiness: true,
	}
	for _, s := range Specs() {
		if s.Owner == "" || !owners[s.Owner] {
			t.Errorf("%s: owner = %q, not one of PLATFORM/UPSTREAM/CONFIG/BUSINESS", s.Code, s.Owner)
		}
		if s.Stage == "" {
			t.Errorf("%s: trigger stage is empty", s.Code)
		}
	}
}

func TestTypeSetHasTenValues(t *testing.T) {
	allowed := map[string]bool{
		TypeAuthentication: true, TypeAuthorization: true, TypeInvalidRequest: true,
		TypeBilling: true, TypeRateLimit: true, TypeConflict: true,
		TypeUpstream: true, TypeServiceUnavailable: true, TypeClient: true, TypeInternal: true,
	}
	seen := make(map[string]bool)
	for _, s := range Specs() {
		if !allowed[s.Type] {
			t.Errorf("%s: type %q is not one of the ten PRD types", s.Code, s.Type)
		}
		seen[s.Type] = true
	}
	if len(seen) != 10 {
		t.Fatalf("types in use = %d, want 10: %v", len(seen), seen)
	}
}

func TestRetryNoAlwaysHasZeroSeconds(t *testing.T) {
	for _, s := range Specs() {
		if s.Retry == RetryNo && s.RetryAfterSeconds != 0 {
			t.Errorf("%s: retry=no but retry_after_seconds=%d (E-1)",
				s.Code, s.RetryAfterSeconds)
		}
		if s.Retry == RetryNotApplicable && s.RetryAfterSeconds != 0 {
			t.Errorf("%s: retry=not_applicable but retry_after_seconds=%d",
				s.Code, s.RetryAfterSeconds)
		}
	}
}

func TestPlatformHintsDoNotSuggestUpgrade(t *testing.T) {
	for _, s := range Specs() {
		if s.Owner != OwnerPlatform {
			continue
		}
		if strings.Contains(s.Hint, "升级套餐") || strings.Contains(s.Hint, "申请提额") {
			t.Errorf("%s: platform-side hint suggests a user quota action: %q", s.Code, s.Hint)
		}
	}
}

func TestRequiredTemplatesArePresent(t *testing.T) {
	for _, s := range Specs() {
		if s.Message == "" {
			t.Errorf("%s: message template is empty", s.Code)
		}
		if s.Hint == "" {
			t.Errorf("%s: hint template is empty", s.Code)
		}
	}
}

// A4: the two 401s must remain distinct codes with distinct hints.
func TestInvalidAPIKeyAndKeyExpiredAreDistinct(t *testing.T) {
	a := New(CodeInvalidAPIKey)
	b := Newf(CodeKeyExpired, "2026-10-01T00:00:00+08:00")
	if a.Code == b.Code {
		t.Fatalf("codes are equal: %q", a.Code)
	}
	if a.HTTPStatus != 401 || b.HTTPStatus != 401 {
		t.Fatalf("statuses = %d/%d, want both 401", a.HTTPStatus, b.HTTPStatus)
	}
	if a.Hint == b.Hint {
		t.Fatalf("hints must differ, both %q", a.Hint)
	}
	if !strings.Contains(b.Message, "2026-10-01") {
		t.Fatalf("key_expired message lacks the expiry time: %q", b.Message)
	}
}

func TestModelNotAvailableIs503WithNotice(t *testing.T) {
	e := Newf(CodeModelNotAvailable, "qwen-max")
	if e.HTTPStatus != 503 {
		t.Fatalf("HTTP = %d, want 503 (not 404/500)", e.HTTPStatus)
	}
	if e.RetryAfterSeconds != 30 {
		t.Fatalf("retry = %d, want 30", e.RetryAfterSeconds)
	}
	if !strings.Contains(e.Message, "恢复上架后有公告") {
		t.Fatalf("message must announce the recovery notice: %q", e.Message)
	}
}

func TestUpstreamCapacityIs503Not429(t *testing.T) {
	if e := New(CodeUpstreamCapacityExceeded); e.HTTPStatus != 503 {
		t.Fatalf("HTTP = %d, want 503; upstream 429 must never become client 429", e.HTTPStatus)
	}
}

func TestNoDuplicateCodeValues(t *testing.T) {
	seen := make(map[string]bool, len(specs))
	for _, s := range specs {
		if seen[s.Code] {
			t.Errorf("duplicate code %q", s.Code)
		}
		seen[s.Code] = true
	}
}

// A12: field sets for the ten type examples in E.6 (field order irrelevant).
func TestE6ExampleFieldSets(t *testing.T) {
	now := fixedNow()
	dailyReset := timeAtShanghai(now, 10, 31, 0)
	rpm := Newf(CodeTPMLimitExceeded, int64(630000), 23).
		WithLimit(630000).WithRemaining(0).WithResetAtUnix(dailyReset.Unix()).
		WithRetryAfter(23)
	frozen := NewFrozenDueToOverdraft(37, 50, 0)

	cases := []struct {
		name string
		e    *APIError
		want []string
	}{
		{"authentication_error", New(CodeInvalidAPIKey),
			[]string{"type", "code", "message", "retry_after_seconds", "hint", "request_id"}},
		{"authorization_error", Newf(CodeModelNotAuthorized, "qwen-max"),
			[]string{"type", "code", "message", "retry_after_seconds", "hint", "request_id"}},
		{"invalid_request_error", Newf(CodeInvalidTemperature, 2.5),
			[]string{"type", "code", "message", "retry_after_seconds", "hint", "request_id"}},
		{"billing_error", frozen,
			[]string{"type", "code", "message", "retry_after_seconds", "limit", "remaining", "hint", "request_id"}},
		{"rate_limit_exceeded", rpm,
			[]string{"type", "code", "message", "retry_after_seconds", "reset_at", "limit", "remaining", "hint", "request_id"}},
		{"conflict_error", New(CodeRequestInFlight),
			[]string{"type", "code", "message", "retry_after_seconds", "hint", "request_id"}},
		{"upstream_error", Newf(CodeUpstreamTimeout, 1),
			[]string{"type", "code", "message", "retry_after_seconds", "hint", "request_id"}},
		{"service_unavailable_error", New(CodeBackpressureRejected),
			[]string{"type", "code", "message", "retry_after_seconds", "hint", "request_id"}},
		{"client_error", New(CodeClientAborted),
			[]string{"type", "code", "message", "retry_after_seconds", "hint", "request_id"}},
		{"internal_error", New(CodeInternalError),
			[]string{"type", "code", "message", "retry_after_seconds", "hint", "request_id"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := errorFieldSet(t, tc.e)
			if !sameSet(got, tc.want) {
				t.Fatalf("field set = %v, E.6 = %v", got, tc.want)
			}
		})
	}
}

// A7 + A12 for every code: required six are always present; optional three
// appear only on rate-limit codes and 402; 402 never has reset_at.
func TestEveryCodeBodyShape(t *testing.T) {
	required := []string{"type", "code", "message", "retry_after_seconds", "hint", "request_id"}
	for _, s := range Specs() {
		got := errorFieldSet(t, New(s.Code))
		for _, key := range required {
			if !got[key] {
				t.Errorf("%s: required field %q missing", s.Code, key)
			}
		}
		switch s.Optional {
		case OptionalNone:
			for _, key := range []string{"limit", "remaining", "reset_at"} {
				if got[key] {
					t.Errorf("%s: field %q must be absent for a non-rate-limit code", s.Code, key)
				}
			}
		case OptionalBilling:
			if !got["limit"] || !got["remaining"] {
				t.Errorf("%s: 402 must carry limit and remaining", s.Code)
			}
			if got["reset_at"] {
				t.Errorf("%s: 402 must not carry reset_at", s.Code)
			}
		case OptionalRateLimit:
			for _, key := range []string{"limit", "remaining", "reset_at"} {
				if !got[key] {
					t.Errorf("%s: 429 must carry %s", s.Code, key)
				}
			}
		}
	}
}

func errorFieldSet(t *testing.T, e *APIError) map[string]bool {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	body, ok := envelope["error"]
	if !ok {
		t.Fatalf("body has no top-level error object: %s", raw)
	}
	out := make(map[string]bool, len(body))
	for k := range body {
		out[k] = true
	}
	return out
}

func sameSet(got map[string]bool, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, k := range want {
		if !got[k] {
			return false
		}
	}
	return true
}

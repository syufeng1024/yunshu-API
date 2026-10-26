package gw

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/apierr"
	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/redisx"
)

// --- fakes ---------------------------------------------------------------

type recordStage struct {
	name  string
	calls *[]string
	err   error
}

func (r recordStage) Name() string { return r.name }
func (r recordStage) Run(context.Context, *Ctx) error {
	*r.calls = append(*r.calls, r.name)
	return r.err
}

type fakeAuth struct {
	records map[string]redisx.KeyAuthRecord
	miss    error
}

func (f fakeAuth) Lookup(_ context.Context, hash string) (redisx.KeyAuthRecord, error) {
	if f.miss != nil {
		return redisx.KeyAuthRecord{}, f.miss
	}
	rec, ok := f.records[hash]
	if !ok {
		return redisx.KeyAuthRecord{}, redisx.ErrKeyAuthNotFound
	}
	return rec, nil
}

type fakeBalance struct {
	micro int64
	err   error
}

func (f fakeBalance) Get(context.Context, int64) (int64, error) { return f.micro, f.err }

type fakePrices struct {
	raw jsonx.RawMessage
	err error
}

func (f fakePrices) GetStandard(context.Context, int64) (jsonx.RawMessage, error) {
	return f.raw, f.err
}

// --- A2: execution order -------------------------------------------------

func TestPipelineOrderIsP05P085P12P09P11P10(t *testing.T) {
	var calls []string
	pipeline := NewPipeline(nil,
		recordStage{name: "P05", calls: &calls},
		recordStage{name: "P08.5", calls: &calls},
		recordStage{name: "P12", calls: &calls},
		recordStage{name: "P09", calls: &calls},
		recordStage{name: "P11", calls: &calls},
		recordStage{name: "P10", calls: &calls},
	)
	if err := pipeline.Run(t.Context(), &Ctx{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []string{"P05", "P08.5", "P12", "P09", "P11", "P10"}
	if len(calls) != len(want) {
		t.Fatalf("call sequence = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("call sequence = %v, want %v", calls, want)
		}
	}
	t.Logf("actual call sequence: %v", calls)
}

func TestDefaultPipelineOrderIsNotNumeric(t *testing.T) {
	pipeline := NewDefaultPipeline(staticDeps(nil))
	got := pipeline.Names()
	want := []string{"P05", "P08.5", "P12", "P09", "P11", "P10"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("default pipeline = %v, want %v", got, want)
	}
}

func TestPipelineShortCircuitsAndNamesFailStage(t *testing.T) {
	var calls []string
	failure := StageErr("P12", apierr.New(apierr.CodeNoAvailableUpstream))
	pipeline := NewPipeline(nil,
		recordStage{name: "P05", calls: &calls},
		recordStage{name: "P08.5", calls: &calls},
		recordStage{name: "P12", calls: &calls, err: failure},
		recordStage{name: "P09", calls: &calls},
	)
	c := &Ctx{}
	err := pipeline.Run(t.Context(), c)
	if err == nil {
		t.Fatal("pipeline did not fail")
	}
	if strings.Join(calls, ",") != "P05,P08.5,P12" {
		t.Fatalf("calls = %v; stages after the failure must not run", calls)
	}
	if c.FailStage != "P12" {
		t.Fatalf("FailStage = %q, want P12", c.FailStage)
	}
	var se *StageError
	if !errors.As(err, &se) || se.API.Code != apierr.CodeNoAvailableUpstream {
		t.Fatalf("error = %v, want no_available_upstream", err)
	}
}

func TestPipelineStopIsNotAnError(t *testing.T) {
	calls := []string{}
	pipeline := NewPipeline(nil,
		recordStage{name: "P05", calls: &calls},
		recordStage{name: "P08.5", calls: &calls, err: ErrPipelineStop},
		recordStage{name: "P12", calls: &calls},
	)
	if err := pipeline.Run(t.Context(), &Ctx{}); err != nil {
		t.Fatalf("stop returned an error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %v, want the pipeline to stop after P08.5", calls)
	}
}

// --- P05 / A3 / A6 -------------------------------------------------------

func ctxWithAuthHeader(key string) *Ctx {
	h := http.Header{}
	if key != "" {
		h.Set("Authorization", "Bearer "+key)
	}
	return &Ctx{Header: h}
}

func TestA3MissingKeyIsInvalidAPIKeyAndExpiredIsKeyExpired(t *testing.T) {
	deps := staticDeps(nil)
	deps.Auth = fakeAuth{records: map[string]redisx.KeyAuthRecord{}}

	// No credential at all.
	stage := NewAuthStage(deps)
	err := stage.Run(t.Context(), ctxWithAuthHeader(""))
	assertAPIError(t, err, apierr.CodeInvalidAPIKey, http.StatusUnauthorized)

	// A key absent from K13: 401 invalid_api_key, never a database fallback.
	err = stage.Run(t.Context(), ctxWithAuthHeader("sk-missing"))
	assertAPIError(t, err, apierr.CodeInvalidAPIKey, http.StatusUnauthorized)

	// An expired key: a different code, because the client's next action is
	// different (extend this key vs. check the key string).
	expired := "sk-expired"
	records := map[string]redisx.KeyAuthRecord{
		KeyHash16(expired): {
			UID: 1, KID: 10, ST: "ENABLED", EXP: time.Now().Add(-time.Minute).UnixMilli(),
			GRP: "standard", GP: 10000,
		},
	}
	expiredDeps := staticDeps(nil)
	expiredDeps.Auth = fakeAuth{records: records}
	err = NewAuthStage(expiredDeps).Run(t.Context(), ctxWithAuthHeader(expired))
	assertAPIError(t, err, apierr.CodeKeyExpired, http.StatusUnauthorized)
	var se *StageError
	if !errors.As(err, &se) || se.Stage != "P05" {
		t.Fatalf("stage = %v, want P05", err)
	}
}

func TestA6ConflictingAuthHeadersIs400(t *testing.T) {
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-one")
	h.Set("x-api-key", "sk-two")
	_, err := Credential(h)
	assertAPIError(t, err, apierr.CodeConflictingAuthHeaders, http.StatusBadRequest)

	// Equal values are the same credential, not a conflict.
	h.Set("x-api-key", "sk-one")
	token, err := Credential(h)
	if err != nil || token != "sk-one" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
}

func TestAuthStageLoadsGroupAndRatioFromSameRecord(t *testing.T) {
	key := "sk-good"
	deps := staticDeps(nil)
	deps.Auth = fakeAuth{records: map[string]redisx.KeyAuthRecord{
		KeyHash16(key): {UID: 7, KID: 10, ST: "ENABLED", GRP: "contract", GP: 8500},
	}}
	c := ctxWithAuthHeader(key)
	if err := NewAuthStage(deps).Run(t.Context(), c); err != nil {
		t.Fatalf("auth: %v", err)
	}
	if c.Auth.GroupCode != "contract" || c.Auth.GroupRatioBP != 8500 {
		t.Fatalf("auth = %+v", c.Auth)
	}
	if c.Limits.RPM != freeDefaultRPM {
		t.Fatalf("limits = %+v, want FREE_DEFAULT", c.Limits)
	}
}

// --- P07 / A4 / A5 -------------------------------------------------------

func p07Ctx(model string, keyID int64, group string) *Ctx {
	return &Ctx{
		IR:   &IRRequest{Model: model},
		Auth: &AuthContext{KeyID: keyID, GroupCode: group, GroupRatioBP: 10000},
	}
}

func TestA4A5ModelStageOrderAndCodes(t *testing.T) {
	catalog := staticCatalog(
		[]ModelEntry{
			{ID: 1, ModelName: "listed", Region: RegionDomestic, ModelType: ModelTypeText, ListingState: ListingListed},
			{ID: 2, ModelName: "catalog-only", Region: RegionDomestic, ModelType: ModelTypeText, ListingState: ListingCatalogOnly},
			{ID: 3, ModelName: "delisted", Region: RegionDomestic, ModelType: ModelTypeText, ListingState: ListingDelisted},
			{ID: 4, ModelName: "unauthorized", Region: RegionDomestic, ModelType: ModelTypeText, ListingState: ListingListed},
			{ID: 5, ModelName: "out-of-group", Region: RegionDomestic, ModelType: ModelTypeText, ListingState: ListingListed},
			{ID: 6, ModelName: "both-unauthorized-and-out", Region: RegionDomestic, ModelType: ModelTypeText, ListingState: ListingListed},
			{ID: 7, ModelName: "overseas", Region: RegionOverseas, ModelType: ModelTypeText, ListingState: ListingListed},
		},
		nil, nil,
		map[string]GroupEntry{
			"standard": {
				Code: "standard", ModelScopeMode: ScopeSelected, ChannelScopeMode: ScopeAll,
				Models: []int64{1, 2, 3, 4, 7},
			},
		},
		map[string]KeyModelGrant{
			"10": {Mode: ScopeSelected, ModelIDs: []int64{1, 2, 3, 5}},
		},
	)
	deps := staticDeps(catalog)
	stage := NewModelStage(deps)

	cases := []struct {
		name   string
		model  string
		code   string
		status int
	}{
		{"listed", "listed", "", 0},
		{"catalog only", "catalog-only", apierr.CodeModelNotAvailable, http.StatusServiceUnavailable},
		{"delisted", "delisted", apierr.CodeModelNotFound, http.StatusBadRequest},
		{"unknown", "no-such-model", apierr.CodeModelNotFound, http.StatusBadRequest},
		{"overseas", "overseas", apierr.CodeModelNotFound, http.StatusBadRequest},
		{"key not authorized", "unauthorized", apierr.CodeModelNotAuthorized, http.StatusForbidden},
		{"group excludes", "out-of-group", apierr.CodeModelNotInGroup, http.StatusForbidden},
		{"authorization before group", "both-unauthorized-and-out", apierr.CodeModelNotAuthorized, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := p07Ctx(tc.model, 10, "standard")
			err := stage.Run(t.Context(), c)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if c.Model == nil || c.Model.ID != 1 {
					t.Fatalf("model = %+v", c.Model)
				}
				return
			}
			assertAPIError(t, err, tc.code, tc.status)
		})
	}
}

func TestP07CatalogOnlyStopsBeforeAuthorization(t *testing.T) {
	// Key 99 has no grant at all. If the stage checked authorization first it
	// would return model_not_authorized; CATALOG_ONLY must win because the user
	// cannot fix a suspended model by editing key scopes.
	catalog := staticCatalog(
		[]ModelEntry{{ID: 2, ModelName: "catalog-only", Region: RegionDomestic,
			ModelType: ModelTypeText, ListingState: ListingCatalogOnly}},
		nil, nil,
		map[string]GroupEntry{"standard": {Code: "standard", ModelScopeMode: ScopeAll, ChannelScopeMode: ScopeAll}},
		map[string]KeyModelGrant{},
	)
	err := NewModelStage(staticDeps(catalog)).Run(t.Context(), p07Ctx("catalog-only", 99, "standard"))
	assertAPIError(t, err, apierr.CodeModelNotAvailable, http.StatusServiceUnavailable)
}

// --- /v1/models ----------------------------------------------------------

func TestModelsListIsAuthorizedIntersectionWithoutCatalogOnly(t *testing.T) {
	catalog := staticCatalog(
		[]ModelEntry{
			{ID: 1, ModelName: "a-listed", Region: RegionDomestic, ModelType: ModelTypeText, ListingState: ListingListed},
			{ID: 2, ModelName: "b-catalog", Region: RegionDomestic, ModelType: ModelTypeText, ListingState: ListingCatalogOnly},
			{ID: 3, ModelName: "c-overseas", Region: RegionOverseas, ModelType: ModelTypeText, ListingState: ListingListed},
			{ID: 4, ModelName: "d-not-granted", Region: RegionDomestic, ModelType: ModelTypeText, ListingState: ListingListed},
			{ID: 5, ModelName: "e-not-in-group", Region: RegionDomestic, ModelType: ModelTypeText, ListingState: ListingListed},
			{ID: 6, ModelName: "f-listed", Region: RegionDomestic, ModelType: ModelTypeEmbedding, ListingState: ListingListed},
		},
		nil, nil,
		map[string]GroupEntry{"standard": {Code: "standard", ModelScopeMode: ScopeSelected,
			ChannelScopeMode: ScopeAll, Models: []int64{1, 2, 3, 4, 6}}},
		map[string]KeyModelGrant{"10": {Mode: ScopeSelected, ModelIDs: []int64{1, 2, 3, 5, 6}}},
	)
	snap, err := catalog.Current()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	models := snap.ListableModels(10, "standard")
	names := make([]string, 0, len(models))
	for _, m := range models {
		names = append(names, m.ModelName)
	}
	if strings.Join(names, ",") != "f-listed,a-listed" {
		t.Fatalf("models = %v, want [f-listed a-listed]", names)
	}
}

// --- P02 / A12 -----------------------------------------------------------

func TestA12BodyOverTenMegabytesIs413(t *testing.T) {
	body := strings.Repeat("x", MaxBodyBytes+1)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	_, err := ReadBody(w, r, MaxBodyBytes, BodyReadTimeout)
	assertAPIError(t, err, apierr.CodeRequestTooLarge, http.StatusRequestEntityTooLarge)
}

func TestA12BodyReadTimeoutIs408(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", io.LimitReader(timeoutReader{}, 10))
	w := httptest.NewRecorder()
	_, err := ReadBody(w, r, MaxBodyBytes, BodyReadTimeout)
	assertAPIError(t, err, apierr.CodeRequestReadTimeout, http.StatusRequestTimeout)
}

type timeoutReader struct{}

func (timeoutReader) Read([]byte) (int, error) { return 0, timeoutError{} }

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// --- P08.5 / A9 ----------------------------------------------------------

func TestA9BackpressureThresholds(t *testing.T) {
	b := NewBackpressure(nil)
	const max = int64(10)

	// 5/10 = 50%: both admitted.
	for i := 0; i < 5; i++ {
		slot, apiErr := b.Acquire(t.Context(), max, true)
		if apiErr != nil || slot == nil {
			t.Fatalf("acquire %d: %v", i, apiErr)
		}
		defer slot.Release()
	}

	// The sixth request makes 6/10 = 60%: streaming is shed first.
	_, apiErr := b.Acquire(t.Context(), max, true)
	assertAPIError(t, apiErr, apierr.CodeBackpressureStreamRejected, http.StatusServiceUnavailable)

	// A non-stream request is still admitted in the 60..90 band.
	slot, apiErr := b.Acquire(t.Context(), max, false)
	if apiErr != nil {
		t.Fatalf("non-stream at 60%%: %v", apiErr)
	}
	slot.Release()

	// Fill to 9/10, then the next request crosses 90% and is rejected for
	// every protocol.
	for i := 0; i < 4; i++ {
		s, err := b.Acquire(t.Context(), max, false)
		if err != nil {
			t.Fatalf("fill %d: %v", i, err)
		}
		defer s.Release()
	}
	_, apiErr = b.Acquire(t.Context(), max, false)
	assertAPIError(t, apiErr, apierr.CodeBackpressureRejected, http.StatusServiceUnavailable)
	if apiErr.RetryAfterSeconds != 2 {
		t.Fatalf("retry_after = %d, want 2", apiErr.RetryAfterSeconds)
	}
}

func TestBackpressureDoesNotReuse429(t *testing.T) {
	// The two gates have different codes by contract: backpressure is a global
	// instance gate, concurrency_limit_exceeded is per-key.
	b := NewBackpressure(nil)
	for i := 0; i < 9; i++ {
		s, err := b.Acquire(t.Context(), 10, false)
		if err != nil {
			t.Fatalf("fill: %v", err)
		}
		defer s.Release()
	}
	_, err := b.Acquire(t.Context(), 10, false)
	if err.Code == ratelimitConcurrencyCode() {
		t.Fatal("backpressure must not return concurrency_limit_exceeded")
	}
}

func ratelimitConcurrencyCode() string { return "concurrency_limit_exceeded" }

// --- P09 / A7 (G-02) -----------------------------------------------------

func g02Context() *Ctx {
	maxTokens := int64(1000)
	return &Ctx{
		Auth: &AuthContext{UserID: 7, KeyID: 10, GroupCode: "standard", GroupRatioBP: 10000},
		Model: &ModelEntry{ID: 1, ModelName: "qwen-plus", ModelType: ModelTypeText,
			ContextLength: 100_000, MaxOutputTokens: 32_768, ListingState: ListingListed},
		// 3,996 ASCII bytes + the inter-message newline = 3,997 bytes, i.e. exactly
		// 1,000 tokens with HeuristicCounter ((3997+3)/4).
		IR: &IRRequest{
			Model:     "qwen-plus",
			Messages:  []IRMessage{{Role: "user", Content: IRContent{Text: strings.Repeat("a", 3996)}}},
			MaxTokens: &maxTokens,
		},
	}
}

func g02Deps(available int64) *Deps {
	d := staticDeps(nil)
	d.Prices = fakePrices{raw: jsonx.RawMessage(
		`{"id":11,"model_type":"TEXT","in_price_micro_per_k":3000,"out_price_micro_per_k":3000}`)}
	d.Balance = fakeBalance{micro: available}
	return d
}

func TestA7G02AdmissionUsesRawEstimateNotPreholdEstimate(t *testing.T) {
	// raw_est = (1000*3000 + 1000*3000 + 999)/1000 = 6,000 micro-credits.
	// est adds the 10% token margin and the 1.2 prehold ratio, so it is larger.
	stage := NewEstimateStage(g02Deps(6500))
	c := g02Context()
	if err := stage.Run(t.Context(), c); err != nil {
		t.Fatalf("balance 6500 with raw_est 6000 must be admitted: %v", err)
	}
	if c.Est.RawMicro != 6000 {
		t.Fatalf("raw_est = %d, want 6000", c.Est.RawMicro)
	}
	if c.Est.EstMicro <= c.Est.RawMicro {
		t.Fatalf("est = %d must exceed raw = %d", c.Est.EstMicro, c.Est.RawMicro)
	}
	t.Logf("G-02 admitted: raw_est=%d est=%d available=6500", c.Est.RawMicro, c.Est.EstMicro)

	// 5000 < 6000: 402 even though the prehold would fit under the 50% cap.
	denied := g02Context()
	err := NewEstimateStage(g02Deps(5000)).Run(t.Context(), denied)
	assertAPIError(t, err, apierr.CodeInsufficientCredits, http.StatusPaymentRequired)
	t.Logf("G-02 denied: available=5000 < raw_est=6000")
}

// --- P12 / A10 / A11 -----------------------------------------------------

// --- helpers -------------------------------------------------------------

func assertAPIError(t *testing.T, err error, code string, status int) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %s, got nil", code)
	}
	var se *StageError
	if !errors.As(err, &se) {
		if apiErr, ok := err.(*apierr.APIError); ok {
			if apiErr.Code != code || apiErr.HTTPStatus != status {
				t.Fatalf("error = %s/%d, want %s/%d", apiErr.Code, apiErr.HTTPStatus, code, status)
			}
			return
		}
		t.Fatalf("error %T is not *StageError: %v", err, err)
	}
	if se.API == nil {
		t.Fatalf("stage error without API error: %v", se.Err)
	}
	if se.API.Code != code || se.API.HTTPStatus != status {
		t.Fatalf("error = %s/%d, want %s/%d", se.API.Code, se.API.HTTPStatus, code, status)
	}
}

func TestP09RejectsEndpointModelTypeMismatch(t *testing.T) {
	d := staticDeps(nil)
	d.Prices = fakePrices{raw: jsonx.RawMessage(
		`{"id":11,"model_type":"TEXT","in_price_micro_per_k":1000,"out_price_micro_per_k":1000}`)}
	d.Balance = fakeBalance{micro: 1_000_000}
	c := &Ctx{
		Endpoint: EndpointImages,
		Auth:     &AuthContext{UserID: 7, KeyID: 10, GroupRatioBP: 10000},
		Model:    &ModelEntry{ID: 1, ModelType: ModelTypeText},
		IR:       &IRRequest{Model: "m", N: 1, ImageParams: &IRImageParams{Prompt: "x", N: 1}},
	}
	err := NewEstimateStage(d).Run(t.Context(), c)
	assertAPIError(t, err, apierr.CodePriceNotConfigured, http.StatusInternalServerError)
}

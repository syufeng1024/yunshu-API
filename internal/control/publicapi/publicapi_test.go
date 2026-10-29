package publicapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"testing"
	"time"

	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/platform/clock"
)

// fixedNow is the instant every effective-window assertion is made against.
// Tests never call time.Now, so a slow CI machine cannot flip a boundary case.
var fixedNow = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

func testService() *Service {
	return New(testDB, clock.NewFixed(fixedNow), nil)
}

func get(t *testing.T, handler http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func decodeRaw(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != publicCache {
		t.Fatalf("Cache-Control = %q, want %q", got, publicCache)
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body.Data
}

// assertKeys pins the wire shape. A renamed or newly added key fails here even
// though both sides still compile: the frontend contract is runtime-only.
func assertKeys(t *testing.T, item map[string]any, want ...string) {
	t.Helper()
	got := make([]string, 0, len(item))
	for k := range item {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("JSON keys = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("JSON keys = %v, want %v", got, want)
		}
	}
}

func findBy(t *testing.T, items []map[string]any, key string, value any) map[string]any {
	t.Helper()
	for _, item := range items {
		if item[key] == value {
			return item
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// plans
// ---------------------------------------------------------------------------

func insertPlan(t *testing.T, code string, sortOrder int64, enabled int, daily any,
	invoice, permanent int) int64 {
	t.Helper()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_plan
			(code, name, price_fen, grant_recharge_micro, grant_bonus_micro, bonus_valid_days,
			 rpm, tpm, concurrency, daily_quota_micro, model_scope_mode, invoice_supported,
			 sla_text, permanent, stackable, sort_order, enabled, created_at, updated_at)
		VALUES (?, ?, 9900, 99000000, 20000000, 90,
		        300, 630000, 110, ?, 'ALL', ?,
		        '7×24 工单受理', ?, 0, ?, ?, NOW(3), NOW(3))`,
		code, code, daily, invoice, permanent, sortOrder, enabled)
	if err != nil {
		t.Fatalf("insert plan %s: %v", code, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("plan id: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM t_plan WHERE id = ?`, id)
	})
	return id
}

func TestPlansFieldNamesNullQuotaAndEnabledFilter(t *testing.T) {
	prefix := fmt.Sprintf("ZZT%d", time.Now().UnixNano()%1_000_000)
	active := prefix + "A"
	off := prefix + "B"
	insertPlan(t, active, -10, 1, nil, 1, 0)
	insertPlan(t, off, -11, 0, int64(123), 0, 1)

	items := decodeRaw(t, get(t, testService().Plans, "/api/public/plans"))

	row := findBy(t, items, "code", active)
	if row == nil {
		t.Fatalf("enabled plan %s missing from %v", active, items)
	}
	assertKeys(t, row,
		"bonus_valid_days", "code", "concurrency", "daily_quota_micro",
		"grant_bonus_micro", "grant_recharge_micro", "invoice_supported",
		"model_scope_mode", "name", "permanent", "price_fen", "rpm",
		"sla_text", "sort_order", "tpm")

	// FREE_DEFAULT-style NULL must stay JSON null, not become 0 ("inherit").
	if value, ok := row["daily_quota_micro"]; !ok || value != nil {
		t.Fatalf("daily_quota_micro = %v (present=%v), want null", value, ok)
	}
	// tinyint(1) flags are integers on the wire, never true/false.
	if row["invoice_supported"] != float64(1) {
		t.Fatalf("invoice_supported = %v, want 1", row["invoice_supported"])
	}
	if row["permanent"] != float64(0) {
		t.Fatalf("permanent = %v, want 0", row["permanent"])
	}

	if findBy(t, items, "code", off) != nil {
		t.Fatalf("disabled plan %s leaked into the public list", off)
	}

	// sort_order ASC: our -10 row is ahead of every seeded card (0..4).
	if items[0]["code"] != active {
		t.Fatalf("first plan = %v, want %s (sort_order ASC)", items[0]["code"], active)
	}
}

func TestPlansDatabaseFailureIsInternalError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/public/plans", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	testService().Plans(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := jsonx.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Code != "internal_error" {
		t.Fatalf("code = %q, want internal_error", body.Error.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("error response is cacheable: %q", got)
	}
}

// ---------------------------------------------------------------------------
// models
// ---------------------------------------------------------------------------

func insertModel(t *testing.T, name, modelType, state, protocol string,
	contextLen, weight int64, published time.Time) int64 {
	t.Helper()
	protocolID := int64(9_999_999) // deliberately dangling
	if protocol != "" {
		if err := testDB.QueryRowContext(context.Background(),
			`SELECT id FROM t_protocol WHERE code = ?`, protocol).Scan(&protocolID); err != nil {
			t.Fatalf("protocol %s: %v", protocol, err)
		}
	}
	publishedAt := any(nil)
	if !published.IsZero() {
		publishedAt = published
	}
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_model
			(model_name, display_name, region, model_type, protocol_id, context_length,
			 capability_mask, recommend_weight, listing_state, published_at, created_at, updated_at)
		VALUES (?, ?, 'DOMESTIC', ?, ?, ?, 1, ?, ?, ?, NOW(3), NOW(3))`,
		name, name, modelType, protocolID, contextLen, weight, state, publishedAt)
	if err != nil {
		t.Fatalf("insert model %s: %v", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("model id: %v", err)
	}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_price_rule WHERE scope_id = ?`, id)
		_, _ = testDB.ExecContext(ctx, `DELETE FROM t_model WHERE id = ?`, id)
	})
	return id
}

func insertPrice(t *testing.T, modelID, version, inMicro, outMicro int64,
	from time.Time, to *time.Time, enabled int) {
	t.Helper()
	if _, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_price_rule
			(scope_level, scope_id, model_type, in_price_micro_per_k, out_price_micro_per_k,
			 per_call_price_micro, version, effective_from, effective_to, enabled,
			 created_by, created_at)
		VALUES ('MODEL', ?, 'TEXT', ?, ?, 0, ?, ?, ?, ?, 0, NOW(3))`,
		modelID, inMicro, outMicro, version, from, to, enabled); err != nil {
		t.Fatalf("insert price for model %d: %v", modelID, err)
	}
}

func TestModelsFiltersPricingProtocolsAndKeys(t *testing.T) {
	prefix := fmt.Sprintf("zzpub-%d", time.Now().UnixNano())

	openai := insertModel(t, prefix+"-openai", "TEXT", "LISTED", "OPENAI_COMPAT",
		65536, 100, fixedNow.Add(-48*time.Hour))
	insertPrice(t, openai, 1, 1500, 3000, fixedNow.Add(-90*24*time.Hour), nil, 1)
	// version 2 supersedes version 1; the public price must follow the
	// (version DESC, id DESC) rule, not the first row.
	insertPrice(t, openai, 2, 1600, 3200, fixedNow.Add(-10*24*time.Hour), nil, 1)

	anthropic := insertModel(t, prefix+"-anthropic", "TEXT", "LISTED", "ANTHROPIC_COMPAT",
		32768, 50, fixedNow.Add(-24*time.Hour))
	insertPrice(t, anthropic, 1, 1000, 2000, fixedNow.Add(-90*24*time.Hour), nil, 1)

	catalogOnly := insertModel(t, prefix+"-catalog", "TEXT", "CATALOG_ONLY", "OPENAI_COMPAT",
		8192, 90, fixedNow.Add(-24*time.Hour))
	insertPrice(t, catalogOnly, 1, 900, 1800, fixedNow.Add(-90*24*time.Hour), nil, 1)

	delisted := insertModel(t, prefix+"-delisted", "TEXT", "DELISTED", "OPENAI_COMPAT",
		8192, 80, fixedNow.Add(-24*time.Hour))
	insertPrice(t, delisted, 1, 900, 1800, fixedNow.Add(-90*24*time.Hour), nil, 1)

	insertModel(t, prefix+"-noprice", "TEXT", "LISTED", "OPENAI_COMPAT",
		8192, 70, fixedNow.Add(-24*time.Hour))

	expired := insertModel(t, prefix+"-expired", "TEXT", "LISTED", "OPENAI_COMPAT",
		8192, 60, fixedNow.Add(-24*time.Hour))
	past := fixedNow.Add(-time.Hour)
	insertPrice(t, expired, 1, 900, 1800, fixedNow.Add(-90*24*time.Hour), &past, 1)

	unknownProtocol := insertModel(t, prefix+"-noproto", "TEXT", "LISTED", "",
		4096, 0, fixedNow.Add(-72*time.Hour))
	insertPrice(t, unknownProtocol, 1, 700, 800, fixedNow.Add(-90*24*time.Hour), nil, 1)

	items := decodeRaw(t, get(t, testService().Models, "/api/public/models"))

	byName := func(name string) map[string]any {
		return findBy(t, items, "model_name", name)
	}

	row := byName(prefix + "-openai")
	if row == nil {
		t.Fatalf("LISTED priced model missing: %v", items)
	}
	assertKeys(t, row,
		"context_window", "display_name", "id", "input_price_micro_per_k",
		"listing_state", "model_name", "model_type", "output_price_micro_per_k",
		"protocols", "published_at", "recommend_weight")

	if row["id"] != strconv.FormatInt(openai, 10) {
		t.Fatalf("id = %v (%T), want string %d", row["id"], row["id"], openai)
	}
	if row["context_window"] != float64(65536) {
		t.Fatalf("context_window = %v, want 65536", row["context_window"])
	}
	if row["input_price_micro_per_k"] != float64(1600) ||
		row["output_price_micro_per_k"] != float64(3200) {
		t.Fatalf("price = %v/%v, want the version-2 rule 1600/3200",
			row["input_price_micro_per_k"], row["output_price_micro_per_k"])
	}
	assertProtocols(t, row, "OPENAI")
	if _, err := time.Parse(time.RFC3339, row["published_at"].(string)); err != nil {
		t.Fatalf("published_at %v is not RFC3339: %v", row["published_at"], err)
	}

	assertProtocols(t, byName(prefix+"-anthropic"), "ANTHROPIC")
	assertProtocols(t, byName(prefix+"-noproto"))

	for _, name := range []string{"-catalog", "-delisted", "-noprice", "-expired"} {
		if got := byName(prefix + name); got != nil {
			t.Fatalf("%s must not appear in the public catalogue: %v", name, got)
		}
	}

	// (recommend_weight DESC): openai(100) before anthropic(50) before noproto(0).
	order := make(map[string]int, len(items))
	for i, item := range items {
		order[item["model_name"].(string)] = i
	}
	if !(order[prefix+"-openai"] < order[prefix+"-anthropic"] &&
		order[prefix+"-anthropic"] < order[prefix+"-noproto"]) {
		t.Fatalf("recommend_weight ordering broken: %v", order)
	}
}

func assertProtocols(t *testing.T, row map[string]any, want ...string) {
	t.Helper()
	if row == nil {
		t.Fatal("row is missing")
	}
	raw, ok := row["protocols"].([]any)
	if !ok {
		t.Fatalf("protocols is not an array: %v", row["protocols"])
	}
	if len(raw) != len(want) {
		t.Fatalf("protocols = %v, want %v", raw, want)
	}
	for i := range want {
		if raw[i] != want[i] {
			t.Fatalf("protocols = %v, want %v", raw, want)
		}
	}
}

// ---------------------------------------------------------------------------
// announcements
// ---------------------------------------------------------------------------

func insertAnnouncement(t *testing.T, title, content, annType string,
	from time.Time, to *time.Time, state string, createdAt time.Time, deletedAt *time.Time) int64 {
	t.Helper()
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_announcement
			(title, content, ann_type, position_mask, pinned, effective_from, effective_to,
			 state, created_by, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, 1, 0, ?, ?, ?, 0, ?, ?, ?)`,
		title, content, annType, from, to, state, createdAt, createdAt, deletedAt)
	if err != nil {
		t.Fatalf("insert announcement %s: %v", title, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("announcement id: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM t_announcement WHERE id = ?`, id)
	})
	return id
}

func TestAnnouncementsWindowStateMappingAndKeys(t *testing.T) {
	prefix := fmt.Sprintf("zzann-%d", time.Now().UnixNano())
	created := fixedNow.Add(-72 * time.Hour)

	inWindow := insertAnnouncement(t, prefix+"-in", "body-a", "PRICE_CHANGE",
		fixedNow.Add(-time.Hour), nil, "PUBLISHED", created, nil)
	older := insertAnnouncement(t, prefix+"-older", "body-b", "NOTICE",
		fixedNow.Add(-2*time.Hour), ptrTime(fixedNow.Add(time.Hour)), "PUBLISHED", created, nil)

	future := insertAnnouncement(t, prefix+"-future", "body-c", "NOTICE",
		fixedNow.Add(time.Hour), nil, "PUBLISHED", created, nil)
	expired := insertAnnouncement(t, prefix+"-expired", "body-d", "NOTICE",
		fixedNow.Add(-3*time.Hour), ptrTime(fixedNow.Add(-time.Hour)), "PUBLISHED", created, nil)
	draft := insertAnnouncement(t, prefix+"-draft", "body-e", "NOTICE",
		fixedNow.Add(-time.Hour), nil, "DRAFT", created, nil)
	deleted := insertAnnouncement(t, prefix+"-deleted", "body-f", "NOTICE",
		fixedNow.Add(-time.Hour), nil, "PUBLISHED", created, ptrTime(fixedNow.Add(-time.Minute)))

	items := decodeRaw(t, get(t, testService().Announcements, "/api/public/announcements"))

	row := findBy(t, items, "id", strconv.FormatInt(inWindow, 10))
	if row == nil {
		t.Fatalf("published in-window announcement missing: %v", items)
	}
	assertKeys(t, row, "body", "category", "effective_at", "id", "published_at", "title")
	if row["body"] != "body-a" {
		t.Fatalf("body = %v, want content column body-a", row["body"])
	}
	if row["category"] != "PRICE" {
		t.Fatalf("PRICE_CHANGE category = %v, want PRICE", row["category"])
	}
	if row["title"] != prefix+"-in" {
		t.Fatalf("title = %v", row["title"])
	}
	if _, err := time.Parse(time.RFC3339, row["effective_at"].(string)); err != nil {
		t.Fatalf("effective_at %v is not RFC3339: %v", row["effective_at"], err)
	}
	if _, err := time.Parse(time.RFC3339, row["published_at"].(string)); err != nil {
		t.Fatalf("published_at %v is not RFC3339: %v", row["published_at"], err)
	}

	olderRow := findBy(t, items, "id", strconv.FormatInt(older, 10))
	if olderRow == nil {
		t.Fatal("second in-window announcement missing")
	}
	if olderRow["category"] != "GENERAL" {
		t.Fatalf("NOTICE category = %v, want GENERAL", olderRow["category"])
	}
	if findIndex(items, inWindow) > findIndex(items, older) {
		t.Fatalf("effective_from DESC ordering broken: in=%d older=%d",
			findIndex(items, inWindow), findIndex(items, older))
	}

	for _, id := range []int64{future, expired, draft, deleted} {
		if findBy(t, items, "id", strconv.FormatInt(id, 10)) != nil {
			t.Fatalf("announcement %d must not be public (future/expired/draft/deleted)", id)
		}
	}
}

func findIndex(items []map[string]any, id int64) int {
	want := strconv.FormatInt(id, 10)
	for i, item := range items {
		if item["id"] == want {
			return i
		}
	}
	return len(items)
}

func ptrTime(v time.Time) *time.Time { return &v }

// ---------------------------------------------------------------------------
// empty / integration-link routes
// ---------------------------------------------------------------------------

// TestEndpointsAndIntegrationLinksAreHonestlyEmpty keeps its name for history,
// but "Empty" now applies only to /api/public/endpoints: t_endpoint still does
// not exist (D-24), while /api/public/integration-links reads t_integration
// since 0026 and serves the enabled rows. Renaming the test would hide that the
// earlier expectation deliberately changed.
func TestEndpointsAndIntegrationLinksAreHonestlyEmpty(t *testing.T) {
	svc := testService()

	t.Run("/api/public/endpoints", func(t *testing.T) {
		rec := get(t, svc.Endpoints, "/api/public/endpoints")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != publicCache {
			t.Fatalf("Cache-Control = %q", got)
		}
		if got := rec.Body.String(); got != `{"data":[]}` {
			t.Fatalf("body = %s, want an empty data array", got)
		}
	})

	// cursor = enabled with a template, nextchat = enabled with a NULL template,
	// claude-code = disabled. Different rows from the ops package's tests so the
	// two packages can run in parallel against the same test schema.
	const cursorTemplate = "https://cursor.example/import/{key}"
	ctx := context.Background()
	if _, err := testDB.ExecContext(ctx,
		`UPDATE t_integration SET enabled = 1, deep_link_template = ? WHERE tool_key = 'cursor'`,
		cursorTemplate); err != nil {
		t.Fatalf("enable cursor: %v", err)
	}
	if _, err := testDB.ExecContext(ctx,
		`UPDATE t_integration SET enabled = 1, deep_link_template = NULL WHERE tool_key = 'nextchat'`); err != nil {
		t.Fatalf("enable nextchat: %v", err)
	}
	if _, err := testDB.ExecContext(ctx,
		`UPDATE t_integration SET enabled = 0, deep_link_template = NULL WHERE tool_key = 'claude-code'`); err != nil {
		t.Fatalf("disable claude-code: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(),
			`UPDATE t_integration SET enabled = 0, deep_link_template = NULL
			  WHERE tool_key IN ('cursor','nextchat','claude-code')`)
	})

	t.Run("/api/public/integration-links", func(t *testing.T) {
		items := decodeRaw(t, get(t, svc.IntegrationLinks, "/api/public/integration-links"))

		cursor := findBy(t, items, "tool_key", "cursor")
		if cursor == nil {
			t.Fatalf("enabled cursor missing from %v", items)
		}
		// Wire shape only: no id, timestamps or display_name.
		assertKeys(t, cursor, "deep_link_template", "enabled", "tool_key")
		if cursor["enabled"] != true {
			t.Fatalf("cursor enabled = %v, want true", cursor["enabled"])
		}
		if cursor["deep_link_template"] != cursorTemplate {
			t.Fatalf("cursor template = %v, want %q", cursor["deep_link_template"], cursorTemplate)
		}

		// A NULL template must stay JSON null, not become "".
		next := findBy(t, items, "tool_key", "nextchat")
		if next == nil {
			t.Fatalf("enabled nextchat missing from %v", items)
		}
		if value, ok := next["deep_link_template"]; !ok || value != nil {
			t.Fatalf("nextchat template = %v (present=%v), want null", value, ok)
		}

		// The disabled row is filtered by the query, not by the client.
		if findBy(t, items, "tool_key", "claude-code") != nil {
			t.Fatalf("disabled claude-code leaked into %v", items)
		}
		for _, item := range items {
			if item["enabled"] != true {
				t.Fatalf("public integration link is not enabled: %v", item)
			}
		}
	})
}

package acceptance

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/control/ticket"
	"yunshu.dev/core/internal/gw"
	"yunshu.dev/core/internal/jobs"
	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/redisx"
)

// Every test name starts with TestCI_ so `grep -c TestCI_` is the
// machine-checkable count the T21 card asks for. The failure text always names
// the F-number first, because a red build must say which promise broke.

// F4-2 — one-click import must never expose a plaintext key.
//
// The Go API has no /api/v1/integrations/import-credentials route yet (the
// contract is still an assumed one in docs/tasks/README.md), so the API-side
// assertion is: key creation returns the plaintext exactly once, and every
// other surface the import flow reads (list, detail, key audit, server log
// payload) is masked. The web deep-link builder is checked statically: it may
// not touch browser storage, and analytics may receive only the tool key.
// The DOM/URL/localStorage half of F4-2 belongs to the Playwright segment.
// TODO(T21-2): replace the static source check with e2e/security Playwright.
func TestCI_F04_2(t *testing.T) {
	uid := newUser(t, userOpts{})
	keyID, plaintext := newUserKey(t, uid, "f4-2")
	if !strings.HasPrefix(plaintext, "sk-") {
		t.Fatalf("F4-2: created key %q is not an sk- key", plaintext)
	}

	list := call(t, uid, http.MethodGet, "/api/console/keys", nil)
	requireStatus(t, "F4-2 list", list, http.StatusOK)
	if strings.Contains(list.Body.String(), plaintext) {
		t.Fatalf("F4-2: key list leaked the plaintext key")
	}

	detail := call(t, uid, http.MethodGet, fmt.Sprintf("/api/console/keys/%d", keyID), nil)
	requireStatus(t, "F4-2 detail", detail, http.StatusOK)
	if strings.Contains(detail.Body.String(), plaintext) {
		t.Fatalf("F4-2: key detail leaked the plaintext key")
	}

	var hash, prefix, last4 string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT key_hash, key_prefix, key_last4 FROM t_api_key WHERE id = ?`, keyID,
	).Scan(&hash, &prefix, &last4); err != nil {
		t.Fatalf("F4-2: read stored key: %v", err)
	}
	if hash == plaintext || strings.Contains(hash, plaintext) || strings.Contains(prefix, plaintext) {
		t.Fatalf("F4-2: t_api_key stores the plaintext")
	}

	var after string
	err := testDB.QueryRowContext(context.Background(),
		`SELECT COALESCE(after_json, '') FROM t_api_key_audit
		  WHERE api_key_id = ? AND action = 'CREATE'`, keyID).Scan(&after)
	if err != nil && err != sql.ErrNoRows {
		t.Fatalf("F4-2: read key audit: %v", err)
	}
	if strings.Contains(after, plaintext) {
		t.Fatalf("F4-2: key audit row leaked the plaintext key")
	}

	// Static half: the deep-link module must not persist credentials and its
	// analytics hook may carry only tool_key.
	deepLink, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "lib", "deepLink.ts"))
	if err != nil {
		t.Fatalf("F4-2: read deepLink.ts: %v", err)
	}
	src := string(deepLink)
	for _, banned := range []string{"localStorage", "sessionStorage", "document.cookie"} {
		if strings.Contains(src, banned) {
			t.Fatalf("F4-2: deepLink.ts uses %s; credentials must not be persisted", banned)
		}
	}
	if !strings.Contains(src, "tool_key: toolKey") {
		t.Fatalf("F4-2: analytics payload no longer proves it carries only tool_key")
	}

	// The assumed import endpoint is not implemented; a future implementation
	// must be registered before the Playwright half can run.
	missing := call(t, uid, http.MethodPost, "/api/v1/integrations/import-credentials",
		map[string]any{"tool_key": "cursor"})
	requireStatus(t, "F4-2 import endpoint", missing, http.StatusNotFound)
	t.Logf("F4-2: key %d plaintext appears only in the create response; DOM/URL/localStorage coverage is deferred to the Playwright segment", keyID)
}

// F7-1 — user B must not read user A's key.
func TestCI_F07_1(t *testing.T) {
	alice := newUser(t, userOpts{})
	bob := newUser(t, userOpts{})
	keyID, plaintext := newUserKey(t, alice, "f7-1")

	for _, tc := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, fmt.Sprintf("/api/console/keys/%d", keyID), nil},
		{http.MethodPatch, fmt.Sprintf("/api/console/keys/%d", keyID), map[string]any{"key_name": "stolen"}},
		{http.MethodDelete, fmt.Sprintf("/api/console/keys/%d", keyID), map[string]any{"confirm": true}},
	} {
		rec := call(t, bob, tc.method, tc.path, tc.body)
		body := requireCode(t, "F7-1 "+tc.method, rec, http.StatusNotFound, "key_not_found")
		if strings.Contains(rec.Body.String(), plaintext) || body.Error.Message == "" {
			t.Fatalf("F7-1: cross-account %s leaked key material", tc.method)
		}
	}

	own := call(t, alice, http.MethodGet, fmt.Sprintf("/api/console/keys/%d", keyID), nil)
	requireStatus(t, "F7-1 owner read", own, http.StatusOK)
	if strings.Contains(own.Body.String(), plaintext) {
		t.Fatalf("F7-1: owner detail leaked the plaintext")
	}

	var name, group string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT key_name, group_code FROM t_api_key WHERE id = ?`, keyID).Scan(&name, &group); err != nil {
		t.Fatalf("F7-1: read key: %v", err)
	}
	if name == "stolen" || group != "standard" {
		t.Fatalf("F7-1: a cross-account write changed the key (name=%q group=%q)", name, group)
	}
}

// F7-4 — group_code is server-owned; its mere presence is rejected.
func TestCI_F07_4(t *testing.T) {
	uid := newUser(t, userOpts{})
	keyID, _ := newUserKey(t, uid, "f7-4")

	rec := call(t, uid, http.MethodPatch, fmt.Sprintf("/api/console/keys/%d", keyID),
		map[string]any{"group_code": "internal"})
	body := requireCode(t, "F7-4 patch", rec, http.StatusBadRequest, "invalid_request_body")
	if !strings.Contains(body.Error.Message, "group_code") && !strings.Contains(body.Error.Hint, "group_code") {
		t.Fatalf("F7-4: refusal does not name group_code: %+v", body.Error)
	}

	create := call(t, uid, http.MethodPost, "/api/console/keys", map[string]any{
		"name": "f7-4-create", "model_scope_mode": "ALL",
		"expire_at": futureRFC3339(24 * time.Hour), "group_code": "internal",
	})
	requireCode(t, "F7-4 create", create, http.StatusBadRequest, "invalid_request_body")

	var stored string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT group_code FROM t_api_key WHERE id = ?`, keyID).Scan(&stored); err != nil {
		t.Fatalf("F7-4: read key: %v", err)
	}
	if stored != "standard" {
		t.Fatalf("F7-4: group_code changed to %q", stored)
	}
}

// F14-2 — upstream keys are masked in every response and sealed in the DB.
func TestCI_F14_2(t *testing.T) {
	ops := newUser(t, userOpts{role: "OPS"})
	name := fmt.Sprintf("t21-ch-%d", nextInt())
	plaintext := "sk-upstream-secret-1234"
	rec := call(t, ops, http.MethodPost, "/api/admin/channels", channelBody(t, name, plaintext))
	requireStatus(t, "F14-2 create channel", rec, http.StatusCreated)

	var created struct {
		Channel struct {
			ID      int64  `json:"id"`
			Mask    string `json:"upstream_key_mask"`
			BaseURL string `json:"base_url"`
		} `json:"channel"`
	}
	decodeBody(t, rec, &created)
	if created.Channel.ID == 0 {
		t.Fatalf("F14-2: channel id missing: %s", rec.Body.String())
	}
	t.Cleanup(func() { dropChannel(t, created.Channel.ID) })

	if strings.Contains(rec.Body.String(), plaintext) {
		t.Fatalf("F14-2: create response leaked the upstream key")
	}
	if created.Channel.Mask != "sk-****1234" {
		t.Fatalf("F14-2: mask=%q, want sk-****1234", created.Channel.Mask)
	}

	var enc []byte
	var mask string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT upstream_key_enc, upstream_key_mask FROM t_channel WHERE id = ?`,
		created.Channel.ID).Scan(&enc, &mask); err != nil {
		t.Fatalf("F14-2: read channel: %v", err)
	}
	if string(enc) == plaintext || strings.Contains(string(enc), plaintext) {
		t.Fatalf("F14-2: upstream_key_enc contains plaintext")
	}
	if !strings.HasPrefix(string(enc), "v1:") {
		t.Fatalf("F14-2: upstream_key_enc is not a v1 envelope: %q", string(enc))
	}
	if mask != "sk-****1234" {
		t.Fatalf("F14-2: stored mask=%q", mask)
	}

	detail := call(t, ops, http.MethodGet, fmt.Sprintf("/api/admin/channels/%d", created.Channel.ID), nil)
	requireStatus(t, "F14-2 detail", detail, http.StatusOK)
	if strings.Contains(detail.Body.String(), plaintext) {
		t.Fatalf("F14-2: channel detail leaked the upstream key")
	}
}

// F19-1 — the request picker is 90 days and the caller's own rows only, and
// submitting a request_id re-checks both predicates.
func TestCI_F19_1(t *testing.T) {
	alice := newUser(t, userOpts{})
	bob := newUser(t, userOpts{})
	modelID := newModel(t, modelName(), "LISTED")

	ownRecent, ownOld, foreign := rid(), rid(), rid()
	now := time.Now().UTC()
	insertUsage(t, ownRecent, alice, modelID, now.Add(-24*time.Hour), "SUCCESS", 1)
	insertUsage(t, ownOld, alice, modelID, now.Add(-100*24*time.Hour), "SUCCESS", 1)
	insertUsage(t, foreign, bob, modelID, now.Add(-24*time.Hour), "SUCCESS", 1)

	picker := call(t, alice, http.MethodGet, "/api/console/request-ids?page_size=100", nil)
	requireStatus(t, "F19-1 picker", picker, http.StatusOK)
	picked := picker.Body.String()
	if !strings.Contains(picked, ownRecent) {
		t.Fatalf("F19-1: picker omitted the caller's recent request")
	}
	if strings.Contains(picked, foreign) {
		t.Fatalf("F19-1: picker exposed another account's request")
	}
	if strings.Contains(picked, ownOld) {
		t.Fatalf("F19-1: picker exposed a request older than 90 days")
	}

	for _, tc := range []struct {
		name string
		id   string
	}{
		{"foreign", foreign},
		{"older than 90 days", ownOld},
	} {
		rec := call(t, alice, http.MethodPost, "/api/console/tickets", map[string]any{
			"ticket_type": "OTHER", "title": "F19-1 " + tc.name,
			"description": "should be refused", "request_id": tc.id,
		})
		requireCode(t, "F19-1 submit "+tc.name, rec, http.StatusNotFound, "resource_not_found")
	}

	ok := call(t, alice, http.MethodPost, "/api/console/tickets", map[string]any{
		"ticket_type": "OTHER", "title": "F19-1 own request",
		"description": "own recent request", "request_id": ownRecent,
	})
	requireStatus(t, "F19-1 submit own", ok, http.StatusCreated)
}

// F6-7 — the free tier stops at three active keys; disabling one frees a slot.
func TestCI_F06_7(t *testing.T) {
	uid := newUser(t, userOpts{})
	modelID := newModel(t, modelName(), "LISTED")
	_ = modelID

	var ids []int64
	for i := 0; i < 3; i++ {
		id, _ := newUserKey(t, uid, fmt.Sprintf("f6-7-%d", i))
		ids = append(ids, id)
	}

	fourth := call(t, uid, http.MethodPost, "/api/console/keys", map[string]any{
		"name": "f6-7-fourth", "model_scope_mode": "ALL",
		"expire_at": futureRFC3339(24 * time.Hour),
	})
	body := requireCode(t, "F6-7 fourth", fourth, http.StatusConflict, "key_limit_reached")
	if !strings.Contains(body.Error.Message, "3") {
		t.Fatalf("F6-7: refusal does not state the ceiling: %q", body.Error.Message)
	}

	disable := call(t, uid, http.MethodPatch, fmt.Sprintf("/api/console/keys/%d", ids[0]),
		map[string]any{"status": "DISABLED", "confirm": true})
	requireStatus(t, "F6-7 disable", disable, http.StatusOK)

	again := call(t, uid, http.MethodPost, "/api/console/keys", map[string]any{
		"name": "f6-7-after-disable", "model_scope_mode": "ALL",
		"expire_at": futureRFC3339(24 * time.Hour),
	})
	requireStatus(t, "F6-7 create after disable", again, http.StatusCreated)
}

// F11-3 — request bodies are deleted after 180 days, metadata after 365.
func TestCI_F11_3(t *testing.T) {
	// t_request_body comes from migration 0022; the retention column is
	// expires_at (= created_at + log.retention_days), not created_at.
	uid := newUser(t, userOpts{})
	modelID := newModel(t, modelName(), "LISTED")
	now := time.Now().UTC()
	bodyCut := now.AddDate(0, 0, -180)
	metaCut := now.AddDate(0, 0, -365)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(),
			`DELETE FROM t_request_body WHERE user_id = ?`, uid)
	})

	insertBody := func(id string, createdAt, expiresAt time.Time) {
		if _, err := testDB.ExecContext(context.Background(),
			`INSERT INTO t_request_body
			   (request_id, user_id, ciphertext, body_key_id, created_at, expires_at)
			 VALUES (?,?,?, 'body-v1', ?, ?)`,
			id, uid, []byte("x"), createdAt, expiresAt); err != nil {
			t.Fatalf("F11-3: insert body: %v", err)
		}
	}
	oldBody, edgeBody, keepBody := rid(), rid(), rid()
	insertBody(oldBody, bodyCut.Add(-time.Hour), now.Add(-time.Hour))
	insertBody(edgeBody, bodyCut.Add(time.Hour), now.Add(time.Hour))
	insertBody(keepBody, now.Add(-24*time.Hour), now.AddDate(0, 0, 156))

	oldMeta, edgeMeta, keepMeta := rid(), rid(), rid()
	insertUsage(t, oldMeta, uid, modelID, metaCut.Add(-time.Hour), "SUCCESS", 1)
	insertUsage(t, edgeMeta, uid, modelID, metaCut.Add(time.Hour), "SUCCESS", 1)
	insertUsage(t, keepMeta, uid, modelID, now.Add(-24*time.Hour), "SUCCESS", 1)

	if _, err := jobs.NewPurgeLog(testDB, nil, clock.Real{}).Run(context.Background()); err != nil {
		t.Fatalf("F11-3: purge: %v", err)
	}

	exists := func(query string, id string) bool {
		var n int
		if err := testDB.QueryRowContext(context.Background(), query, id).Scan(&n); err != nil {
			t.Fatalf("F11-3: count: %v", err)
		}
		return n > 0
	}
	if exists(`SELECT COUNT(*) FROM t_request_body WHERE request_id = ?`, oldBody) {
		t.Fatalf("F11-3: a 181-day-old body survived the purge")
	}
	if !exists(`SELECT COUNT(*) FROM t_request_body WHERE request_id = ?`, edgeBody) ||
		!exists(`SELECT COUNT(*) FROM t_request_body WHERE request_id = ?`, keepBody) {
		t.Fatalf("F11-3: a body inside the 180-day window was deleted")
	}
	if exists(`SELECT COUNT(*) FROM t_usage_record WHERE request_id = ?`, oldMeta) {
		t.Fatalf("F11-3: a 366-day-old usage row survived the purge")
	}
	if !exists(`SELECT COUNT(*) FROM t_usage_record WHERE request_id = ?`, edgeMeta) ||
		!exists(`SELECT COUNT(*) FROM t_usage_record WHERE request_id = ?`, keepMeta) {
		t.Fatalf("F11-3: a usage row inside the 365-day window was deleted")
	}
}

// F14-4 — a channel visible only to one group is never routed to another.
func TestCI_F14_4(t *testing.T) {
	ops := newUser(t, userOpts{role: "OPS"})
	modelID := newModel(t, modelName(), "LISTED")
	ch := createChannelAPI(t, ops, "f14-4")

	contractGroup := insertGroup(t, "CONTRACT", "SELECTED", []int64{ch})
	standardGroup := insertGroup(t, "STANDARD", "SELECTED", nil)
	insertRoute(t, modelID, ch)

	publishRoutes(t)
	catalog := liveCatalog(t)

	routed := routeChannels(t, catalog, modelID, contractGroup, 1001)
	if len(routed) != 1 || routed[0] != ch {
		t.Fatalf("F14-4: contract group candidates = %v, want [%d]", routed, ch)
	}
	err := routeError(t, catalog, modelID, standardGroup, 1002)
	if err == nil {
		t.Fatalf("F14-4: standard group was routed to a contract-only channel")
	}
	if !strings.Contains(err.Error(), "no_available_upstream") {
		t.Fatalf("F14-4: standard group error = %v, want no_available_upstream*", err)
	}
}

// F14-5 — V1.0 refuses an overseas channel at save time.
func TestCI_F14_5(t *testing.T) {
	ops := newUser(t, userOpts{role: "OPS"})
	body := channelBody(t, fmt.Sprintf("t21-overseas-%d", nextInt()), "sk-overseas-1234")
	body["region"] = "OVERSEAS"
	rec := call(t, ops, http.MethodPost, "/api/admin/channels", body)
	msg := requireCode(t, "F14-5 overseas", rec, http.StatusBadRequest, "invalid_request_body")
	if !strings.Contains(msg.Error.Message, "OVERSEAS") {
		t.Fatalf("F14-5: refusal does not explain the overseas ban: %q", msg.Error.Message)
	}

	ok := call(t, ops, http.MethodPost, "/api/admin/channels", channelBody(t, fmt.Sprintf("t21-domestic-%d", nextInt()), "sk-domestic-1234"))
	requireStatus(t, "F14-5 domestic", ok, http.StatusCreated)
	var created struct {
		Channel struct {
			ID int64 `json:"id"`
		} `json:"channel"`
	}
	decodeBody(t, ok, &created)
	t.Cleanup(func() { dropChannel(t, created.Channel.ID) })
}

// F14-6 — a dedicated channel is invisible outside its allowlist; after it is
// disabled the shared fallback is still visible.
func TestCI_F14_6(t *testing.T) {
	ops := newUser(t, userOpts{role: "OPS"})
	listed := newUser(t, userOpts{})
	outsider := newUser(t, userOpts{})
	modelID := newModel(t, modelName(), "LISTED")

	publicCh := createChannelAPI(t, ops, "f14-6-public")
	dedicatedCh := createChannelAPI(t, ops, "f14-6-dedicated")
	quote := listed
	setChannelDedicated(t, dedicatedCh, quote)
	insertRoute(t, modelID, publicCh)
	insertRoute(t, modelID, dedicatedCh)

	publishRoutes(t)
	catalog := liveCatalog(t)

	inside := routeChannels(t, catalog, modelID, "standard", listed)
	if len(inside) != 2 {
		t.Fatalf("F14-6: listed user candidates = %v, want both channels", inside)
	}
	out := routeChannels(t, catalog, modelID, "standard", outsider)
	if len(out) != 1 || out[0] != publicCh {
		t.Fatalf("F14-6: outsider candidates = %v, want only public %d", out, publicCh)
	}

	disable := call(t, ops, http.MethodPost, fmt.Sprintf("/api/admin/channels/%d/disable", dedicatedCh), map[string]any{})
	requireStatus(t, "F14-6 disable dedicated", disable, http.StatusOK)
	publishRoutes(t)
	catalog = liveCatalog(t)

	fallback := routeChannels(t, catalog, modelID, "standard", listed)
	if len(fallback) != 1 || fallback[0] != publicCh {
		t.Fatalf("F14-6: after disable, listed user candidates = %v, want the public fallback only", fallback)
	}
}

// F16-4 — an increase needs 7 days' notice; a decrease may be immediate.
func TestCI_F16_4(t *testing.T) {
	ops := newUser(t, userOpts{role: "OPS"})
	modelID := newModel(t, modelName(), "LISTED")
	priceID := createPriceAPI(t, ops, modelID, 10000, 20000, 5000)

	raise := call(t, ops, http.MethodPatch, fmt.Sprintf("/api/admin/prices/%d", priceID), map[string]any{
		"in_price_micro_per_k": 12000,
		"effective_from":       time.Now().UTC().Add(3 * 24 * time.Hour).Format(time.RFC3339),
	})
	msg := requireCode(t, "F16-4 raise", raise, http.StatusBadRequest, "invalid_request_body")
	if !strings.Contains(msg.Error.Message, "7 天") {
		t.Fatalf("F16-4: raise refusal does not state the 7-day notice: %q", msg.Error.Message)
	}

	drop := call(t, ops, http.MethodPatch, fmt.Sprintf("/api/admin/prices/%d", priceID), map[string]any{
		"in_price_micro_per_k": 9000,
		"effective_from":       time.Now().UTC().Format(time.RFC3339),
	})
	requireStatus(t, "F16-4 decrease", drop, http.StatusOK)

	var stored int64
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT in_price_micro_per_k FROM t_price_rule WHERE id = ?`, priceID).Scan(&stored); err != nil {
		t.Fatalf("F16-4: read price: %v", err)
	}
	if stored != 9000 {
		t.Fatalf("F16-4: stored price = %d, want 9000 after the immediate decrease", stored)
	}
}

// F16-6 — two overlapping price sources are refused.
func TestCI_F16_6(t *testing.T) {
	ops := newUser(t, userOpts{role: "OPS"})
	modelID := newModel(t, modelName(), "LISTED")
	// The create path clamps a past effective_from to "now", so a second rule
	// with the same past instant would become a predecessor instead of an
	// overlap. Schedule both rules for the same future instant.
	first := call(t, ops, http.MethodPost, "/api/admin/prices", map[string]any{
		"model_id":              modelID,
		"in_price_micro_per_k":  10000,
		"out_price_micro_per_k": 20000,
		"cost_in_micro_per_k":   5000,
		"cost_out_micro_per_k":  5000,
		"effective_from":        time.Now().UTC().Add(8 * 24 * time.Hour).Format(time.RFC3339),
	})
	requireStatus(t, "F16-6 first price", first, http.StatusCreated)
	var firstBody struct {
		Price struct {
			ID int64 `json:"id"`
		} `json:"price"`
	}
	decodeBody(t, first, &firstBody)
	firstID := firstBody.Price.ID

	// Start the second rule at the first rule's own effective instant. A later
	// start is a normal changeover (the predecessor is closed), not a conflict.
	var firstFrom time.Time
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT effective_from FROM t_price_rule WHERE id = ?`, firstID).Scan(&firstFrom); err != nil {
		t.Fatalf("F16-6: read first effective_from: %v", err)
	}
	second := call(t, ops, http.MethodPost, "/api/admin/prices", map[string]any{
		"model_id":              modelID,
		"in_price_micro_per_k":  9000,
		"out_price_micro_per_k": 18000,
		"cost_in_micro_per_k":   4000,
		"cost_out_micro_per_k":  4000,
		"confirm_no_cost":       false,
		"effective_from":        firstFrom.UTC().Format(time.RFC3339Nano),
	})
	msg := requireCode(t, "F16-6 conflict", second, http.StatusConflict, "price_conflict")
	if !strings.Contains(msg.Error.Message, fmt.Sprintf("t_price_rule#%d", firstID)) {
		t.Fatalf("F16-6: conflict does not name the first source: %q", msg.Error.Message)
	}
}

// F19-4 — an account with an OPEN or RESPONDED ticket cannot be cancelled.
//
// There is no cancellation endpoint in V1.0 (T16 receipt: "仓库无注销接口"),
// so the cross-package assertion drives the real ticket service's exported
// pre-flight. The ticket itself is created and answered through the real mux.
func TestCI_F19_4(t *testing.T) {
	user := newUser(t, userOpts{})
	ops := newUser(t, userOpts{role: "OPS"})

	create := call(t, user, http.MethodPost, "/api/console/tickets", map[string]any{
		"ticket_type": "OTHER", "title": "F19-4 cancellation blocker",
		"description": "leave this one answered but open",
	})
	requireStatus(t, "F19-4 create ticket", create, http.StatusCreated)
	var created struct {
		Ticket struct {
			ID       int64  `json:"id"`
			TicketNo string `json:"ticket_no"`
		} `json:"ticket"`
	}
	decodeBody(t, create, &created)

	reply := call(t, ops, http.MethodPost, fmt.Sprintf("/api/admin/tickets/%d/reply", created.Ticket.ID),
		map[string]any{"content": "we are looking into it", "is_internal": false})
	requireStatus(t, "F19-4 operator reply", reply, http.StatusOK)

	svc := ticket.New(testDB, nil, clock.Real{}, nil)
	err := svc.CheckAccountCancellable(context.Background(), user)
	if err == nil {
		t.Fatalf("F19-4: cancellation allowed while ticket %s is RESPONDED", created.Ticket.TicketNo)
	}
	outstanding, ok := err.(*ticket.OutstandingTicketsError)
	if !ok || len(outstanding.TicketNos) != 1 || outstanding.TicketNos[0] != created.Ticket.TicketNo {
		t.Fatalf("F19-4: refusal = %v, want the blocking ticket number %s", err, created.Ticket.TicketNo)
	}

	closeRec := call(t, user, http.MethodPost, fmt.Sprintf("/api/console/tickets/%d/close", created.Ticket.ID),
		map[string]any{"reason": "resolved"})
	requireStatus(t, "F19-4 close", closeRec, http.StatusOK)
	if err := svc.CheckAccountCancellable(context.Background(), user); err != nil {
		t.Fatalf("F19-4: cancellation still blocked after the ticket was closed: %v", err)
	}
}

// F20-3 — the applicant may never decide their own document.
func TestCI_F20_3(t *testing.T) {
	plan := planID(t, "STANDARD")
	admin := newUser(t, userOpts{role: "SUPER_ADMIN", planID: &plan})

	apply := call(t, admin, http.MethodPost, "/api/console/quota-applies", map[string]any{
		"target_quota_micro": int64(60_000_000),
		"apply_reason":       "F20-3 self-audit probe",
	})
	requireStatus(t, "F20-3 submit", apply, http.StatusCreated)
	var created struct {
		Apply struct {
			ID int64 `json:"id"`
		} `json:"apply"`
	}
	decodeBody(t, apply, &created)

	var taskID int64
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT id FROM t_review_task
		 WHERE biz_type = 'QUOTA_APPLY' AND biz_id = ?`, created.Apply.ID).Scan(&taskID); err != nil {
		t.Fatalf("F20-3: review task: %v", err)
	}

	approve := call(t, admin, http.MethodPost, fmt.Sprintf("/api/admin/reviews/%d/approve", taskID),
		map[string]any{"reason": "self approval"})
	requireCode(t, "F20-3 self approve", approve, http.StatusForbidden, "permission_denied")

	var state string
	if err := testDB.QueryRowContext(context.Background(),
		`SELECT state FROM t_quota_apply WHERE id = ?`, created.Apply.ID).Scan(&state); err != nil {
		t.Fatalf("F20-3: read apply: %v", err)
	}
	if state != "PENDING" {
		t.Fatalf("F20-3: self-audit moved the apply to %s", state)
	}

	var auditResult int
	if err := testDB.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM t_admin_audit_log
		 WHERE operator_id = ? AND module = 'REVIEW' AND action = 'APPROVE'
		   AND target_id = ? AND result = 'REJECTED'`, admin, strconv.FormatInt(taskID, 10)).Scan(&auditResult); err != nil {
		t.Fatalf("F20-3: audit: %v", err)
	}
	if auditResult == 0 {
		t.Fatalf("F20-3: the self-audit refusal was not recorded")
	}
}

// ---------------------------------------------------------------------------
// helpers shared by the routing and pricing cases
// ---------------------------------------------------------------------------

func channelBody(t *testing.T, name, upstreamKey string) map[string]any {
	t.Helper()
	return map[string]any{
		"name":             name,
		"protocol_id":      protocolID(t, "OPENAI_COMPAT"),
		"region":           "DOMESTIC",
		"base_url":         "https://upstream.example/v1",
		"upstream_key":     upstreamKey,
		"group_scope_mode": "ALL",
		"weight":           100,
		"priority":         100,
		"stream_supported": true,
		"fallback_allowed": true,
	}
}

func createChannelAPI(t *testing.T, ops int64, label string) int64 {
	t.Helper()
	name := fmt.Sprintf("t21-%s-%d", label, nextInt())
	rec := call(t, ops, http.MethodPost, "/api/admin/channels", channelBody(t, name, "sk-channel-secret-"+name))
	requireStatus(t, "create channel "+label, rec, http.StatusCreated)
	var created struct {
		Channel struct {
			ID int64 `json:"id"`
		} `json:"channel"`
	}
	decodeBody(t, rec, &created)
	if created.Channel.ID == 0 {
		t.Fatalf("channel %s has no id: %s", label, rec.Body.String())
	}
	t.Cleanup(func() { dropChannel(t, created.Channel.ID) })
	return created.Channel.ID
}

func dropChannel(t *testing.T, id int64) {
	t.Helper()
	ctx := context.Background()
	_, _ = testDB.ExecContext(ctx, `DELETE FROM t_channel_dedicated_user WHERE channel_id = ?`, id)
	_, _ = testDB.ExecContext(ctx, `DELETE FROM t_route WHERE channel_id = ?`, id)
	_, _ = testDB.ExecContext(ctx, `DELETE FROM t_channel WHERE id = ?`, id)
}

func setChannelDedicated(t *testing.T, channelID, userID int64) {
	t.Helper()
	if _, err := testDB.ExecContext(context.Background(), `
		UPDATE t_channel SET dedicated_scope_mode = 'SELECTED' WHERE id = ?`, channelID); err != nil {
		t.Fatalf("set dedicated mode: %v", err)
	}
	if _, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_channel_dedicated_user (channel_id, user_id, created_by, created_at)
		VALUES (?,?,?,UTC_TIMESTAMP(3))`, channelID, userID, userID); err != nil {
		t.Fatalf("add dedicated user: %v", err)
	}
}

func insertRoute(t *testing.T, modelID, channelID int64) {
	t.Helper()
	if _, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_route
			(model_id, channel_id, protocol_id, upstream_model_name, region, weight,
			 priority, stream_supported, max_tokens_ceiling, enabled, created_at, updated_at)
		VALUES (?,?,?,?,'DOMESTIC',100,100,1,0,1,UTC_TIMESTAMP(3),UTC_TIMESTAMP(3))`,
		modelID, channelID, protocolID(t, "OPENAI_COMPAT"), fmt.Sprintf("up-%d", modelID)); err != nil {
		t.Fatalf("insert route: %v", err)
	}
}

// insertGroup creates a private price group so the test never mutates the
// shared standard/contract rows another package may be reading.
func insertGroup(t *testing.T, label, channelScope string, channels []int64) string {
	t.Helper()
	code := fmt.Sprintf("t21%s%d", strings.ToLower(label), nextInt()%1000000)
	res, err := testDB.ExecContext(context.Background(), `
		INSERT INTO t_price_group
			(code, name, ratio_bp, model_scope_mode, channel_scope_mode, enabled,
			 created_by, created_at, updated_at)
		VALUES (?,?,10000,'ALL',?,1,0,UTC_TIMESTAMP(3),UTC_TIMESTAMP(3))`,
		code, code, channelScope)
	if err != nil {
		t.Fatalf("insert group %s: %v", label, err)
	}
	groupID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("group id: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM t_price_group_scope WHERE group_id = ?`, groupID)
		_, _ = testDB.ExecContext(context.Background(), `DELETE FROM t_price_group WHERE id = ?`, groupID)
	})
	for _, ch := range channels {
		if _, err := testDB.ExecContext(context.Background(), `
			INSERT INTO t_price_group_scope (group_id, scope_type, scope_id, created_at)
			VALUES (?, 'CHANNEL', ?, UTC_TIMESTAMP(3))`, groupID, ch); err != nil {
			t.Fatalf("insert group scope: %v", err)
		}
	}
	return code
}

func liveCatalog(t *testing.T) *gw.Catalog {
	t.Helper()
	server(t)
	store := config.NewStore()
	loader := &config.Loader{
		Redis: config.NewGoRedis(testRedis.Raw()),
		Store: store,
		Now:   clock.Real{}.Now,
	}
	if err := loader.RefreshAll(context.Background()); err != nil {
		t.Fatalf("load routing snapshot: %v", err)
	}
	return gw.NewCatalog(store)
}

func routeChannels(t *testing.T, catalog *gw.Catalog, modelID int64, groupCode string, userID int64) []int64 {
	t.Helper()
	c := &gw.Ctx{
		Model: &gw.ModelEntry{ID: modelID},
		Auth:  &gw.AuthContext{UserID: userID, KeyID: 1, GroupCode: groupCode, GroupRatioBP: 10000},
	}
	if err := gw.NewRouterStage(&gw.Deps{Catalog: catalog, Clock: redisx.NewClock(nil, nil)}).Run(context.Background(), c); err != nil {
		return nil
	}
	out := make([]int64, 0, len(c.Routes))
	for _, r := range c.Routes {
		out = append(out, r.ChannelID)
	}
	return out
}

func routeError(t *testing.T, catalog *gw.Catalog, modelID int64, groupCode string, userID int64) error {
	t.Helper()
	c := &gw.Ctx{
		Model: &gw.ModelEntry{ID: modelID},
		Auth:  &gw.AuthContext{UserID: userID, KeyID: 1, GroupCode: groupCode, GroupRatioBP: 10000},
	}
	return gw.NewRouterStage(&gw.Deps{Catalog: catalog, Clock: redisx.NewClock(nil, nil)}).Run(context.Background(), c)
}

func createPriceAPI(t *testing.T, ops, modelID, in, out, cost int64) int64 {
	t.Helper()
	rec := call(t, ops, http.MethodPost, "/api/admin/prices", map[string]any{
		"model_id":              modelID,
		"in_price_micro_per_k":  in,
		"out_price_micro_per_k": out,
		"cost_in_micro_per_k":   cost,
		"cost_out_micro_per_k":  cost,
		"effective_from":        time.Now().UTC().Format(time.RFC3339),
	})
	requireStatus(t, "create price", rec, http.StatusCreated)
	var created struct {
		Price struct {
			ID int64 `json:"id"`
		} `json:"price"`
	}
	decodeBody(t, rec, &created)
	if created.Price.ID == 0 {
		t.Fatalf("price has no id: %s", rec.Body.String())
	}
	return created.Price.ID
}

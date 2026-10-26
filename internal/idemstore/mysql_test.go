package idemstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"yunshu.dev/core/internal/platform/clock"
	"yunshu.dev/core/internal/platform/migrate"
)

// testDB is the real MySQL schema used by the durable-leg tests. It is created
// on first run and migrated with the production migration set; rows written by
// these tests are deleted by their own cleanup, other rows are left alone so an
// operator can inspect them.
var (
	testDB    *sql.DB
	testDBDSN string
)

const (
	defaultTestDSN = "root:root@tcp(127.0.0.1:13306)/yunshu_test?parseTime=true&loc=UTC&charset=utf8mb4&multiStatements=true"

	idemTestScope  = "idem"
	idemTestAPIKey = int64(7)
	idemTestUser   = int64(70)
)

var safeDBName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// setupMySQL mirrors the ledger / config fixtures: create the schema when it is
// missing, drop a half-created one (tables but no schema_migrations), then
// apply every migration. 0019 is skipped because its local seed rows are
// irrelevant here.
func setupMySQL() error {
	dsn := os.Getenv("TEST_DB_DSN")
	if dsn == "" {
		dsn = defaultTestDSN
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return fmt.Errorf("parse TEST_DB_DSN: %w", err)
	}
	if cfg.DBName == "" {
		cfg.DBName = "yunshu_test"
	}
	if !safeDBName.MatchString(cfg.DBName) {
		return fmt.Errorf("unsafe database name %q", cfg.DBName)
	}
	cfg.ParseTime = true
	if cfg.Loc == nil {
		cfg.Loc = time.UTC
	}
	cfg.MultiStatements = true

	dbName := cfg.DBName
	adminCfg := *cfg
	adminCfg.DBName = ""
	admin, err := sql.Open("mysql", adminCfg.FormatDSN())
	if err != nil {
		return fmt.Errorf("open admin: %w", err)
	}
	defer admin.Close()

	ctx, cancel := context.WithTimeout(context.TODO(), 2*time.Minute)
	defer cancel()

	createSQL := fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_as_cs",
		dbName,
	)
	if _, err := admin.ExecContext(ctx, createSQL); err != nil {
		return fmt.Errorf("create schema %s: %w", dbName, err)
	}

	var tables, bookkeeping int
	if err := admin.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = ?`, dbName,
	).Scan(&tables); err != nil {
		return fmt.Errorf("count tables: %w", err)
	}
	if err := admin.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM information_schema.tables
		  WHERE table_schema = ? AND table_name = 'schema_migrations'`, dbName,
	).Scan(&bookkeeping); err != nil {
		return fmt.Errorf("count bookkeeping: %w", err)
	}
	if tables > 0 && bookkeeping == 0 {
		if _, err := admin.ExecContext(ctx, fmt.Sprintf("DROP DATABASE `%s`", dbName)); err != nil {
			return fmt.Errorf("drop incomplete schema: %w", err)
		}
		if _, err := admin.ExecContext(ctx, createSQL); err != nil {
			return fmt.Errorf("recreate schema: %w", err)
		}
	}

	testDBDSN = cfg.FormatDSN()
	testDB, err = sql.Open("mysql", testDBDSN)
	if err != nil {
		return fmt.Errorf("open test schema: %w", err)
	}
	testDB.SetMaxOpenConns(20)
	testDB.SetMaxIdleConns(10)
	testDB.SetConnMaxLifetime(5 * time.Minute)

	if err := testDB.PingContext(ctx); err != nil {
		return fmt.Errorf("ping test schema: %w", err)
	}

	migrations, err := migrate.LoadEmbedded()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	runner := migrate.NewRunner(testDB, migrations, clock.Real{}.Now)
	runner.Skip("0019", "idempotency tests write their own rows; local seed rows are irrelevant")
	if _, err := runner.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

func newMySQLStore(t *testing.T) (*Store, *MySQL) {
	t.Helper()
	flushRedis(t)
	db := NewMySQL(testDB)
	return New(newClient(t), nil, db, DefaultOptions(), nil), db
}

func uniqueIdemKey(t *testing.T) string {
	t.Helper()
	key := fmt.Sprintf("test-%s-%d", strings.ToLower(t.Name()), time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := testDB.ExecContext(context.TODO(),
			`DELETE FROM t_idempotency_record WHERE scope = ? AND api_key_id = ? AND idem_key = ?`,
			idemTestScope, idemTestAPIKey, key,
		); err != nil {
			t.Logf("cleanup idempotency row %s: %v", key, err)
		}
	})
	return key
}

func mysqlInput(key, requestID, bodyHash, fence string) BeginInput {
	return BeginInput{
		Scope: idemTestScope, APIKeyID: idemTestAPIKey, UserID: idemTestUser,
		IdemKey: key, BodyHash: bodyHash, RequestID: requestID, FenceToken: fence,
	}
}

func readOwner(t *testing.T, key string) (fence, requestID, state string) {
	t.Helper()
	if err := testDB.QueryRowContext(context.TODO(),
		`SELECT fence_token, request_id, state FROM t_idempotency_record
		  WHERE scope = ? AND api_key_id = ? AND idem_key = ?`,
		idemTestScope, idemTestAPIKey, key,
	).Scan(&fence, &requestID, &state); err != nil {
		t.Fatalf("read owner row: %v", err)
	}
	return fence, requestID, state
}

// ① 两个并发请求争同一个键：唯一键只放行一个插入，另一个读到 IN_FLIGHT 后 WAIT。
func TestMySQLConcurrentBeginGrantsOneLease(t *testing.T) {
	store, _ := newMySQLStore(t)
	key := uniqueIdemKey(t)
	ctx := t.Context()

	type outcome struct {
		action Action
		record *Record
		err    error
	}
	start := make(chan struct{})
	out := make(chan outcome, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			result, err := store.Begin(ctx, mysqlInput(
				key,
				fmt.Sprintf("req-race-%d", i),
				"hash-race",
				fmt.Sprintf("fence-race-%d", i),
			))
			out <- outcome{action: result.Action, record: result.Record, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(out)

	counts := map[Action]int{}
	var winner *Record
	for result := range out {
		if result.err != nil {
			t.Fatalf("concurrent Begin: %v", result.err)
		}
		counts[result.action]++
		if result.action == ActionNew {
			winner = result.record
		}
	}
	if counts[ActionNew] != 1 || counts[ActionWait] != 1 {
		t.Fatalf("actions = %v，期望恰好 1 个 NEW + 1 个 WAIT", counts)
	}
	if winner == nil {
		t.Fatal("NEW 没有返回记录")
	}

	fence, requestID, state := readOwner(t, key)
	if fence != winner.FenceToken || requestID != winner.RequestID || state != string(StateInFlight) {
		t.Fatalf("库中持有者 = (%q, %q, %q)，期望 (%q, %q, %q)",
			fence, requestID, state, winner.FenceToken, winner.RequestID, StateInFlight)
	}
	t.Logf("并发: NEW=1 WAIT=1，唯一持有者 fence=%s request_id=%s state=%s", fence, requestID, state)
}

// ② 租约过期后新请求可抢占，fence 轮换；旧持有者写入被 ErrFenceInvalid 拒绝。
func TestMySQLLeaseExpiryTakeoverRotatesFence(t *testing.T) {
	store, _ := newMySQLStore(t)
	key := uniqueIdemKey(t)
	ctx := t.Context()

	first, err := store.Begin(ctx, mysqlInput(key, "req-old", "hash-lease", "fence-old"))
	if err != nil || first.Action != ActionNew {
		t.Fatalf("first begin = %+v err=%v", first, err)
	}

	// 模拟持有者死亡：把租约改为已过期，而不是真的等 15 秒。
	if _, err := testDB.ExecContext(ctx,
		`UPDATE t_idempotency_record SET lease_expires_at = NOW(3) - INTERVAL 1 SECOND
		  WHERE scope = ? AND api_key_id = ? AND idem_key = ?`,
		idemTestScope, idemTestAPIKey, key,
	); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	flushRedis(t) // 丢掉 K6/K7，强制走 DB 判定

	second, err := store.Begin(ctx, mysqlInput(key, "req-new", "hash-lease", "fence-new"))
	if err != nil {
		t.Fatalf("takeover begin: %v", err)
	}
	if second.Action != ActionStaleLock {
		t.Fatalf("takeover action = %v，期望 STALE_LOCK", second.Action)
	}
	if second.Record == nil || second.Record.FenceToken != "fence-new" || second.Record.RequestID != "req-new" {
		t.Fatalf("takeover record = %+v，期望新 fence/request", second.Record)
	}

	fence, requestID, state := readOwner(t, key)
	if fence != "fence-new" || requestID != "req-new" || fence == "fence-old" {
		t.Fatalf("库中持有者 = (%q, %q, %q)，fence 必须轮换", fence, requestID, state)
	}

	if err := store.Complete(ctx, *first.Record, StateDone, []byte("late")); !errors.Is(err, ErrFenceInvalid) {
		t.Fatalf("旧持有者 Complete = %v，期望 ErrFenceInvalid", err)
	}
	if err := store.Complete(ctx, *second.Record, StateDone, []byte("new")); err != nil {
		t.Fatalf("新持有者 Complete: %v", err)
	}
	t.Logf("抢占: fence-old → fence-new，旧持有者写入被拒；新持有者完成 DONE")
}

// ②-b Expand 兼容：0020 之前写入的 IN_FLIGHT 行（fence=”, lease=NULL）可被抢占，
// 不会因为空 fence 永远卡在 WAIT。
func TestMySQLTakeoverHealsLegacyEmptyFenceRow(t *testing.T) {
	store, _ := newMySQLStore(t)
	key := uniqueIdemKey(t)
	ctx := t.Context()

	if _, err := testDB.ExecContext(ctx,
		`INSERT INTO t_idempotency_record
		   (scope, idem_key, user_id, api_key_id, request_id, state, body_hash, created_at, expires_at)
		 VALUES (?, ?, ?, ?, 'req-legacy', 'IN_FLIGHT', 'hash-legacy',
		         NOW(3) - INTERVAL 1 MINUTE, NOW(3) + INTERVAL 1 HOUR)`,
		idemTestScope, key, idemTestUser, idemTestAPIKey,
	); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	result, err := store.Begin(ctx, mysqlInput(key, "req-heal", "hash-legacy", "fence-heal"))
	if err != nil {
		t.Fatalf("heal begin: %v", err)
	}
	if result.Action != ActionStaleLock {
		t.Fatalf("action = %v，期望 STALE_LOCK（存量空 fence 行可抢占）", result.Action)
	}
	fence, requestID, state := readOwner(t, key)
	if fence != "fence-heal" || requestID != "req-heal" || state != string(StateInFlight) {
		t.Fatalf("库中持有者 = (%q, %q, %q)", fence, requestID, state)
	}
	t.Logf("Expand: 空 fence 存量行被 fence-heal 接管")
}

// ③ 同键不同 body_hash：返回 KEY_REUSED（400），绝不返回旧响应。
func TestMySQLKeyReusedForDifferentBody(t *testing.T) {
	store, db := newMySQLStore(t)
	key := uniqueIdemKey(t)
	ctx := t.Context()

	first, err := store.Begin(ctx, mysqlInput(key, "req-first", "hash-a", "fence-first"))
	if err != nil || first.Action != ActionNew {
		t.Fatalf("first begin = %+v err=%v", first, err)
	}
	if err := store.Complete(ctx, *first.Record, StateDone, []byte(`{"ok":true}`)); err != nil {
		t.Fatalf("complete: %v", err)
	}

	reused, err := store.Begin(ctx, mysqlInput(key, "req-second", "hash-b", "fence-second"))
	if err != nil {
		t.Fatalf("reused begin: %v", err)
	}
	if reused.Action != ActionKeyReused {
		t.Fatalf("action = %v，期望 KEY_REUSED", reused.Action)
	}
	decision := DecideReplay(reused, false)
	if decision.Status != 400 || decision.Code != "idempotency_key_reused" || len(decision.Body) != 0 {
		t.Fatalf("reused decision = %+v，期望 400 idempotency_key_reused 且无响应体", decision)
	}

	stored, err := db.Get(ctx, idemTestScope, idemTestAPIKey, key)
	if err != nil {
		t.Fatalf("get stored row: %v", err)
	}
	if string(stored.ResponseBody) != `{"ok":true}` || stored.RequestID != "req-first" {
		t.Fatalf("存量响应被覆盖: body=%q request_id=%q", stored.ResponseBody, stored.RequestID)
	}
	t.Logf("复用: 400 idempotency_key_reused，存量响应仍属 req-first")
}

// ④ 完成后 Get 取回响应体与 request_id；重放返回同一响应。
func TestMySQLCompleteThenLookupReturnsResponse(t *testing.T) {
	store, db := newMySQLStore(t)
	key := uniqueIdemKey(t)
	ctx := t.Context()
	body := []byte(`{"choices":[{"text":"hi"}]}`)

	first, err := store.Begin(ctx, mysqlInput(key, "req-lookup", "hash-lookup", "fence-lookup"))
	if err != nil || first.Action != ActionNew {
		t.Fatalf("first begin = %+v err=%v", first, err)
	}
	if err := store.Complete(ctx, *first.Record, StateDone, body); err != nil {
		t.Fatalf("complete: %v", err)
	}

	stored, err := db.Get(ctx, idemTestScope, idemTestAPIKey, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stored.State != StateDone || stored.RequestID != "req-lookup" || string(stored.ResponseBody) != string(body) {
		t.Fatalf("get = state=%s request_id=%s body=%q", stored.State, stored.RequestID, stored.ResponseBody)
	}
	if !stored.LeaseExpiresAt.IsZero() {
		t.Fatalf("完成后租约未清空: %v", stored.LeaseExpiresAt)
	}

	replay, err := store.Begin(ctx, mysqlInput(key, "req-replay", "hash-lookup", "fence-replay"))
	if err != nil || replay.Action != ActionReplay {
		t.Fatalf("replay begin = %+v err=%v", replay, err)
	}
	decision := DecideReplay(replay, false)
	if decision.Status != 200 || string(decision.Body) != string(body) {
		t.Fatalf("replay decision = %+v", decision)
	}
	t.Logf("查询: state=DONE request_id=req-lookup body=%s，重放 200", body)
}

// ⑤ DB 写失败必须让 Begin 失败，不得降级为放行。
func TestMySQLBeginFailsWhenDBWriteFails(t *testing.T) {
	flushRedis(t)
	closed, err := sql.Open("mysql", testDBDSN)
	if err != nil {
		t.Fatalf("open closed pool: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("close pool: %v", err)
	}

	store := New(newClient(t), nil, NewMySQL(closed), DefaultOptions(), nil)
	key := uniqueIdemKey(t)
	result, err := store.Begin(t.Context(), mysqlInput(key, "req-fail", "hash-fail", "fence-fail"))
	if err == nil {
		t.Fatalf("DB 写失败时 Begin 必须返回错误，实际 action=%v", result.Action)
	}
	if result.Action != 0 || result.Record != nil {
		t.Fatalf("失败结果不应携带动作: %+v", result)
	}
	t.Logf("DB 写失败: Begin 直接报错（未降级）: %v", err)
}

// 续约把 DB 租约推到 now+LeaseTTL；换了 fence 的请求续约被拒。
func TestMySQLRenewExtendsLeaseAndRejectsStaleFence(t *testing.T) {
	store, db := newMySQLStore(t)
	key := uniqueIdemKey(t)
	ctx := t.Context()

	first, err := store.Begin(ctx, mysqlInput(key, "req-renew", "hash-renew", "fence-renew"))
	if err != nil || first.Action != ActionNew {
		t.Fatalf("first begin = %+v err=%v", first, err)
	}
	if _, err := testDB.ExecContext(ctx,
		`UPDATE t_idempotency_record SET lease_expires_at = NOW(3) + INTERVAL 1 SECOND
		  WHERE scope = ? AND api_key_id = ? AND idem_key = ?`,
		idemTestScope, idemTestAPIKey, key,
	); err != nil {
		t.Fatalf("shorten lease: %v", err)
	}

	if err := store.Renew(ctx, *first.Record); err != nil {
		t.Fatalf("renew: %v", err)
	}
	renewed, err := db.Get(ctx, idemTestScope, idemTestAPIKey, key)
	if err != nil {
		t.Fatalf("get after renew: %v", err)
	}
	if remaining := time.Until(renewed.LeaseExpiresAt); remaining < 10*time.Second {
		t.Fatalf("续约后剩余租约 = %v，期望约 15s", remaining)
	}

	stale := *first.Record
	stale.FenceToken = "fence-other"
	if err := store.Renew(ctx, stale); !errors.Is(err, ErrFenceInvalid) {
		t.Fatalf("换 fence 续约 = %v，期望 ErrFenceInvalid", err)
	}
	t.Logf("续约: 剩余 %v；旧 fence 被拒", time.Until(renewed.LeaseExpiresAt).Round(time.Second))
}

// fence 必须是 char(36) 能容纳的 UUIDv7 文本，且每次生成都不同。
func TestGenerateFenceIsUUIDv7Shaped(t *testing.T) {
	first := generateFence()
	second := generateFence()
	if len(first) != fenceTokenWidth {
		t.Fatalf("fence 长度 = %d，期望 %d（char(36)）", len(first), fenceTokenWidth)
	}
	parts := strings.Split(first, "-")
	if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 ||
		len(parts[3]) != 4 || len(parts[4]) != 12 {
		t.Fatalf("fence %q 不是 UUIDv7 分段形态", first)
	}
	if first[14] != '7' {
		t.Fatalf("fence %q 的版本位不是 7", first)
	}
	if first == second {
		t.Fatalf("两次生成的 fence 相同: %s", first)
	}
}

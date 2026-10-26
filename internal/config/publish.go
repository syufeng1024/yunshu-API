package config

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/platform/clock"
)

// publishLockName serialises publishers across control-plane replicas. A
// version is reserved and the five snapshots are written while this MySQL
// advisory lock is held.
const publishLockName = "yunshu:config_publish"

// ErrPublishBusy means another publisher held the advisory lock past the
// timeout. The caller may retry.
var ErrPublishBusy = errors.New("config: another publish is in progress")

// Change is one key update submitted to Publish.
type Change struct {
	Key Key
	Raw jsonx.RawMessage
}

// PublishResult describes a committed publish. It is returned even when the
// final Pub/Sub notification fails, because the database and snapshots are
// already updated and the 30s poll will converge.
type PublishResult struct {
	Version       int64
	PublishID     string
	Namespaces    []Namespace
	ChangedKeys   []Key
	SnapshotBytes map[Namespace]int
}

// Publisher writes the authoritative rows, the publish record, the snapshots
// and the version in the order required by T05 hard constraint 7:
//
//	t_config_item -> t_config_publish -> SET cfg:snap:* -> INCR cfg:ver -> PUBLISH
//
// The reverse order would let a node read "new version + old snapshot".
type Publisher struct {
	DB          *sql.DB
	Redis       Redis
	Now         func() time.Time
	LockTimeout time.Duration
}

func (p *Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return clock.Real{}.Now()
}

// Publish applies key changes for the namespaces they belong to.
func (p *Publisher) Publish(ctx context.Context, changes []Change, operatorID uint64) (*PublishResult, error) {
	if p.DB == nil || p.Redis == nil {
		return nil, fmt.Errorf("config: publisher requires database and redis")
	}
	values, namespaces, err := validatePublishChanges(changes)
	if err != nil {
		return nil, err
	}

	conn, release, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	rows, err := loadItemRows(ctx, conn)
	if err != nil {
		return nil, err
	}
	next, err := p.nextVersion(ctx, conn)
	if err != nil {
		return nil, err
	}

	_, payloads, err := buildKeySnapshots(rows, values, next, p.now())
	if err != nil {
		return nil, err
	}

	changedKeys := sortedKeys(values)
	publishID, err := newPublishID(p.now())
	if err != nil {
		return nil, fmt.Errorf("config: generate publish id: %w", err)
	}

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("config: begin publish transaction: %w", err)
	}
	for _, key := range changedKeys {
		if err := upsertItem(ctx, tx, key, values[key].Raw(), next, operatorID); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	if err := insertPublish(ctx, tx, publishID, next, scopeFor(namespaces), changedKeys, operatorID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("config: commit publish transaction: %w", err)
	}

	result := &PublishResult{
		Version:       next,
		PublishID:     publishID,
		Namespaces:    namespaces,
		ChangedKeys:   changedKeys,
		SnapshotBytes: payloadSizes(payloads),
	}
	if err := p.writeSnapshotsAndVersion(ctx, payloads, next); err != nil {
		return result, err
	}
	if err := p.notify(ctx, publishID, next, namespaces); err != nil {
		return result, fmt.Errorf("config: version %d committed but pub/sub notify failed: %w", next, err)
	}
	return result, nil
}

// PublishRaw publishes a snapshot assembled outside t_config_item. It exists
// for keyless namespaces (ROUTING): the route/price loader owns that payload.
// The same size limit, version order and publish record apply.
func (p *Publisher) PublishRaw(ctx context.Context, ns Namespace, payload []byte, changedKeys []Key, operatorID uint64) (*PublishResult, error) {
	if p.DB == nil || p.Redis == nil {
		return nil, fmt.Errorf("config: publisher requires database and redis")
	}
	if namespaceIndex(ns) < 0 {
		return nil, fmt.Errorf("config: %q is not a snapshot namespace", ns)
	}
	if len(KeysInNamespace(ns)) != 0 {
		return nil, fmt.Errorf("config: %s is key-driven; use Publish", ns)
	}
	// Reject oversize input before touching either store (acceptance A8).
	if err := checkSnapshotSize(ns, payload); err != nil {
		return nil, err
	}
	var wire wireSnapshot
	if err := jsonx.Unmarshal(payload, &wire); err != nil {
		return nil, fmt.Errorf("config: decode %s raw snapshot: %w", ns, err)
	}
	if wire.Namespace != "" && wire.Namespace != string(ns) {
		return nil, fmt.Errorf("config: %s raw snapshot declares namespace %q", ns, wire.Namespace)
	}

	conn, release, err := p.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	rows, err := loadItemRows(ctx, conn)
	if err != nil {
		return nil, err
	}
	next, err := p.nextVersion(ctx, conn)
	if err != nil {
		return nil, err
	}

	_, payloads, err := buildKeySnapshots(rows, nil, next, p.now())
	if err != nil {
		return nil, err
	}
	wire.Namespace = string(ns)
	wire.Version = next
	encoded, err := jsonx.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("config: encode %s raw snapshot: %w", ns, err)
	}
	if err := checkSnapshotSize(ns, encoded); err != nil {
		return nil, err
	}
	if _, err := decodeSnapshot(ns, encoded, next, p.now()); err != nil {
		return nil, fmt.Errorf("config: validate %s raw snapshot: %w", ns, err)
	}
	payloads[ns] = encoded

	keys := sortedKeySlice(changedKeys)
	publishID, err := newPublishID(p.now())
	if err != nil {
		return nil, fmt.Errorf("config: generate publish id: %w", err)
	}
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("config: begin publish transaction: %w", err)
	}
	if err := insertPublish(ctx, tx, publishID, next, string(ns), keys, operatorID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("config: commit publish transaction: %w", err)
	}

	result := &PublishResult{
		Version:       next,
		PublishID:     publishID,
		Namespaces:    []Namespace{ns},
		ChangedKeys:   keys,
		SnapshotBytes: payloadSizes(payloads),
	}
	if err := p.writeSnapshotsAndVersion(ctx, payloads, next); err != nil {
		return result, err
	}
	if err := p.notify(ctx, publishID, next, []Namespace{ns}); err != nil {
		return result, fmt.Errorf("config: version %d committed but pub/sub notify failed: %w", next, err)
	}
	return result, nil
}

// VersionRaceError means cfg:ver moved between reservation and INCR despite
// the advisory lock (for example, an operator ran INCR manually).
type VersionRaceError struct {
	Expected int64
	Got      int64
}

func (e *VersionRaceError) Error() string {
	return fmt.Sprintf("config: cfg:ver moved during publish: expected %d, got %d", e.Expected, e.Got)
}

func (p *Publisher) acquire(ctx context.Context) (*sql.Conn, func(), error) {
	conn, err := p.DB.Conn(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("config: acquire publish connection: %w", err)
	}
	timeout := int(p.LockTimeout / time.Second)
	if timeout < 1 {
		timeout = 5
	}
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", publishLockName, timeout).Scan(&got); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("config: acquire publish lock: %w", err)
	}
	if !got.Valid || got.Int64 != 1 {
		_ = conn.Close()
		return nil, nil, ErrPublishBusy
	}
	release := func() {
		relCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		var released sql.NullInt64
		_ = conn.QueryRowContext(relCtx, "SELECT RELEASE_LOCK(?)", publishLockName).Scan(&released)
		_ = conn.Close()
	}
	return conn, release, nil
}

// nextVersion reserves the next global version. Seed rows carry version 1
// while cfg:ver may not exist yet; the baseline is repaired before the
// snapshots are written so the first publish is always newer than the rows a
// node may have loaded from the database.
func (p *Publisher) nextVersion(ctx context.Context, conn *sql.Conn) (int64, error) {
	redisVer, exists, err := (&Loader{Redis: p.Redis}).readVersion(ctx)
	if err != nil {
		return 0, err
	}
	if !exists {
		redisVer = 0
	}
	dbVer, err := readDBVersion(ctx, conn)
	if err != nil {
		return 0, err
	}
	base := redisVer
	if dbVer > base {
		base = dbVer
	}
	if redisVer < base {
		if err := p.Redis.Set(ctx, RedisVersionKey, strconv.FormatInt(base, 10), 0); err != nil {
			return 0, fmt.Errorf("config: repair cfg:ver baseline: %w", err)
		}
	}
	return base + 1, nil
}

func (p *Publisher) writeSnapshotsAndVersion(ctx context.Context, payloads map[Namespace][]byte, version int64) error {
	for _, ns := range SnapshotNamespaces {
		payload, ok := payloads[ns]
		if !ok {
			return fmt.Errorf("config: missing encoded snapshot for %s", ns)
		}
		if err := p.Redis.Set(ctx, SnapshotRedisKey(ns), string(payload), SnapshotTTL); err != nil {
			return fmt.Errorf("config: version %d committed but snapshot %s write failed: %w", version, ns, err)
		}
	}
	got, err := p.Redis.Incr(ctx, RedisVersionKey)
	if err != nil {
		return fmt.Errorf("config: snapshots written for version %d but INCR cfg:ver failed: %w", version, err)
	}
	if got != version {
		return &VersionRaceError{Expected: version, Got: got}
	}
	return nil
}

type invalidateMessage struct {
	PublishID  string   `json:"publish_id"`
	Version    int64    `json:"version"`
	Namespaces []string `json:"namespaces"`
}

func (p *Publisher) notify(ctx context.Context, publishID string, version int64, namespaces []Namespace) error {
	names := make([]string, 0, len(namespaces))
	for _, ns := range namespaces {
		names = append(names, string(ns))
	}
	sort.Strings(names)
	payload, err := jsonx.Marshal(invalidateMessage{
		PublishID:  publishID,
		Version:    version,
		Namespaces: names,
	})
	if err != nil {
		return err
	}
	return p.Redis.Publish(ctx, RedisInvalidateChannel, string(payload))
}

// validatePublishChanges validates the ops publish path. Unlike the runtime
// path it accepts every assigned key, but it still refuses an unregistered
// strategy so a release cannot brick the startup gate.
func validatePublishChanges(changes []Change) (map[Key]Value, []Namespace, error) {
	if len(changes) == 0 {
		return nil, nil, fmt.Errorf("config: publish requires at least one change")
	}
	values := make(map[Key]Value, len(changes))
	namespaceSet := make(map[Namespace]struct{})
	for _, change := range changes {
		m, ok := metaByKey[change.Key]
		if !ok {
			return nil, nil, invalid(change.Key, "unknown config key")
		}
		if m.Namespace == NsUnassigned {
			return nil, nil, invalid(change.Key, "key is not assigned to any namespace")
		}
		if _, dup := values[change.Key]; dup {
			return nil, nil, invalid(change.Key, "duplicate change for key")
		}
		value, err := Parse(change.Key, change.Raw)
		if err != nil {
			return nil, nil, err
		}
		if m.Strategy != "" {
			name, _ := value.String()
			if _, ok := Resolve(m.Strategy, name); !ok {
				return nil, nil, invalid(change.Key,
					"strategy %q is not registered for %s; available: %s",
					name, m.Strategy, strings.Join(RegisteredNames(m.Strategy), ", "))
			}
		}
		values[change.Key] = value
		namespaceSet[m.Namespace] = struct{}{}
	}
	namespaces := make([]Namespace, 0, len(namespaceSet))
	for ns := range namespaceSet {
		namespaces = append(namespaces, ns)
	}
	sort.Slice(namespaces, func(i, j int) bool { return namespaces[i] < namespaces[j] })
	return values, namespaces, nil
}

func buildKeySnapshots(rows []itemRow, changes map[Key]Value, version int64, now time.Time) (map[Namespace]*Snapshot, map[Namespace][]byte, error) {
	snaps := make(map[Namespace]*Snapshot, len(SnapshotNamespaces))
	for _, ns := range SnapshotNamespaces {
		snaps[ns] = newSnapshot(ns, version, now)
	}
	for _, row := range rows {
		snap := snaps[row.Namespace]
		if snap == nil || row.Namespace == NsUnassigned {
			continue
		}
		applyRow(snap, row)
	}
	for key, value := range changes {
		snaps[metaByKey[key].Namespace].values[key] = value
	}

	payloads := make(map[Namespace][]byte, len(SnapshotNamespaces))
	for _, ns := range SnapshotNamespaces {
		payload, err := encodeSnapshot(snaps[ns])
		if err != nil {
			return nil, nil, fmt.Errorf("config: encode %s snapshot: %w", ns, err)
		}
		if err := checkSnapshotSize(ns, payload); err != nil {
			return nil, nil, err
		}
		payloads[ns] = payload
	}
	return snaps, payloads, nil
}

type queryExecutor interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type itemRow struct {
	Namespace Namespace
	Key       Key
	Raw       jsonx.RawMessage
	Version   int64
}

func loadItemRows(ctx context.Context, q queryExecutor) ([]itemRow, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT cfg_namespace, cfg_key, cfg_value, version
		  FROM t_config_item
		 WHERE scope = 'GLOBAL' AND scope_id = 0 AND enabled = 1
		 ORDER BY cfg_key`)
	if err != nil {
		return nil, fmt.Errorf("config: load config items: %w", err)
	}
	defer rows.Close()

	var out []itemRow
	for rows.Next() {
		var ns, key, value string
		var version int64
		if err := rows.Scan(&ns, &key, &value, &version); err != nil {
			return nil, fmt.Errorf("config: scan config item: %w", err)
		}
		out = append(out, itemRow{
			Namespace: Namespace(ns),
			Key:       Key(key),
			Raw:       jsonx.RawMessage(value),
			Version:   version,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("config: iterate config items: %w", err)
	}
	return out, nil
}

func readDBVersion(ctx context.Context, q queryExecutor) (int64, error) {
	var itemVersion, publishVersion int64
	if err := q.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM t_config_item`).Scan(&itemVersion); err != nil {
		return 0, fmt.Errorf("config: read max config item version: %w", err)
	}
	if err := q.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM t_config_publish`).Scan(&publishVersion); err != nil {
		return 0, fmt.Errorf("config: read max publish version: %w", err)
	}
	if publishVersion > itemVersion {
		return publishVersion, nil
	}
	return itemVersion, nil
}

func upsertItem(ctx context.Context, tx *sql.Tx, key Key, raw jsonx.RawMessage, version int64, operatorID uint64) error {
	var operator any
	if operatorID != 0 {
		operator = operatorID
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO t_config_item
			(cfg_namespace, cfg_key, scope, scope_id, cfg_value, version, enabled, updated_by, created_at, updated_at)
		VALUES (?, ?, 'GLOBAL', 0, ?, ?, 1, ?, NOW(3), NOW(3))
		ON DUPLICATE KEY UPDATE
			cfg_value  = VALUES(cfg_value),
			version    = VALUES(version),
			enabled    = 1,
			updated_by = VALUES(updated_by),
			updated_at = VALUES(updated_at)`,
		string(metaByKey[key].Namespace), string(key), string(raw), version, operator)
	if err != nil {
		return fmt.Errorf("config: upsert %s: %w", key, err)
	}
	return nil
}

func insertPublish(ctx context.Context, tx *sql.Tx, publishID string, version int64, scope string, changedKeys []Key, operatorID uint64) error {
	keys := make([]string, 0, len(changedKeys))
	for _, key := range changedKeys {
		keys = append(keys, string(key))
	}
	changed, err := jsonx.Marshal(keys)
	if err != nil {
		return fmt.Errorf("config: encode changed_keys: %w", err)
	}
	var operator any
	if operatorID != 0 {
		operator = operatorID
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO t_config_publish
			(publish_id, version, scope, changed_keys, operator_id, created_at)
		VALUES (?, ?, ?, ?, ?, NOW(3))`,
		publishID, version, scope, string(changed), operator)
	if err != nil {
		return fmt.Errorf("config: insert publish record: %w", err)
	}
	return nil
}

func scopeFor(namespaces []Namespace) string {
	if len(namespaces) == 1 {
		return string(namespaces[0])
	}
	return "ALL"
}

func sortedKeys(values map[Key]Value) []Key {
	out := make([]Key, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortedKeySlice(keys []Key) []Key {
	out := make([]Key, len(keys))
	copy(out, keys)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func payloadSizes(payloads map[Namespace][]byte) map[Namespace]int {
	out := make(map[Namespace]int, len(payloads))
	for ns, payload := range payloads {
		out[ns] = len(payload)
	}
	return out
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newPublishID returns a 26-character ULID-shaped identifier: 10 characters
// of millisecond timestamp followed by 16 characters of randomness. The
// column is char(26); no external ULID dependency is needed for this one use.
func newPublishID(now time.Time) (string, error) {
	var random [10]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	ms := uint64(now.UnixMilli()) & ((1 << 48) - 1)
	var out [26]byte
	v := ms << 2
	for i := 9; i >= 0; i-- {
		out[i] = crockford[v&31]
		v >>= 5
	}

	var acc uint64
	bits := 0
	pos := 10
	for _, b := range random {
		acc = (acc << 8) | uint64(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[pos] = crockford[(acc>>uint(bits))&31]
			pos++
		}
	}
	if bits > 0 {
		out[pos] = crockford[(acc<<uint(5-bits))&31]
		pos++
	}
	if pos != len(out) {
		return "", fmt.Errorf("config: publish id encoded %d characters, expected 26", pos)
	}
	return string(out[:]), nil
}

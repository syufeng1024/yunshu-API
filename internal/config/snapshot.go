package config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"yunshu.dev/core/internal/jsonx"
	"yunshu.dev/core/internal/platform/clock"
)

// MaxSnapshotBytes is the per-namespace upper bound (TDS U-12). A snapshot
// above it is rejected, never truncated.
const MaxSnapshotBytes = 256 * 1024

// Logger is the minimal warning sink used by the loader and watcher.
type Logger interface {
	Warn(ctx context.Context, msg string)
}

type nopLogger struct{}

func (nopLogger) Warn(context.Context, string) {}

// Snapshot is one immutable namespace snapshot. All fields are unexported and
// there is no mutator: updates replace the pointer held by Store (T05 hard
// constraint 9).
type Snapshot struct {
	namespace Namespace
	version   int64
	values    map[Key]Value
	// extra holds opaque members for keyless namespaces (ROUTING is assembled
	// by the route/price loader, not from t_config_item). Key-driven
	// namespaces never use it.
	extra     map[string]jsonx.RawMessage
	fallbacks []string
	loadedAt  time.Time
}

// Namespace returns the partition this snapshot belongs to.
func (s *Snapshot) Namespace() Namespace { return s.namespace }

// Version returns the global configuration version carried by the snapshot.
func (s *Snapshot) Version() int64 { return s.version }

// LoadedAt returns when the snapshot was accepted.
func (s *Snapshot) LoadedAt() time.Time { return s.loadedAt }

// Fallbacks lists fields that failed validation or strategy resolution and
// therefore kept their built-in default. The watcher logs these as WARN.
func (s *Snapshot) Fallbacks() []string {
	out := make([]string, len(s.fallbacks))
	copy(out, s.fallbacks)
	return out
}

// Value returns one decoded value.
func (s *Snapshot) Value(key Key) (Value, bool) {
	v, ok := s.values[key]
	return v, ok
}

// Has reports whether the snapshot contains the key.
func (s *Snapshot) Has(key Key) bool {
	_, ok := s.values[key]
	return ok
}

// Int returns an integer value.
func (s *Snapshot) Int(key Key) (int64, bool) {
	v, ok := s.values[key]
	if !ok {
		return 0, false
	}
	return v.Int()
}

// Bool returns a boolean value.
func (s *Snapshot) Bool(key Key) (bool, bool) {
	v, ok := s.values[key]
	if !ok {
		return false, false
	}
	return v.Bool()
}

// String returns an enum or string value.
func (s *Snapshot) String(key Key) (string, bool) {
	v, ok := s.values[key]
	if !ok {
		return "", false
	}
	return v.String()
}

// Decimal returns a fixed-point decimal value.
func (s *Snapshot) Decimal(key Key) (Decimal, bool) {
	v, ok := s.values[key]
	if !ok {
		return 0, false
	}
	return v.Decimal()
}

// Raw returns the canonical JSON literal for key.
func (s *Snapshot) Raw(key Key) (jsonx.RawMessage, bool) {
	v, ok := s.values[key]
	if !ok {
		return nil, false
	}
	return v.Raw(), true
}

// Extra returns one opaque member of a keyless namespace snapshot.
func (s *Snapshot) Extra(name string) (jsonx.RawMessage, bool) {
	v, ok := s.extra[name]
	return v, ok
}

// ExtraNames returns the opaque member names in sorted order.
func (s *Snapshot) ExtraNames() []string {
	out := make([]string, 0, len(s.extra))
	for name := range s.extra {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// newSnapshot builds a complete snapshot from the metadata defaults for ns.
func newSnapshot(ns Namespace, version int64, now time.Time) *Snapshot {
	s := &Snapshot{
		namespace: ns,
		version:   version,
		values:    make(map[Key]Value, 8),
		extra:     make(map[string]jsonx.RawMessage),
		loadedAt:  now,
	}
	if ns == NsUnassigned {
		// Defensive: the unassigned bucket must never acquire a snapshot.
		return s
	}
	for _, key := range KeysInNamespace(ns) {
		m := metaByKey[key]
		v, err := Parse(key, jsonx.RawMessage(m.Default))
		if err != nil {
			// init() already validated every default; reaching this is a
			// programming error, not a data error.
			panic("config: invalid built-in default for " + string(key) + ": " + err.Error())
		}
		s.values[key] = v
	}
	return s
}

func (s *Snapshot) fallback(key Key, reason string) {
	s.fallbacks = append(s.fallbacks, string(key)+": "+reason)
}

// Store holds one atomic pointer per namespace. Reads are lock-free.
type Store struct {
	snaps [len(SnapshotNamespaces)]atomic.Pointer[Snapshot]
}

// NewStore returns a store populated with built-in defaults at version 0. It
// is the bootstrap path when neither a local database read nor a Redis
// snapshot is available (TDS §3.8 bootstrapPlan analogue for config).
func NewStore() *Store {
	store := &Store{}
	for i, ns := range SnapshotNamespaces {
		store.snaps[i].Store(newSnapshot(ns, 0, time.Time{}))
	}
	return store
}

// replace swaps one namespace pointer. The previous snapshot stays valid for
// readers that already loaded it.
func (s *Store) replace(ns Namespace, snap *Snapshot) {
	i := namespaceIndex(ns)
	if i < 0 || snap == nil {
		return
	}
	s.snaps[i].Store(snap)
}

func (s *Store) replaceAll(snaps []*Snapshot) {
	for _, snap := range snaps {
		if snap != nil {
			s.replace(snap.Namespace(), snap)
		}
	}
}

// Snapshot returns the current immutable snapshot for ns.
func (s *Store) Snapshot(ns Namespace) *Snapshot {
	i := namespaceIndex(ns)
	if i < 0 {
		return nil
	}
	return s.snaps[i].Load()
}

// GlobalVersion is the minimum namespace version. A full refresh sets every
// namespace to the same version; a partial refresh leaves the minimum behind
// so the next poll retries.
func (s *Store) GlobalVersion() int64 {
	min := int64(-1)
	for i := range s.snaps {
		snap := s.snaps[i].Load()
		if snap == nil {
			continue
		}
		if min < 0 || snap.version < min {
			min = snap.version
		}
	}
	if min < 0 {
		return 0
	}
	return min
}

// Value routes a key to its namespace snapshot.
func (s *Store) Value(key Key) (Value, bool) {
	m, ok := metaByKey[key]
	if !ok || m.Namespace == NsUnassigned {
		return Value{}, false
	}
	snap := s.Snapshot(m.Namespace)
	if snap == nil {
		return Value{}, false
	}
	return snap.Value(key)
}

// Int reads an integer key through the namespace snapshot.
func (s *Store) Int(key Key) (int64, bool) {
	v, ok := s.Value(key)
	if !ok {
		return 0, false
	}
	return v.Int()
}

// Bool reads a boolean key through the namespace snapshot.
func (s *Store) Bool(key Key) (bool, bool) {
	v, ok := s.Value(key)
	if !ok {
		return false, false
	}
	return v.Bool()
}

// String reads an enum or string key through the namespace snapshot.
func (s *Store) String(key Key) (string, bool) {
	v, ok := s.Value(key)
	if !ok {
		return "", false
	}
	return v.String()
}

// Decimal reads a decimal key through the namespace snapshot.
func (s *Store) Decimal(key Key) (Decimal, bool) {
	v, ok := s.Value(key)
	if !ok {
		return 0, false
	}
	return v.Decimal()
}

// wireSnapshot is the JSON payload stored at cfg:snap:{namespace}.
type wireSnapshot struct {
	Namespace string                      `json:"namespace"`
	Version   int64                       `json:"version"`
	Values    map[string]jsonx.RawMessage `json:"values"`
}

func encodeSnapshot(s *Snapshot) ([]byte, error) {
	values := make(map[string]jsonx.RawMessage, len(s.values)+len(s.extra))
	for name, value := range s.extra {
		values[name] = value
	}
	for key, value := range s.values {
		values[string(key)] = value.Raw()
	}
	return jsonx.Marshal(wireSnapshot{
		Namespace: string(s.namespace),
		Version:   s.version,
		Values:    values,
	})
}

// checkSnapshotSize applies the 256 KB bound to an encoded payload.
func checkSnapshotSize(ns Namespace, payload []byte) error {
	if len(payload) >= MaxSnapshotBytes {
		return fmt.Errorf("config: %s snapshot is %d bytes, limit is %d bytes; split the namespace",
			ns, len(payload), MaxSnapshotBytes)
	}
	return nil
}

// VersionSkewError reports a read that could not obtain a snapshot matching
// the observed version. It is returned, never swallowed, and the caller keeps
// the previous snapshot.
type VersionSkewError struct {
	Namespace  Namespace
	Attempts   int
	FirstRead  int64
	SecondRead int64
	Snapshot   int64
}

func (e *VersionSkewError) Error() string {
	return fmt.Sprintf("config: version skew for %s after %d attempt(s): cfg:ver reads %d/%d, snapshot version %d",
		e.Namespace, e.Attempts, e.FirstRead, e.SecondRead, e.Snapshot)
}

// decodeSnapshot parses one payload against expectedVersion. Invalid fields
// fall back to their built-in defaults; only a malformed frame or a version
// mismatch fails the whole namespace.
func decodeSnapshot(ns Namespace, payload []byte, expectedVersion int64, now time.Time) (*Snapshot, error) {
	var wire wireSnapshot
	if err := jsonx.Unmarshal(payload, &wire); err != nil {
		return nil, fmt.Errorf("config: decode %s snapshot: %w", ns, err)
	}
	if wire.Namespace != "" && wire.Namespace != string(ns) {
		return nil, fmt.Errorf("config: %s snapshot declares namespace %q", ns, wire.Namespace)
	}
	if wire.Version != expectedVersion {
		return nil, &VersionSkewError{
			Namespace: ns, FirstRead: expectedVersion, SecondRead: expectedVersion, Snapshot: wire.Version,
		}
	}

	snap := newSnapshot(ns, wire.Version, now)
	if len(KeysInNamespace(ns)) == 0 {
		// Keyless namespace (ROUTING): every member is opaque route/price
		// data. Preserve it verbatim.
		for name, rawValue := range wire.Values {
			snap.extra[name] = rawValue
		}
		return snap, nil
	}
	for rawKey, rawValue := range wire.Values {
		key := Key(rawKey)
		m, ok := metaByKey[key]
		if !ok {
			snap.fallback(key, "unknown key")
			continue
		}
		if m.Namespace != ns || ns == NsUnassigned {
			snap.fallback(key, "key belongs to another namespace")
			continue
		}
		value, err := Parse(key, rawValue)
		if err != nil {
			snap.fallback(key, "invalid value: "+err.Error())
			continue
		}
		if m.Strategy != "" {
			name, _ := value.String()
			if _, ok := Resolve(m.Strategy, name); !ok {
				snap.fallback(key, "strategy not registered: "+name)
				continue
			}
		}
		snap.values[key] = value
	}
	return snap, nil
}

// Loader performs the version-consistent read protocol. It never mutates a
// published Snapshot; a successful read swaps pointers.
type Loader struct {
	Redis       Redis
	Store       *Store
	Logger      Logger
	Now         func() time.Time
	MaxAttempts int
}

var errNoVersion = errors.New("config: cfg:ver is absent")

func (l *Loader) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return clock.Real{}.Now()
}

func (l *Loader) logger() Logger {
	if l.Logger != nil {
		return l.Logger
	}
	return nopLogger{}
}

func (l *Loader) attempts() int {
	if l.MaxAttempts > 0 {
		return l.MaxAttempts
	}
	return 3
}

func (l *Loader) readVersion(ctx context.Context) (int64, bool, error) {
	raw, err := l.Redis.Get(ctx, RedisVersionKey)
	if errors.Is(err, ErrKeyNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("config: read cfg:ver: %w", err)
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("config: cfg:ver %q is not an integer: %w", raw, err)
	}
	return v, true, nil
}

// RefreshAll fetches all five namespaces under one version observation and
// replaces them together. A failed attempt leaves every pointer untouched.
func (l *Loader) RefreshAll(ctx context.Context) error {
	if l.Redis == nil || l.Store == nil {
		return fmt.Errorf("config: loader requires redis and store")
	}
	target, exists, err := l.readVersion(ctx)
	if err != nil {
		l.logger().Warn(ctx, "config_version_read_failed: "+err.Error())
		return err
	}
	if !exists {
		return nil
	}
	if l.Store.GlobalVersion() == target {
		return nil
	}

	var lastErr error
	for attempt := 1; attempt <= l.attempts(); attempt++ {
		version, payloads, err := l.fetchAllOnce(ctx)
		if err != nil {
			if errors.Is(err, errNoVersion) {
				return nil
			}
			var skew *VersionSkewError
			if errors.As(err, &skew) {
				skew.Attempts = attempt
				lastErr = skew
				continue
			}
			l.logger().Warn(ctx, "config_fetch_failed: "+err.Error())
			return err
		}

		snaps := make([]*Snapshot, 0, len(SnapshotNamespaces))
		for _, ns := range SnapshotNamespaces {
			snap, err := decodeSnapshot(ns, payloads[ns], version, l.now())
			if err != nil {
				var skew *VersionSkewError
				if errors.As(err, &skew) {
					skew.Attempts = attempt
					lastErr = skew
					snaps = nil
					break
				}
				l.logger().Warn(ctx, "config_parse_failed: "+err.Error())
				return err
			}
			snaps = append(snaps, snap)
		}
		if snaps == nil {
			continue
		}
		l.Store.replaceAll(snaps)
		l.logFallbacks(ctx, snaps)
		return nil
	}
	if lastErr == nil {
		lastErr = &VersionSkewError{Attempts: l.attempts()}
	}
	l.logger().Warn(ctx, "config_version_skew: "+lastErr.Error())
	return lastErr
}

// RefreshNamespace is the single-namespace variant used by targeted tests and
// by a caller that already knows which partition changed.
func (l *Loader) RefreshNamespace(ctx context.Context, ns Namespace) error {
	if l.Redis == nil || l.Store == nil {
		return fmt.Errorf("config: loader requires redis and store")
	}
	if namespaceIndex(ns) < 0 {
		return fmt.Errorf("config: %q is not a snapshot namespace", ns)
	}

	var lastErr error
	for attempt := 1; attempt <= l.attempts(); attempt++ {
		version, payload, err := l.fetchNamespaceOnce(ctx, ns)
		if err != nil {
			if errors.Is(err, errNoVersion) {
				return nil
			}
			var skew *VersionSkewError
			if errors.As(err, &skew) {
				skew.Attempts = attempt
				lastErr = skew
				continue
			}
			l.logger().Warn(ctx, "config_fetch_failed: "+err.Error())
			return err
		}
		snap, err := decodeSnapshot(ns, payload, version, l.now())
		if err != nil {
			var skew *VersionSkewError
			if errors.As(err, &skew) {
				skew.Attempts = attempt
				lastErr = skew
				continue
			}
			l.logger().Warn(ctx, "config_parse_failed: "+err.Error())
			return err
		}
		l.Store.replace(ns, snap)
		l.logFallbacks(ctx, []*Snapshot{snap})
		return nil
	}
	if lastErr == nil {
		lastErr = &VersionSkewError{Namespace: ns, Attempts: l.attempts()}
	}
	l.logger().Warn(ctx, "config_version_skew: "+lastErr.Error())
	return lastErr
}

func (l *Loader) fetchAllOnce(ctx context.Context) (int64, map[Namespace][]byte, error) {
	first, exists, err := l.readVersion(ctx)
	if err != nil {
		return 0, nil, err
	}
	if !exists {
		return 0, nil, errNoVersion
	}
	payloads := make(map[Namespace][]byte, len(SnapshotNamespaces))
	for _, ns := range SnapshotNamespaces {
		raw, err := l.Redis.Get(ctx, SnapshotRedisKey(ns))
		if errors.Is(err, ErrKeyNotFound) {
			return 0, nil, fmt.Errorf("config: snapshot %s is missing", ns)
		}
		if err != nil {
			return 0, nil, fmt.Errorf("config: read snapshot %s: %w", ns, err)
		}
		payloads[ns] = []byte(raw)
	}
	second, exists, err := l.readVersion(ctx)
	if err != nil {
		return 0, nil, err
	}
	if !exists {
		return 0, nil, errNoVersion
	}
	if first != second {
		return 0, nil, &VersionSkewError{FirstRead: first, SecondRead: second, Snapshot: -1}
	}
	return first, payloads, nil
}

func (l *Loader) fetchNamespaceOnce(ctx context.Context, ns Namespace) (int64, []byte, error) {
	first, exists, err := l.readVersion(ctx)
	if err != nil {
		return 0, nil, err
	}
	if !exists {
		return 0, nil, errNoVersion
	}
	raw, err := l.Redis.Get(ctx, SnapshotRedisKey(ns))
	if errors.Is(err, ErrKeyNotFound) {
		return 0, nil, fmt.Errorf("config: snapshot %s is missing", ns)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("config: read snapshot %s: %w", ns, err)
	}
	second, exists, err := l.readVersion(ctx)
	if err != nil {
		return 0, nil, err
	}
	if !exists {
		return 0, nil, errNoVersion
	}
	if first != second {
		return 0, nil, &VersionSkewError{
			Namespace: ns, FirstRead: first, SecondRead: second, Snapshot: -1,
		}
	}
	return first, []byte(raw), nil
}

func (l *Loader) logFallbacks(ctx context.Context, snaps []*Snapshot) {
	for _, snap := range snaps {
		for _, fb := range snap.Fallbacks() {
			l.logger().Warn(ctx, "config_field_fallback: "+string(snap.Namespace())+": "+fb)
		}
	}
}

// NewStoreFromDB is the first-start path for a process that can reach the
// configuration table. It builds every namespace from the current rows and
// stamps all of them with the highest row version, so a later publish with a
// higher cfg:ver is always observed as newer.
func NewStoreFromDB(ctx context.Context, db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("config: NewStoreFromDB requires a database")
	}
	rows, err := loadItemRows(ctx, db)
	if err != nil {
		return nil, err
	}
	var version int64
	for _, row := range rows {
		if row.Version > version {
			version = row.Version
		}
	}
	store := &Store{}
	for i, ns := range SnapshotNamespaces {
		snap := newSnapshot(ns, version, clock.Real{}.Now())
		for _, row := range rows {
			if row.Namespace != ns || ns == NsUnassigned {
				continue
			}
			applyRow(snap, row)
		}
		store.snaps[i].Store(snap)
	}
	return store, nil
}

// applyRow applies one t_config_item row using the same per-field fallback
// rules as decodeSnapshot.
func applyRow(snap *Snapshot, row itemRow) {
	m, ok := metaByKey[row.Key]
	if !ok {
		snap.fallback(row.Key, "unknown key")
		return
	}
	if m.Namespace != row.Namespace || row.Namespace == NsUnassigned {
		snap.fallback(row.Key, "key belongs to another namespace")
		return
	}
	value, err := Parse(row.Key, row.Raw)
	if err != nil {
		snap.fallback(row.Key, "invalid value: "+err.Error())
		return
	}
	if m.Strategy != "" {
		name, _ := value.String()
		if _, ok := Resolve(m.Strategy, name); !ok {
			snap.fallback(row.Key, "strategy not registered: "+name)
			return
		}
	}
	snap.values[row.Key] = value
}

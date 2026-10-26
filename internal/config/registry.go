package config

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"sync"

	"yunshu.dev/core/internal/jsonx"
)

// StrategyKind names a family of C2 implementations. The value is stable: it
// appears in logs and in the strategy_fallback metric label.
type StrategyKind string

// The eight strategy families selected by configuration keys. The first five
// names match TDS §3.8; reset_scope, tier_chain and group_switch_mode were
// added with the corresponding C2 keys.
const (
	KindDeductPolicy    StrategyKind = "deduct_policy"
	KindRefundPolicy    StrategyKind = "refund_policy"
	KindCostSource      StrategyKind = "cost_source"
	KindDailyMeter      StrategyKind = "daily_meter"
	KindKeyDailyQuota   StrategyKind = "key_daily_quota"
	KindResetScope      StrategyKind = "reset_scope"
	KindTierChain       StrategyKind = "tier_chain"
	KindGroupSwitchMode StrategyKind = "group_switch_mode"
)

func (k StrategyKind) valid() bool {
	switch k {
	case KindDeductPolicy, KindRefundPolicy, KindCostSource, KindDailyMeter,
		KindKeyDailyQuota, KindResetScope, KindTierChain, KindGroupSwitchMode:
		return true
	default:
		return false
	}
}

var (
	registryMu sync.RWMutex
	registry   = make(map[StrategyKind]map[string]any)
)

// Register adds one implementation. It is called by the implementation
// package's init() (reverse registration, TDS §3.8): config never imports a
// business package. Duplicate names are a programming error and panic at
// startup rather than silently choosing one implementation.
//
// Register is intentionally not called from NewStore or any other
// configuration path: registration is an explicit process-startup act.
func Register[T any](kind StrategyKind, name string, impl T) {
	if !kind.valid() {
		panic("config: Register called with unknown strategy kind " + string(kind))
	}
	if strings.TrimSpace(name) == "" {
		panic("config: Register called with empty name for " + string(kind))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	byName := registry[kind]
	if byName == nil {
		byName = make(map[string]any)
		registry[kind] = byName
	}
	if _, exists := byName[name]; exists {
		panic("config: duplicate strategy " + string(kind) + "/" + name)
	}
	byName[name] = impl
}

// Resolve returns the registered implementation. It never panics: a missing
// name is a runtime fallback, not a request failure (TDS §3.8).
func Resolve(kind StrategyKind, name string) (any, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	byName := registry[kind]
	if byName == nil {
		return nil, false
	}
	impl, ok := byName[name]
	return impl, ok
}

// ResolveAs resolves and type-asserts in one step. A resolved implementation
// of the wrong type is treated as missing so the caller can fall back.
func ResolveAs[T any](kind StrategyKind, name string) (T, bool) {
	var zero T
	impl, ok := Resolve(kind, name)
	if !ok {
		return zero, false
	}
	typed, ok := impl.(T)
	if !ok {
		return zero, false
	}
	return typed, true
}

// RegisteredNames returns the sorted names registered for kind.
func RegisteredNames(kind StrategyKind) []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	byName := registry[kind]
	out := make([]string, 0, len(byName))
	for name := range byName {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// RawItem is one enabled t_config_item row as read by the startup gate.
type RawItem struct {
	Key Key
	Raw jsonx.RawMessage
}

// StrategyProblem is one failed strategy reference.
type StrategyProblem struct {
	Key        Key
	Kind       StrategyKind
	Configured string
	Reason     string
	Available  []string
}

// StrategyStartupError lists every unregistered strategy reference instead of
// failing on the first one. Startup must be able to show all broken keys.
type StrategyStartupError struct {
	Problems []StrategyProblem
}

func (e *StrategyStartupError) Error() string {
	var b strings.Builder
	b.WriteString("config: strategy startup check failed")
	for _, p := range e.Problems {
		available := p.Available
		if len(available) == 0 {
			available = []string{"<none registered>"}
		}
		fmt.Fprintf(&b, "; %s=%q (%s): %s; available: %s",
			p.Key, p.Configured, p.Kind, p.Reason, strings.Join(available, ", "))
	}
	return b.String()
}

// CheckStrategyRefs is the startup gate. Every configured strategy name must
// have a registered implementation; otherwise the process must not start
// (T05 acceptance A3). Unknown and non-strategy keys are ignored here: they
// are validated by the ordinary value path.
func CheckStrategyRefs(items []RawItem) error {
	var problems []StrategyProblem
	for _, item := range items {
		m, ok := metaByKey[item.Key]
		if !ok || m.Strategy == "" {
			continue
		}
		// The startup gate asks "is this strategy implementable", not "is this
		// enum value in the V1.0 domain". Reading the string directly lets an
		// unknown name be reported as "not registered" with the available
		// implementations, which is the actionable message (acceptance A3).
		var configured string
		if err := jsonx.Unmarshal(item.Raw, &configured); err != nil {
			problems = append(problems, StrategyProblem{
				Key: item.Key, Kind: m.Strategy, Configured: short(item.Raw),
				Reason: "configured value is not a strategy name", Available: RegisteredNames(m.Strategy),
			})
			continue
		}
		if _, ok := Resolve(m.Strategy, configured); !ok {
			problems = append(problems, StrategyProblem{
				Key: item.Key, Kind: m.Strategy, Configured: configured,
				Reason: "strategy is not registered", Available: RegisteredNames(m.Strategy),
			})
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Slice(problems, func(i, j int) bool { return problems[i].Key < problems[j].Key })
	return &StrategyStartupError{Problems: problems}
}

// StartupCheck reads the enabled global configuration rows and runs the
// strategy gate. It is called from each process's startup path after the
// database is reachable and before the listeners start.
func StartupCheck(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("config: startup check requires a database")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT cfg_key, cfg_value
		  FROM t_config_item
		 WHERE scope = 'GLOBAL' AND scope_id = 0 AND enabled = 1`)
	if err != nil {
		return fmt.Errorf("config: read config items for startup check: %w", err)
	}
	defer rows.Close()

	var items []RawItem
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return fmt.Errorf("config: scan config item: %w", err)
		}
		items = append(items, RawItem{Key: Key(key), Raw: jsonx.RawMessage(value)})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("config: iterate config items: %w", err)
	}
	return CheckStrategyRefs(items)
}

// registrySnapshot and restoreRegistry exist for tests that need to prove the
// startup gate without unregistering the built-ins for other tests.
func registrySnapshot() map[StrategyKind]map[string]any {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make(map[StrategyKind]map[string]any, len(registry))
	for kind, byName := range registry {
		cp := make(map[string]any, len(byName))
		for name, impl := range byName {
			cp[name] = impl
		}
		out[kind] = cp
	}
	return out
}

func restoreRegistry(snapshot map[StrategyKind]map[string]any) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = snapshot
}

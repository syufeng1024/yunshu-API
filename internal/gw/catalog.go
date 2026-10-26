package gw

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"

	"yunshu.dev/core/internal/config"
	"yunshu.dev/core/internal/jsonx"
)

// Region values (PRD §3.4). V1.0 routes DOMESTIC only; OVERSEAS is excluded by
// a hard filter, never by priority.
const (
	RegionDomestic = "DOMESTIC"
	RegionOverseas = "OVERSEAS"
)

// Listing states (PRD §3.4).
const (
	ListingListed      = "LISTED"
	ListingCatalogOnly = "CATALOG_ONLY"
	ListingDelisted    = "DELISTED"
)

// Model types.
const (
	ModelTypeText      = "TEXT"
	ModelTypeImage     = "IMAGE"
	ModelTypeEmbedding = "EMBEDDING"
)

// Scope modes shared by channels, groups and keys.
const (
	ScopeAll      = "ALL"
	ScopeSelected = "SELECTED"
)

// RoutingSnapshot is the decoded cfg:snap:ROUTING payload. The keyless
// namespace is a bag of members; the control-plane route/price loader writes
// them and this decoder is the only reader (TDS §1.4 / TA-1.1).
type RoutingSnapshot struct {
	Models         []ModelEntry
	Aliases        map[string]string
	Routes         []RouteEntry
	Channels       []ChannelEntry
	Groups         map[string]GroupEntry
	KeyModels      map[string]KeyModelGrant
	DedicatedUsers map[string][]int64
	IPRules        map[string]IPRule
	RateLimits     map[string]LimitPolicy

	modelByName  map[string]*ModelEntry
	modelByID    map[int64]*ModelEntry
	routeByModel map[int64][]RouteEntry
	channelByID  map[int64]*ChannelEntry
}

// ModelEntry is one t_model row reduced to what the inbound half needs.
type ModelEntry struct {
	ID                 int64  `json:"id"`
	ModelName          string `json:"model_name"`
	Region             string `json:"region"`
	ModelType          string `json:"model_type"`
	ContextLength      int64  `json:"context_length"`
	MaxOutputTokens    int64  `json:"max_output_tokens"`
	ListingState       string `json:"listing_state"`
	DefaultPriceRuleID int64  `json:"default_price_rule_id"`
	CapabilityMask     int64  `json:"capability_mask"`
}

// RouteEntry is one t_route row.
type RouteEntry struct {
	ID                int64    `json:"id"`
	ModelID           int64    `json:"model_id"`
	ChannelID         int64    `json:"channel_id"`
	ProtocolID        int64    `json:"protocol_id"`
	UpstreamModelName string   `json:"upstream_model_name"`
	Region            string   `json:"region"`
	Weight            int64    `json:"weight"`
	Priority          int64    `json:"priority"`
	StreamSupported   flexBool `json:"stream_supported"`
	MaxTokensCeiling  int64    `json:"max_tokens_ceiling"`
	Enabled           flexBool `json:"enabled"`
}

// ChannelEntry is one t_channel row reduced to routing fields.
type ChannelEntry struct {
	ID                 int64    `json:"id"`
	Name               string   `json:"name"`
	ProtocolID         int64    `json:"protocol_id"`
	Region             string   `json:"region"`
	Weight             int64    `json:"weight"`
	Priority           int64    `json:"priority"`
	GroupScopeMode     string   `json:"group_scope_mode"`
	DedicatedScopeMode string   `json:"dedicated_scope_mode"`
	FallbackAllowed    flexBool `json:"fallback_allowed"`
	StreamSupported    flexBool `json:"stream_supported"`
	Enabled            flexBool `json:"enabled"`
}

// GroupEntry is one t_price_group row plus its scope sets.
type GroupEntry struct {
	Code             string   `json:"code"`
	RatioBP          int64    `json:"ratio_bp"`
	ModelScopeMode   string   `json:"model_scope_mode"`
	ChannelScopeMode string   `json:"channel_scope_mode"`
	Enabled          flexBool `json:"enabled"`
	Models           []int64  `json:"models"`
	Channels         []int64  `json:"channels"`
}

// KeyModelGrant is the t_api_key_model projection for one key.
type KeyModelGrant struct {
	Mode     string  `json:"mode"`
	ModelIDs []int64 `json:"model_ids"`
}

// IPRule is one t_api_key_ip_rule projection. Version is the value stored in
// K13.ipm; a mismatch means the snapshot and the key cache disagree.
type IPRule struct {
	Version int64    `json:"version"`
	CIDRs   []string `json:"cidrs"`
}

// LimitPolicy is the resolved four-dimension policy for one scope. Daily is
// enabled only when DailyQuotaMicro > 0 (FREE_DEFAULT has no daily ceiling).
type LimitPolicy struct {
	RPM             int64 `json:"rpm"`
	TPM             int64 `json:"tpm"`
	Concurrency     int64 `json:"concurrency"`
	DailyQuotaMicro int64 `json:"daily_quota_micro"`
}

// Limits is the four-dimension input of ratelimit.Acquire.
type Limits struct {
	RPM             int64
	TPM             int64
	Concurrency     int64
	DailyQuotaMicro int64
	DailyEnabled    bool
}

// strKey renders a numeric id as the map key used by the routing snapshot.
func strKey(id int64) string { return strconv.FormatInt(id, 10) }

// flexBool accepts true/false, 0/1 and "0"/"1". The control plane reads
// tinyint(1) columns and the snapshot producer may serialize either form.
type flexBool bool

// UnmarshalJSON implements json.Unmarshaler.
func (b *flexBool) UnmarshalJSON(data []byte) error {
	var asBool bool
	if err := jsonx.Unmarshal(data, &asBool); err == nil {
		*b = flexBool(asBool)
		return nil
	}
	var asInt int64
	if err := jsonx.Unmarshal(data, &asInt); err == nil {
		*b = flexBool(asInt != 0)
		return nil
	}
	var asString string
	if err := jsonx.Unmarshal(data, &asString); err == nil {
		parsed, err := strconv.ParseInt(asString, 10, 64)
		if err == nil {
			*b = flexBool(parsed != 0)
			return nil
		}
	}
	return fmt.Errorf("gw: %s is not a boolean", string(data))
}

func (b flexBool) Bool() bool { return bool(b) }

// Catalog decodes cfg:snap:ROUTING once per snapshot version. Snapshots are
// immutable pointers, so an identity comparison is a correct cache key.
type Catalog struct {
	store  *config.Store
	static *RoutingSnapshot

	mu      sync.Mutex
	last    *config.Snapshot
	decoded *RoutingSnapshot
}

// NewCatalog builds a catalog over the live configuration store.
func NewCatalog(store *config.Store) *Catalog { return &Catalog{store: store} }

// NewStaticCatalog is the test/injection constructor.
func NewStaticCatalog(s *RoutingSnapshot) *Catalog {
	if s == nil {
		s = &RoutingSnapshot{}
	}
	s.prepare()
	return &Catalog{static: s}
}

// Current returns the decoded routing snapshot.
func (c *Catalog) Current() (*RoutingSnapshot, error) {
	if c == nil {
		return (&RoutingSnapshot{}).prepare(), nil
	}
	if c.static != nil {
		return c.static, nil
	}
	if c.store == nil {
		return (&RoutingSnapshot{}).prepare(), nil
	}
	snap := c.store.Snapshot(config.NsRouting)
	if snap == nil {
		return nil, errors.New("gw: routing snapshot is missing")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == snap && c.decoded != nil {
		return c.decoded, nil
	}
	decoded, err := decodeRouting(snap)
	if err != nil {
		return nil, err
	}
	c.last = snap
	c.decoded = decoded
	return decoded, nil
}

// prepare builds the lookup indexes and defaults.
func (s *RoutingSnapshot) prepare() *RoutingSnapshot {
	if s.Aliases == nil {
		s.Aliases = map[string]string{}
	}
	if s.Groups == nil {
		s.Groups = map[string]GroupEntry{}
	}
	if s.KeyModels == nil {
		s.KeyModels = map[string]KeyModelGrant{}
	}
	if s.DedicatedUsers == nil {
		s.DedicatedUsers = map[string][]int64{}
	}
	if s.IPRules == nil {
		s.IPRules = map[string]IPRule{}
	}
	if s.RateLimits == nil {
		s.RateLimits = map[string]LimitPolicy{}
	}
	s.modelByName = make(map[string]*ModelEntry, len(s.Models))
	s.modelByID = make(map[int64]*ModelEntry, len(s.Models))
	for i := range s.Models {
		m := &s.Models[i]
		s.modelByName[m.ModelName] = m
		s.modelByID[m.ID] = m
	}
	s.routeByModel = make(map[int64][]RouteEntry, len(s.Models))
	for _, r := range s.Routes {
		s.routeByModel[r.ModelID] = append(s.routeByModel[r.ModelID], r)
	}
	s.channelByID = make(map[int64]*ChannelEntry, len(s.Channels))
	for i := range s.Channels {
		s.channelByID[s.Channels[i].ID] = &s.Channels[i]
	}
	for code, g := range s.Groups {
		if g.Code == "" {
			g.Code = code
			s.Groups[code] = g
		}
	}
	return s
}

// ModelByName resolves an exact, case-sensitive model name.
func (s *RoutingSnapshot) ModelByName(name string) *ModelEntry {
	return s.modelByName[name]
}

// ModelByAlias resolves an alias to its canonical model.
func (s *RoutingSnapshot) ModelByAlias(alias string) *ModelEntry {
	name, ok := s.Aliases[alias]
	if !ok {
		return nil
	}
	return s.modelByName[name]
}

// RoutesFor returns the routes of one model ordered by priority then id. The
// weighted shuffle happens in the router stage, after every filter.
func (s *RoutingSnapshot) RoutesFor(modelID int64) []RouteEntry {
	out := append([]RouteEntry(nil), s.routeByModel[modelID]...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Channel returns the channel row.
func (s *RoutingSnapshot) Channel(id int64) (*ChannelEntry, bool) {
	ch, ok := s.channelByID[id]
	return ch, ok
}

// Group returns one price group definition.
func (s *RoutingSnapshot) Group(code string) (GroupEntry, bool) {
	g, ok := s.Groups[code]
	return g, ok
}

// KeyGrant returns the key's model authorization. An absent entry is treated
// as SELECTED with no models: the safest reading of "this key was not listed".
func (s *RoutingSnapshot) KeyGrant(keyID int64) KeyModelGrant {
	g, ok := s.KeyModels[strconv.FormatInt(keyID, 10)]
	if !ok {
		return KeyModelGrant{Mode: ScopeSelected}
	}
	if g.Mode == "" {
		g.Mode = ScopeSelected
	}
	return g
}

// IsDedicated reports whether userID is on the channel's dedicated list.
func (s *RoutingSnapshot) IsDedicated(channelID, userID int64) bool {
	for _, id := range s.DedicatedUsers[strconv.FormatInt(channelID, 10)] {
		if id == userID {
			return true
		}
	}
	return false
}

// IPRuleFor returns the whitelist entry of one key.
func (s *RoutingSnapshot) IPRuleFor(keyID int64) (IPRule, bool) {
	r, ok := s.IPRules[strconv.FormatInt(keyID, 10)]
	return r, ok
}

// RateLimitFor resolves the key policy first, then the user policy for the
// daily dimension. Missing entries fall back to the FREE_DEFAULT spec and are
// reported to the caller so it can WARN once.
func (s *RoutingSnapshot) RateLimitFor(keyID, userID int64) (Limits, bool) {
	keyPolicy, keyOK := s.RateLimits["KEY:"+strconv.FormatInt(keyID, 10)]
	userPolicy, userOK := s.RateLimits["USER:"+strconv.FormatInt(userID, 10)]
	limits := Limits{
		RPM:             keyPolicy.RPM,
		TPM:             keyPolicy.TPM,
		Concurrency:     keyPolicy.Concurrency,
		DailyQuotaMicro: userPolicy.DailyQuotaMicro,
		DailyEnabled:    userPolicy.DailyQuotaMicro > 0,
	}
	if limits.RPM <= 0 && limits.TPM <= 0 && limits.Concurrency <= 0 {
		fallback := FreeDefaultLimits()
		// The daily dimension is per-user and lives in a different policy row;
		// a missing KEY row must not erase it.
		fallback.DailyQuotaMicro = userPolicy.DailyQuotaMicro
		fallback.DailyEnabled = userPolicy.DailyQuotaMicro > 0
		return fallback, false
	}
	return limits, keyOK || userOK
}

// FREE_DEFAULT values (PRD §3.2.1): RPM 60 / TPM 60,000 / concurrency 5 /
// no daily ceiling. They are the documented fallback when the snapshot has no
// policy row for a key, and are logged so a missing snapshot is visible.
const (
	freeDefaultRPM         = 60
	freeDefaultTPM         = 60_000
	freeDefaultConcurrency = 5
)

// FreeDefaultLimits returns the PRD's unpurchased-plan limits.
func FreeDefaultLimits() Limits {
	return Limits{RPM: freeDefaultRPM, TPM: freeDefaultTPM, Concurrency: freeDefaultConcurrency}
}

// HasModel reports whether group g includes modelID. ALL means every model.
func (g GroupEntry) HasModel(modelID int64) bool {
	if g.ModelScopeMode != ScopeSelected {
		return true
	}
	for _, id := range g.Models {
		if id == modelID {
			return true
		}
	}
	return false
}

// HasChannel reports whether group g can see channelID. ALL means every
// channel.
func (g GroupEntry) HasChannel(channelID int64) bool {
	if g.ChannelScopeMode != ScopeSelected {
		return true
	}
	for _, id := range g.Channels {
		if id == channelID {
			return true
		}
	}
	return false
}

// ListableModels is the /v1/models set: key grant ∩ LISTED ∩ DOMESTIC ∩ group
// range. CATALOG_ONLY and DELISTED never appear (TDS §6.1).
func (s *RoutingSnapshot) ListableModels(keyID int64, groupCode string) []ModelEntry {
	grant := s.KeyGrant(keyID)
	group, groupOK := s.Group(groupCode)
	if !groupOK {
		// P07 denies an unknown group; the listing must agree, otherwise a
		// model would be advertised and then rejected on the first call.
		return nil
	}
	authorized := make(map[int64]bool, len(grant.ModelIDs))
	for _, id := range grant.ModelIDs {
		authorized[id] = true
	}
	out := make([]ModelEntry, 0, len(s.Models))
	for _, m := range s.Models {
		if m.Region != RegionDomestic {
			continue
		}
		if m.ListingState != ListingListed {
			continue
		}
		if grant.Mode != ScopeAll && !authorized[m.ID] {
			continue
		}
		if !group.HasModel(m.ID) {
			continue
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ModelType != out[j].ModelType {
			return out[i].ModelType < out[j].ModelType
		}
		return out[i].ModelName < out[j].ModelName
	})
	return out
}

// decodeRouting turns the opaque members of cfg:snap:ROUTING into typed
// structures. A malformed member fails the whole snapshot: serving half a
// routing table would silently drop models or channels.
func decodeRouting(snap *config.Snapshot) (*RoutingSnapshot, error) {
	rs := &RoutingSnapshot{}
	decode := func(name string, dst any) error {
		raw, ok := snap.Extra(name)
		if !ok || len(raw) == 0 {
			return nil
		}
		if err := jsonx.Unmarshal(raw, dst); err != nil {
			return fmt.Errorf("gw: routing snapshot member %q: %w", name, err)
		}
		return nil
	}
	for name, dst := range map[string]any{
		"models":         &rs.Models,
		"aliases":        &rs.Aliases,
		"routes":         &rs.Routes,
		"channels":       &rs.Channels,
		"groups":         &rs.Groups,
		"keyModels":      &rs.KeyModels,
		"dedicatedUsers": &rs.DedicatedUsers,
		"ipRules":        &rs.IPRules,
		"rateLimits":     &rs.RateLimits,
	} {
		if err := decode(name, dst); err != nil {
			return nil, err
		}
	}
	return rs.prepare(), nil
}

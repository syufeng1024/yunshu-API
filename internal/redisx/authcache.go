package redisx

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// K13 is the gateway's only API-key source. gatewayd has no primary-DB pool
// (TDS §1), so a miss cannot fall back to MySQL: the caller must answer 401.
// The control plane writes through on create/enable/disable/rename/regroup.
//
// K13 has no TTL. A TTL would make every key expire at the same time once the
// control plane stops refreshing, with no fallback point — an availability
// single point, not a cache optimization. The operational consequence is
// explicit: losing Redis stops authentication until the control plane
// re-warms the keyspace.

// ErrKeyAuthNotFound means K13 is absent. It is a 401, never a DB lookup.
var ErrKeyAuthNotFound = errors.New("redisx: api key not found")

// KeyAuthRecord is the K13 HASH.
type KeyAuthRecord struct {
	UID int64  // user id
	KID int64  // api key id
	ST  string // status: enabled / disabled
	IPM int64  // per-minute policy marker
	EXP int64  // expiry unix ms; 0 = no expiry
	NT  string // node-config version
	KN  string // key name shown in request logs
	GRP string // price group code
	GP  int64  // group ratio in basis points (10000 = 1.0)
}

// KeyAuthWriter is the write-through surface implemented by the control
// plane. BulkUpdateGroupRatio takes the affected hashes because K13 is not
// indexable by group; the control plane reads them from t_api_key.
type KeyAuthWriter interface {
	Upsert(ctx context.Context, hash16 string, rec KeyAuthRecord) error
	Delete(ctx context.Context, hash16 string) error
	BulkUpdateGroupRatio(ctx context.Context, hashes []string, gp int64) error
}

// KeyAuthReader is the gateway-side read surface.
type KeyAuthReader interface {
	Lookup(ctx context.Context, hash16 string) (KeyAuthRecord, error)
}

// KeyAuthCache implements both interfaces on K13.
type KeyAuthCache struct {
	client *Client
}

// NewKeyAuthCache builds the K13 accessor.
func NewKeyAuthCache(client *Client) *KeyAuthCache { return &KeyAuthCache{client: client} }

// Upsert writes every field with HSET and never sets a TTL.
func (c *KeyAuthCache) Upsert(ctx context.Context, hash16 string, rec KeyAuthRecord) error {
	if err := validateHash16(hash16); err != nil {
		return err
	}
	cctx, cancel := c.client.CommandContext(ctx)
	defer cancel()
	return c.client.Raw().HSet(cctx, APIKeyHashKey(hash16), map[string]interface{}{
		"uid": rec.UID,
		"kid": rec.KID,
		"st":  rec.ST,
		"ipm": rec.IPM,
		"exp": rec.EXP,
		"nt":  rec.NT,
		"kn":  rec.KN,
		"grp": rec.GRP,
		"gp":  rec.GP,
	}).Err()
}

// Delete removes a revoked key.
func (c *KeyAuthCache) Delete(ctx context.Context, hash16 string) error {
	if err := validateHash16(hash16); err != nil {
		return err
	}
	cctx, cancel := c.client.CommandContext(ctx)
	defer cancel()
	return c.client.Raw().Del(cctx, APIKeyHashKey(hash16)).Err()
}

// BulkUpdateGroupRatio updates gp for the supplied keys in one pipeline.
// It never rewrites other fields: a concurrent rename must not be lost.
func (c *KeyAuthCache) BulkUpdateGroupRatio(ctx context.Context, hashes []string, gp int64) error {
	if len(hashes) == 0 {
		return nil
	}
	for _, hash := range hashes {
		if err := validateHash16(hash); err != nil {
			return err
		}
	}
	cctx, cancel := c.client.CommandContext(ctx)
	defer cancel()
	pipe := c.client.Raw().Pipeline()
	for _, hash := range hashes {
		pipe.HSet(cctx, APIKeyHashKey(hash), "gp", gp)
	}
	_, err := pipe.Exec(cctx)
	return err
}

// Lookup reads K13. An empty HASH is ErrKeyAuthNotFound (401), not a zero
// record; treating it as zero would let an unknown key through.
func (c *KeyAuthCache) Lookup(ctx context.Context, hash16 string) (KeyAuthRecord, error) {
	if err := validateHash16(hash16); err != nil {
		return KeyAuthRecord{}, err
	}
	cctx, cancel := c.client.CommandContext(ctx)
	defer cancel()
	values, err := c.client.Raw().HGetAll(cctx, APIKeyHashKey(hash16)).Result()
	if err != nil {
		return KeyAuthRecord{}, err
	}
	if len(values) == 0 {
		return KeyAuthRecord{}, ErrKeyAuthNotFound
	}
	rec := KeyAuthRecord{
		ST:  values["st"],
		NT:  values["nt"],
		KN:  values["kn"],
		GRP: values["grp"],
	}
	if rec.UID, err = parseIntField(values, "uid"); err != nil {
		return KeyAuthRecord{}, err
	}
	if rec.KID, err = parseIntField(values, "kid"); err != nil {
		return KeyAuthRecord{}, err
	}
	if rec.IPM, err = parseIntField(values, "ipm"); err != nil {
		return KeyAuthRecord{}, err
	}
	if rec.EXP, err = parseIntField(values, "exp"); err != nil {
		return KeyAuthRecord{}, err
	}
	if rec.GP, err = parseIntField(values, "gp"); err != nil {
		return KeyAuthRecord{}, err
	}
	return rec, nil
}

// validateHash16 rejects anything that is not 16 lowercase hex characters.
// The prefix is part of the key, so a colon or uppercase would silently
// address a different HASH than the control plane wrote.
func validateHash16(hash16 string) error {
	if len(hash16) != 16 {
		return fmt.Errorf("redisx: api key hash must be 16 hex chars, got %d", len(hash16))
	}
	for i := 0; i < len(hash16); i++ {
		ch := hash16[i]
		if (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') {
			continue
		}
		return fmt.Errorf("redisx: api key hash contains non-lowercase-hex byte %q", ch)
	}
	return nil
}

func parseIntField(values map[string]string, field string) (int64, error) {
	raw, ok := values[field]
	if !ok || raw == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("redisx: K13 field %q is not an integer: %w", field, err)
	}
	return parsed, nil
}

var _ KeyAuthWriter = (*KeyAuthCache)(nil)
var _ KeyAuthReader = (*KeyAuthCache)(nil)

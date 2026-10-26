// Key constructors for TDS §2.1 K1–K26.
//
// Every key in this module is built here. A rate-limit key assembled with
// string concatenation anywhere else is a review reject (T06 hard constraint
// 19), because two spellings of the same key silently split one counter into
// two and double the allowance.
//
// Naming rules: lowercase, colon separated. {min_epoch} is a UTC minute
// ordinal (floor(unix_seconds/60)); {yyyymmdd_cst} is an Asia/Shanghai
// calendar day. The two never share a key, so a timezone change cannot move a
// per-minute counter into a daily one.
package redisx

import (
	"crypto/sha1"
	"encoding/hex"
	"strconv"
)

// MinEpochFromMS converts a corrected unix-millisecond timestamp to the
// per-minute bucket ordinal used by K1/K2.
func MinEpochFromMS(ms int64) int64 { return ms / 60_000 }

// K1 rl:rpm:{key_id}:{min_epoch}
func RPMLKey(keyID, minEpoch int64) string {
	return "rl:rpm:" + strconv.FormatInt(keyID, 10) + ":" + strconv.FormatInt(minEpoch, 10)
}

// K2 rl:tpm:{key_id}:{min_epoch}
func TPMKey(keyID, minEpoch int64) string {
	return "rl:tpm:" + strconv.FormatInt(keyID, 10) + ":" + strconv.FormatInt(minEpoch, 10)
}

// K3 rl:cc:{key_id}
func ConcurrencyKey(keyID int64) string {
	return "rl:cc:" + strconv.FormatInt(keyID, 10)
}

// K4 rl:dq:{user_id}:{yyyymmdd_cst}
func DailyQuotaKey(userID int64, day string) string {
	return "rl:dq:" + strconv.FormatInt(userID, 10) + ":" + day
}

// K5 rl:dqr:{user_id}:{yyyymmdd_cst}
func DailyQuotaRejectedKey(userID int64, day string) string {
	return "rl:dqr:" + strconv.FormatInt(userID, 10) + ":" + day
}

// K6 idem:{user_id}:{sha1(idem_key)}
//
// The hash argument must come from IdemHash, which folds scope and
// api_key_id into the digest. §1.6 requires (scope, api_key_id, idem_key) so
// one Key cannot read another Key's replay body.
func IdemKey(userID int64, hash string) string {
	return "idem:" + strconv.FormatInt(userID, 10) + ":" + hash
}

// K7 idem:lock:{user_id}:{sha1(idem_key)}
func IdemLockKey(userID int64, hash string) string {
	return "idem:lock:" + strconv.FormatInt(userID, 10) + ":" + hash
}

// K8 cfg:ver
func ConfigVersionKey() string { return "cfg:ver" }

// K9 cfg:snap:{namespace}
func ConfigSnapshotKey(namespace string) string { return "cfg:snap:" + namespace }

// K10 cfg:invalidate (Pub/Sub channel)
func ConfigInvalidateChannel() string { return "cfg:invalidate" }

// K11 hold:{request_id}
func HoldKey(requestID string) string { return "hold:" + requestID }

// K12 hold:recover:{request_id}
func HoldRecoverKey(requestID string) string { return "hold:recover:" + requestID }

// K13 ak:{sha256_prefix_16}
func APIKeyHashKey(sha256Prefix16 string) string { return "ak:" + sha256Prefix16 }

// K14 m:{scope_type}:{scope_id}:{min_epoch}
func MetricKey(scopeType string, scopeID, minEpoch int64) string {
	return "m:" + scopeType + ":" + strconv.FormatInt(scopeID, 10) + ":" + strconv.FormatInt(minEpoch, 10)
}

// K15 m:chan:{channel_id}:{min_epoch}
func ChannelMetricKey(channelID, minEpoch int64) string {
	return "m:chan:" + strconv.FormatInt(channelID, 10) + ":" + strconv.FormatInt(minEpoch, 10)
}

// K16 up:cb:{channel_id}
func BreakerKey(channelID int64) string {
	return "up:cb:" + strconv.FormatInt(channelID, 10)
}

// K17 up:probe:{channel_id}
func ProbeKey(channelID int64) string {
	return "up:probe:" + strconv.FormatInt(channelID, 10)
}

// K18 node:set
func NodeSetKey() string { return "node:set" }

// K19 price:{model_id}
func PriceKey(modelID int64) string {
	return "price:" + strconv.FormatInt(modelID, 10)
}

// K20 sess:admin:{session_id}
func AdminSessionKey(sessionID string) string { return "sess:admin:" + sessionID }

// K21 lock:recon:{yyyymmdd}
func ReconLockKey(day string) string { return "lock:recon:" + day }

// K22 lock:expire_batch
func ExpireBatchLockKey() string { return "lock:expire_batch" }

// K23 up:cd:{channel_id}
func CooldownKey(channelID int64) string {
	return "up:cd:" + strconv.FormatInt(channelID, 10)
}

// K24 log:stream
func LogStreamKey() string { return "log:stream" }

// K25 job:heartbeat:{instance}
func JobHeartbeatKey(instance string) string { return "job:heartbeat:" + instance }

// K26 bal:{userID}
func BalanceKey(userID int64) string {
	return "bal:" + strconv.FormatInt(userID, 10)
}

// IdemHash folds the idempotency identity required by §1.6 into the K6/K7
// hash segment. sha1 is used because the key template is defined with it and
// collisions here only affect cache placement, never authorization: the DB
// uniqueness key remains (scope, api_key_id, idem_key) and is checked on
// every miss.
func IdemHash(scope string, apiKeyID int64, idemKey string) string {
	sum := sha1.Sum([]byte(scope + "|" + strconv.FormatInt(apiKeyID, 10) + "|" + idemKey))
	return hex.EncodeToString(sum[:])
}

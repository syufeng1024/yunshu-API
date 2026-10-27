package redisx

import (
	"strings"
	"testing"
)

func TestKeyConstructors(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"K1", RPMLKey(42, 29_179_000), "rl:rpm:42:29179000"},
		{"K2", TPMKey(42, 29_179_000), "rl:tpm:42:29179000"},
		{"K3", ConcurrencyKey(42), "rl:cc:42"},
		{"K4", DailyQuotaKey(7, "20261026"), "rl:dq:7:20261026"},
		{"K5", DailyQuotaRejectedKey(7, "20261026"), "rl:dqr:7:20261026"},
		{"K6", IdemKey(7, "abc"), "idem:7:abc"},
		{"K7", IdemLockKey(7, "abc"), "idem:lock:7:abc"},
		{"K8", ConfigVersionKey(), "cfg:ver"},
		{"K9", ConfigSnapshotKey("ROUTING"), "cfg:snap:ROUTING"},
		{"K10", ConfigInvalidateChannel(), "cfg:invalidate"},
		{"K11", HoldKey("r1"), "hold:r1"},
		{"K12", HoldRecoverKey("r1"), "hold:recover:r1"},
		{"K13", APIKeyHashKey("deadbeefdeadbeef"), "ak:deadbeefdeadbeef"},
		{"K14", MetricKey("key", 42, 29_179_000), "m:key:42:29179000"},
		{"K15", ChannelMetricKey(9, 29_179_000), "m:chan:9:29179000"},
		{"K16", BreakerKey(9), "up:cb:9"},
		{"K17", ProbeKey(9), "up:probe:9"},
		{"K18", NodeSetKey(), "node:set"},
		{"K19", PriceKey(3), "price:3"},
		{"K20", AdminSessionKey("s1"), "sess:admin:s1"},
		{"K21", ReconLockKey("20261026"), "lock:recon:20261026"},
		{"K22", ExpireBatchLockKey(), "lock:expire_batch"},
		{"K23", CooldownKey(9), "up:cd:9"},
		{"K24", LogStreamKey(), "log:stream"},
		{"K25", JobHeartbeatKey("jobsd-1"), "job:heartbeat:jobsd-1"},
		{"K26", BalanceKey(7), "bal:7"},
		{"K27", ExportLockKey(7), "exp:user:7"},
		{"K28", LoginAccountFailKey("alice@example.com"), "auth:fail:acct:ff8d9819fc0e12bf0d24892e45987e249a28dce836a85cad60e28eaaa8c6d976"},
		{"K29", LoginIPFailKey("192.0.2.1"), "auth:fail:ip:37fcff24bf62035b2b08020afc08b4fecd4fcffce57ab23518e3561ff0fe76b9"},
		{"K30", AdminUserSessionSetKey(7), "sess:admin:uid:7"},
	}
	if len(tests) != 30 {
		t.Fatalf("key table has %d entries, want 30", len(tests))
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: got %q want %q", tt.name, tt.got, tt.want)
		}
		// Counter and identity keys are lowercase. K9 keeps the uppercase
		// namespace token because that is the value config publishes
		// (cfg:snap:ROUTING); lowering it would make the node miss the
		// snapshot entirely.
		if tt.name != "K9" {
			if lower := strings.ToLower(tt.got); lower != tt.got {
				t.Errorf("%s: key %q is not lowercase", tt.name, tt.got)
			}
		}
		if strings.Contains(tt.got, " ") || strings.Contains(tt.got, "{") {
			t.Errorf("%s: key %q contains a forbidden separator", tt.name, tt.got)
		}
	}
}

func TestMinEpochFromMS(t *testing.T) {
	const minute = int64(60_000)
	if got := MinEpochFromMS(29_179_999_999); got != 486_333 {
		t.Errorf("MinEpochFromMS = %d want 486333", got)
	}
	if got := MinEpochFromMS(minute*7 + minute - 1); got != 7 {
		t.Errorf("last millisecond of minute 7 = %d want 7", got)
	}
	if got := MinEpochFromMS(minute * 8); got != 8 {
		t.Errorf("first millisecond of minute 8 = %d want 8", got)
	}
}

func TestIdemHashIncludesAPIKey(t *testing.T) {
	a := IdemHash("idem", 1, "same-key")
	b := IdemHash("idem", 2, "same-key")
	if a == b {
		t.Fatal("hash must differ when api_key_id differs; otherwise one Key can read another Key's response")
	}
	if again := IdemHash("idem", 1, "same-key"); again != a {
		t.Fatal("hash is not deterministic")
	}
	if len(a) != 40 {
		t.Fatalf("sha1 hex length = %d want 40", len(a))
	}
}

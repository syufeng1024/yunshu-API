package boot

import (
	"strings"
	"testing"

	"yunshu.dev/core/internal/jsonx"
)

// TestChannelEntryCarriesD14Columns asserts the snapshot payload contains the
// eight dispatch columns from TDS §1.4. A missing field makes the gateway
// refuse the channel (D-14), so this is the producer-side guard.
func TestChannelEntryCarriesD14Columns(t *testing.T) {
	retry := int64(2)
	c := channelEntry{
		ID:                   1,
		BaseURL:              "http://127.0.0.1:18090/v1",
		UpstreamKeyEnc:       "v1:ciphertext",
		TimeoutConnectMS:     3000,
		TimeoutFirstByteMS:   60000,
		TimeoutTotalMS:       120000,
		RetryMax:             &retry,
		StreamUsageMode:      "STREAM_OPTIONS",
		UpstreamModelNameMap: map[string]string{"qwen-plus": "qwen-plus"},
	}
	raw, err := jsonx.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(raw)
	for _, key := range []string{
		`"base_url"`,
		`"upstream_key_enc"`,
		`"timeout_connect_ms"`,
		`"timeout_first_byte_ms"`,
		`"timeout_total_ms"`,
		`"retry_max"`,
		`"stream_usage_mode"`,
		`"upstream_model_name_map"`,
	} {
		if !strings.Contains(body, key) {
			t.Fatalf("payload is missing %s: %s", key, body)
		}
	}
}

func TestStrKey(t *testing.T) {
	if got := strKey(42); got != "42" {
		t.Fatalf("strKey = %q", got)
	}
}

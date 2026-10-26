package config

import (
	"errors"
	"strings"
	"testing"

	"yunshu.dev/core/internal/jsonx"
)

func TestAllDefaultsParse(t *testing.T) {
	for _, key := range AllKeys() {
		meta := metaByKey[key]
		if _, err := Parse(key, jsonx.RawMessage(meta.Default)); err != nil {
			t.Errorf("%s default %s does not parse: %v", key, meta.Default, err)
		}
	}
}

func TestValidationTable(t *testing.T) {
	cases := []struct {
		name    string
		key     Key
		raw     string
		wantErr bool
	}{
		{"int ok", KeyBillingOverdraftLimitDefault, `51`, false},
		{"int as string", KeyBillingOverdraftLimitDefault, `"51"`, true},
		{"int with fraction", KeyBillingOverdraftLimitDefault, `51.0`, true},
		{"int with exponent", KeyBillingOverdraftLimitDefault, `5e1`, true},
		{"int below min", KeyGwMaxInflight, `0`, true},
		{"int above max", KeyGwRetryMax, `6`, true},
		{"bool ok", KeyPricingCacheBillingEnabled, `true`, false},
		{"bool as string", KeyPricingCacheBillingEnabled, `"true"`, true},
		{"enum ok", KeyBillingDeductPolicy, `"fifo_expiry"`, false},
		{"enum outside domain", KeyBillingDeductPolicy, `"nope"`, true},
		{"decimal ok", KeyPricingTaxRateMultiplier, `1.13`, false},
		{"decimal too precise", KeyPricingTaxRateMultiplier, `1.00001`, true},
		{"decimal outside range", KeyBillingPreholdMaxRatio, `1.5`, true},
		{"basis points above 100%", KeyPricingMinMarginBP, `10001`, true},
		{"json ok", KeyBillingSettleDeadlineMs, `{"non_stream_ms":1,"stream_ms":2}`, false},
		{"json missing field", KeyBillingSettleDeadlineMs, `{"non_stream_ms":1}`, true},
		{"json extra field", KeyBillingSettleDeadlineMs, `{"non_stream_ms":1,"stream_ms":2,"x":3}`, true},
		{"json wrong field type", KeyGwRetryThresholdMs, `{"unary":"65000","stream":45000}`, true},
		{"string ok", KeyQuotaResetTZ, `"Asia/Shanghai"`, false},
		{"string outside allow list", KeyQuotaResetTZ, `"UTC"`, true},
		{"string too long", KeyPlanDefaultTier, `"` + strings.Repeat("x", 65) + `"`, true},
		{"unknown key", Key("does.not.exist"), `1`, true},
		{"empty value", KeyGwMaxInflight, ``, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.key, jsonx.RawMessage(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("%s=%s was accepted", tc.key, tc.raw)
				}
				if !errors.Is(err, ErrConfigValueInvalid) {
					t.Fatalf("error %v is not config_value_invalid", err)
				}
				var ve *ValidationError
				if !errors.As(err, &ve) {
					t.Fatalf("error %T is not *ValidationError", err)
				}
				if ve.Code() != CodeConfigValueInvalid || ve.HTTPStatus() != 400 {
					t.Fatalf("code=%s status=%d", ve.Code(), ve.HTTPStatus())
				}
				return
			}
			if err != nil {
				t.Fatalf("%s=%s was rejected: %v", tc.key, tc.raw, err)
			}
		})
	}
}

func TestRuntimeWhitelistValuesAccepted(t *testing.T) {
	for key, raw := range runtimeTestValues {
		if err := ValidateRuntime(key, jsonx.RawMessage(raw)); err != nil {
			t.Errorf("%s=%s rejected: %v", key, raw, err)
		}
	}
}

func TestRuntimeWhitelistRejections(t *testing.T) {
	cases := []struct {
		name string
		key  Key
		raw  string
	}{
		{"thirteenth key retry_max", KeyGwRetryMax, `1`},
		{"cache billing is release-gated", KeyPricingCacheBillingEnabled, `true`},
		{"overdraft warn days is unassigned", KeyQuotaOverdraftWarnDays, `7`},
		{"TA-1.1 min margin", KeyPricingMinMarginBP, `2500`},
		{"TA-1.1 plan min margin", KeyPricingPlanMinMarginBP, `1000`},
		{"TA-1.1 group switch", KeyPricingGroupSwitchMode, `"ops_only"`},
		{"TA-1.1 long ctx guard", KeyBillingLongCtxGuardBP, `9000`},
		{"key daily mode not implemented", KeyQuotaKeyDailyMode, `"inherit"`},
		{"reset scope not implemented", KeyQuotaResetScope, `"key"`},
		{"reset tz fixed", KeyQuotaResetTZ, `"UTC"`},
		{"rate limit order fixed", KeyQuotaRateLimitOrder, `"rpm>tpm>daily>concurrency"`},
		{"deduct policy not registered", KeyBillingDeductPolicy, `"prorata"`},
		{"refund policy not runtime writable", KeyBillingRefundPolicy, `"by_order"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRuntime(tc.key, jsonx.RawMessage(tc.raw))
			if err == nil {
				t.Fatalf("%s=%s was accepted", tc.key, tc.raw)
			}
			if !errors.Is(err, ErrConfigValueInvalid) {
				t.Fatalf("error %v is not config_value_invalid", err)
			}
		})
	}
}

// runtimeTestValues is shared by the unit test above and the integration
// writer test. Every value is a V1.0 implementation value.
var runtimeTestValues = map[Key]string{
	KeyBillingDeductPolicy:    `"fifo_expiry"`,
	KeyBillingPreholdMaxRatio: `0.6`,
	KeyGwUpstreamTimeoutMs:    `51000`,
	KeyGwClientTimeoutBudget:  `121000`,
	KeyGwMaxInflight:          `8001`,
	KeyGwDrainDeadlineMs:      `120001`,
	KeyLogRetentionDays:       `181`,
	KeyQuotaDailyMeter:        `"settle"`,
	KeyQuotaKeyDailyMode:      `"none"`,
	KeyQuotaResetScope:        `"account"`,
	KeyQuotaResetTZ:           `"Asia/Shanghai"`,
	KeyQuotaRateLimitOrder:    `"concurrency>rpm>tpm>daily"`,
}

func TestDecimalParsing(t *testing.T) {
	cases := []struct {
		in      string
		want    Decimal
		wantStr string
		wantErr bool
	}{
		{"1.2", 12000, "1.2", false},
		{"1.00", 10000, "1", false},
		{"0.5", 5000, "0.5", false},
		{"-0.25", -2500, "-0.25", false},
		{"1.00001", 0, "", true},
		{"1e3", 0, "", true},
		{"01", 0, "", true},
		{".5", 0, "", true},
		{"1.", 0, "", true},
	}
	for _, tc := range cases {
		got, err := ParseDecimal(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseDecimal(%q) succeeded, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDecimal(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseDecimal(%q) = %d, want %d", tc.in, got, tc.want)
		}
		if got.String() != tc.wantStr {
			t.Errorf("ParseDecimal(%q).String() = %q, want %q", tc.in, got.String(), tc.wantStr)
		}
	}
}

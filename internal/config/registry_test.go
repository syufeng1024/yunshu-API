package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"yunshu.dev/core/internal/jsonx"
)

func TestBuiltinStrategiesRegistered(t *testing.T) {
	want := map[StrategyKind][]string{
		KindDeductPolicy:    {EnumFifoExpiry},
		KindRefundPolicy:    {EnumByOrder},
		KindCostSource:      {EnumManual},
		KindDailyMeter:      {EnumPrehold, EnumSettle},
		KindKeyDailyQuota:   {EnumNone},
		KindResetScope:      {EnumAccount},
		KindTierChain:       {EnumTierModelChannel},
		KindGroupSwitchMode: {EnumGroupOpsOnly},
	}
	for kind, names := range want {
		got := RegisteredNames(kind)
		if !reflect.DeepEqual(got, names) {
			t.Errorf("%s registered = %v, want %v", kind, got, names)
		}
	}
}

func TestResolveAndResolveAs(t *testing.T) {
	policy, ok := ResolveAs[DeductPolicy](KindDeductPolicy, EnumFifoExpiry)
	if !ok {
		t.Fatal("fifo_expiry did not resolve as DeductPolicy")
	}
	if policy.Name() != EnumFifoExpiry {
		t.Fatalf("resolved policy name = %q", policy.Name())
	}
	if _, ok := Resolve(KindDeductPolicy, EnumProrata); ok {
		t.Fatal("unregistered prorata unexpectedly resolved")
	}
	if _, ok := Resolve(KindDeductPolicy, ""); ok {
		t.Fatal("empty strategy name unexpectedly resolved")
	}
	// A kind that exists but a name from another kind must not resolve.
	if _, ok := Resolve(KindRefundPolicy, EnumFifoExpiry); ok {
		t.Fatal("deduct_policy name resolved under refund_policy")
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Register did not panic")
		}
	}()
	Register(KindDeductPolicy, EnumFifoExpiry, fifoExpiryPolicy{})
}

// TestStartupCheckFailsForUnknownStrategy is acceptance A3: an unknown
// strategy value in the database must fail startup and name the available
// implementations.
func TestStartupCheckFailsForUnknownStrategy(t *testing.T) {
	resetState(t)
	ctx := testCtx(t)
	if _, err := testDB.ExecContext(ctx, `
		UPDATE t_config_item SET cfg_value = ?
		 WHERE cfg_key = ? AND scope = 'GLOBAL' AND scope_id = 0`,
		`"xxx"`, string(KeyBillingDeductPolicy)); err != nil {
		t.Fatalf("seed bad strategy: %v", err)
	}

	err := StartupCheck(ctx, testDB)
	if err == nil {
		t.Fatal("StartupCheck accepted an unregistered strategy")
	}
	var startup *StrategyStartupError
	if !errors.As(err, &startup) {
		t.Fatalf("error %T is not *StrategyStartupError: %v", err, err)
	}
	text := err.Error()
	for _, want := range []string{"xxx", EnumFifoExpiry, string(KeyBillingDeductPolicy), "not registered"} {
		if !strings.Contains(text, want) {
			t.Errorf("startup error %q does not contain %q", text, want)
		}
	}
}

func TestStartupCheckRejectsValidButUnimplementedEnum(t *testing.T) {
	resetState(t)
	ctx := testCtx(t)
	if _, err := testDB.ExecContext(ctx, `
		UPDATE t_config_item SET cfg_value = ?
		 WHERE cfg_key = ? AND scope = 'GLOBAL' AND scope_id = 0`,
		`"prorata"`, string(KeyBillingDeductPolicy)); err != nil {
		t.Fatalf("seed strategy: %v", err)
	}
	err := StartupCheck(ctx, testDB)
	if err == nil || !strings.Contains(err.Error(), EnumFifoExpiry) {
		t.Fatalf("expected a startup error listing fifo_expiry, got %v", err)
	}
}

func TestCheckStrategyRefsListsEveryProblem(t *testing.T) {
	saved := registrySnapshot()
	restoreRegistry(map[StrategyKind]map[string]any{})
	t.Cleanup(func() { restoreRegistry(saved) })

	err := CheckStrategyRefs([]RawItem{
		{Key: KeyBillingDeductPolicy, Raw: jsonx.RawMessage(`"fifo_expiry"`)},
		{Key: KeyBillingRefundPolicy, Raw: jsonx.RawMessage(`"by_order"`)},
		{Key: KeyGwMaxInflight, Raw: jsonx.RawMessage(`9999`)},
	})
	if err == nil {
		t.Fatal("CheckStrategyRefs passed with an empty registry")
	}
	var startup *StrategyStartupError
	if !errors.As(err, &startup) {
		t.Fatalf("error %T is not *StrategyStartupError", err)
	}
	if len(startup.Problems) != 2 {
		t.Fatalf("problems = %d, want 2 (non-strategy keys are ignored)", len(startup.Problems))
	}
	if !strings.Contains(err.Error(), "<none registered>") {
		t.Fatalf("error does not report an empty registry: %v", err)
	}
}

func TestStartupCheckRequiresDatabase(t *testing.T) {
	if err := StartupCheck(testCtx(t), nil); err == nil {
		t.Fatal("StartupCheck(nil) succeeded")
	}
}

package env

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

var allVars = []string{
	"ENV",
	"DB_DSN",
	"LOG_SQL_DSN",
	"GW_WRITE_DSN",
	"REDIS_ADDR",
	"REDIS_PASSWORD",
	"GATEWAY_METRICS_ADDR",
	"GATEWAY_METRICS_TOKEN",
	"GATEWAY_DB_MAX_CONNS",
	"SESSION_SECRET",
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range allVars {
		t.Setenv(k, "")
	}
}

func setGatewayEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ENV", "dev")
	t.Setenv("SESSION_SECRET", "test-session-secret")
	t.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	t.Setenv("GW_WRITE_DSN", "user:pass@tcp(127.0.0.1:3306)/yunshu")
	t.Setenv("LOG_SQL_DSN", "user:pass@tcp(127.0.0.1:3306)/yunshu_log")
}

func TestLoadReportsAllMissingVariablesAtOnce(t *testing.T) {
	clearEnv(t)

	_, err := Load(ServiceGateway)
	if err == nil {
		t.Fatal("want error, got nil")
	}

	var cfgErr *Error
	if !errors.As(err, &cfgErr) {
		t.Fatalf("want *Error, got %T", err)
	}
	want := []string{"ENV", "SESSION_SECRET", "REDIS_ADDR", "GW_WRITE_DSN", "LOG_SQL_DSN"}
	if !reflect.DeepEqual(cfgErr.Missing, want) {
		t.Fatalf("missing = %v, want %v", cfgErr.Missing, want)
	}
	msg := err.Error()
	if !strings.Contains(msg, "missing required environment variables (5)") {
		t.Fatalf("error must report the count once: %s", msg)
	}
	for _, name := range want {
		if !strings.Contains(msg, name) {
			t.Errorf("error does not list %s: %s", name, msg)
		}
	}
}

func TestLoadAppliesGatewayDefaults(t *testing.T) {
	clearEnv(t)
	setGatewayEnv(t)

	cfg, err := Load(ServiceGateway)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GatewayDBMaxConns != 8 {
		t.Errorf("GatewayDBMaxConns = %d, want 8", cfg.GatewayDBMaxConns)
	}
	if cfg.MetricsAddr != ":9090" {
		t.Errorf("MetricsAddr = %q, want :9090", cfg.MetricsAddr)
	}
	if cfg.DBDSN != "" {
		t.Errorf("gateway must not require DB_DSN, got %q", cfg.DBDSN)
	}
}

func TestLoadParsesGatewayMaxConns(t *testing.T) {
	clearEnv(t)
	setGatewayEnv(t)
	t.Setenv("GATEWAY_DB_MAX_CONNS", "12")
	t.Setenv("GATEWAY_METRICS_ADDR", ":19090")

	cfg, err := Load(ServiceGateway)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GatewayDBMaxConns != 12 {
		t.Errorf("GatewayDBMaxConns = %d, want 12", cfg.GatewayDBMaxConns)
	}
	if cfg.MetricsAddr != ":19090" {
		t.Errorf("MetricsAddr = %q", cfg.MetricsAddr)
	}
}

func TestLoadRejectsNonPositiveMaxConns(t *testing.T) {
	clearEnv(t)
	setGatewayEnv(t)
	t.Setenv("GATEWAY_DB_MAX_CONNS", "0")

	_, err := Load(ServiceGateway)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var cfgErr *Error
	if !errors.As(err, &cfgErr) {
		t.Fatalf("want *Error, got %T", err)
	}
	if len(cfgErr.Invalid) != 1 || !strings.Contains(cfgErr.Invalid[0], "GATEWAY_DB_MAX_CONNS") {
		t.Fatalf("invalid = %v", cfgErr.Invalid)
	}
}

func TestControlDoesNotRequireGatewayWriteDSN(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENV", "dev")
	t.Setenv("SESSION_SECRET", "test-session-secret")
	t.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	t.Setenv("DB_DSN", "user:pass@tcp(127.0.0.1:3306)/yunshu")
	t.Setenv("LOG_SQL_DSN", "user:pass@tcp(127.0.0.1:3306)/yunshu_log")

	if _, err := Load(ServiceControl); err != nil {
		t.Fatalf("Load control: %v", err)
	}
}

func TestJobsDoesNotRequireSessionSecret(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENV", "dev")
	t.Setenv("REDIS_ADDR", "127.0.0.1:6379")
	t.Setenv("DB_DSN", "user:pass@tcp(127.0.0.1:3306)/yunshu")
	t.Setenv("LOG_SQL_DSN", "user:pass@tcp(127.0.0.1:3306)/yunshu_log")

	if _, err := Load(ServiceJobs); err != nil {
		t.Fatalf("Load jobs: %v", err)
	}
}

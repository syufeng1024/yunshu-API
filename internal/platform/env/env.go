// Package env loads process configuration from the environment and validates
// required variables before a process starts.
package env

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Process names used by Load.
const (
	ServiceGateway = "gatewayd"
	ServiceControl = "controld"
	ServiceJobs    = "jobsd"
)

// DefaultGatewayDBMaxConns is the size of the gateway's restricted main-database
// channel. The gateway never holds a general-purpose main-database pool.
const DefaultGatewayDBMaxConns = 8

// Config is the union of variables read by the three processes. Each process
// only uses the fields it needs; Load enforces process-specific requirements.
type Config struct {
	Env               string
	DBDSN             string
	LogSQLDSN         string
	GWWriteDSN        string
	RedisAddr         string
	RedisPassword     string
	MetricsAddr       string
	MetricsToken      string
	GatewayDBMaxConns int
	SessionSecret     string
}

// Error reports every missing or invalid variable in one pass. Startup must not
// fail one variable at a time.
type Error struct {
	Service string
	Missing []string
	Invalid []string
}

func (e *Error) Error() string {
	parts := make([]string, 0, 2)
	if len(e.Missing) > 0 {
		parts = append(parts, fmt.Sprintf(
			"missing required environment variables (%d): %s",
			len(e.Missing), strings.Join(e.Missing, ", "),
		))
	}
	if len(e.Invalid) > 0 {
		parts = append(parts, fmt.Sprintf(
			"invalid environment variables (%d): %s",
			len(e.Invalid), strings.Join(e.Invalid, "; "),
		))
	}
	return fmt.Sprintf("config for %s: %s", e.Service, strings.Join(parts, "; "))
}

// RequiredFor returns the required variables for a process, in a stable order.
func RequiredFor(service string) []string {
	switch service {
	case ServiceGateway:
		return []string{"ENV", "SESSION_SECRET", "REDIS_ADDR", "GW_WRITE_DSN", "LOG_SQL_DSN"}
	case ServiceControl:
		return []string{"ENV", "SESSION_SECRET", "REDIS_ADDR", "DB_DSN", "LOG_SQL_DSN"}
	case ServiceJobs:
		return []string{"ENV", "REDIS_ADDR", "DB_DSN", "LOG_SQL_DSN"}
	default:
		return nil
	}
}

// Load reads the environment for service. It collects all missing required
// variables and all malformed optional values, then returns a single error.
func Load(service string) (*Config, error) {
	required := RequiredFor(service)
	if required == nil {
		return nil, &Error{Service: service, Invalid: []string{"service name"}}
	}

	cfg := &Config{
		Env:               strings.TrimSpace(os.Getenv("ENV")),
		DBDSN:             os.Getenv("DB_DSN"),
		LogSQLDSN:         os.Getenv("LOG_SQL_DSN"),
		GWWriteDSN:        os.Getenv("GW_WRITE_DSN"),
		RedisAddr:         strings.TrimSpace(os.Getenv("REDIS_ADDR")),
		RedisPassword:     os.Getenv("REDIS_PASSWORD"),
		MetricsAddr:       strings.TrimSpace(os.Getenv("GATEWAY_METRICS_ADDR")),
		MetricsToken:      os.Getenv("GATEWAY_METRICS_TOKEN"),
		GatewayDBMaxConns: DefaultGatewayDBMaxConns,
		SessionSecret:     os.Getenv("SESSION_SECRET"),
	}

	var missing []string
	for _, name := range required {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, name)
		}
	}

	var invalid []string
	if raw := strings.TrimSpace(os.Getenv("GATEWAY_DB_MAX_CONNS")); raw != "" {
		n, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			invalid = append(invalid, "GATEWAY_DB_MAX_CONNS (not an integer)")
		case n < 1:
			invalid = append(invalid, "GATEWAY_DB_MAX_CONNS (must be >= 1)")
		default:
			cfg.GatewayDBMaxConns = n
		}
	}

	if cfg.MetricsAddr == "" {
		cfg.MetricsAddr = ":9090"
	}

	if len(missing) > 0 || len(invalid) > 0 {
		return nil, &Error{Service: service, Missing: missing, Invalid: invalid}
	}
	return cfg, nil
}

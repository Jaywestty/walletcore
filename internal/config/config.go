// Package config loads and validates typed application settings from the
// environment.
//
// Every setting is read once at startup and validated immediately, so the
// process fails fast with a clear message instead of failing later on a
// missing value. There is no global state: callers receive a *Config.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully validated application configuration.
type Config struct {
	// HTTPAddr is the listen address for the API server, for example ":8080".
	HTTPAddr string

	// HTTPReadTimeout, HTTPWriteTimeout and HTTPIdleTimeout bound the server.
	HTTPReadTimeout  time.Duration
	HTTPWriteTimeout time.Duration
	HTTPIdleTimeout  time.Duration

	// DatabaseURL is the pgx connection string for the primary database.
	DatabaseURL string

	// Database pool tuning.
	DBMaxConns          int32
	DBMinConns          int32
	DBMaxConnLifetime   time.Duration
	DBMaxConnIdleTime   time.Duration
	DBHealthCheckPeriod time.Duration

	// ShutdownTimeout bounds graceful shutdown so in-flight requests finish.
	ShutdownTimeout time.Duration

	// TrustProxyHeaders enables deriving the client IP from proxy headers such
	// as X-Forwarded-For. It defaults to false and should only be enabled when
	// the service sits behind a trusted reverse proxy.
	TrustProxyHeaders bool

	// LogLevel is one of debug, info, warn, error.
	LogLevel string

	// LedgerLockTimeout is how long a posting waits for a lock before failing
	// with ErrLockTimeout.
	LedgerLockTimeout time.Duration

	// LedgerMaxRetries is the maximum number of automatic retries for a
	// posting that fails with a deadlock or serialization error.
	LedgerMaxRetries int
}

// Load reads configuration from the process environment, applies defaults and
// validates the result. It returns a descriptive error on the first invalid
// value so startup failures are actionable.
func Load() (*Config, error) {
	return load(os.Getenv)
}

// loader is the environment accessor, injected so tests do not touch the
// real environment.
type loader func(string) string

func load(getenv loader) (*Config, error) {
	cfg := &Config{
		HTTPAddr:            valueOr(getenv, "HTTP_ADDR", ":8080"),
		DatabaseURL:         strings.TrimSpace(getenv("DATABASE_URL")),
		LogLevel:            strings.ToLower(valueOr(getenv, "LOG_LEVEL", "info")),
		DBMaxConns:          10,
		DBMinConns:          1,
		DBMaxConnLifetime:   30 * time.Minute,
		DBMaxConnIdleTime:   5 * time.Minute,
		DBHealthCheckPeriod: time.Minute,
		HTTPReadTimeout:     5 * time.Second,
		HTTPWriteTimeout:    10 * time.Second,
		HTTPIdleTimeout:     60 * time.Second,
		ShutdownTimeout:     10 * time.Second,
	}

	var err error
	if cfg.TrustProxyHeaders, err = boolEnv(getenv, "TRUST_PROXY_HEADERS", false); err != nil {
		return nil, err
	}
	if cfg.LedgerLockTimeout, err = durationEnv(getenv, "LEDGER_LOCK_TIMEOUT", 5*time.Second); err != nil {
		return nil, err
	}
	if cfg.LedgerMaxRetries, err = intEnv(getenv, "LEDGER_MAX_RETRIES", 3, 0); err != nil {
		return nil, err
	}
	if cfg.DBMaxConns, err = int32Env(getenv, "DB_MAX_CONNS", cfg.DBMaxConns, 1); err != nil {
		return nil, err
	}
	if cfg.DBMinConns, err = int32Env(getenv, "DB_MIN_CONNS", cfg.DBMinConns, 0); err != nil {
		return nil, err
	}
	if cfg.DBMaxConnLifetime, err = durationEnv(getenv, "DB_MAX_CONN_LIFETIME", cfg.DBMaxConnLifetime); err != nil {
		return nil, err
	}
	if cfg.DBMaxConnIdleTime, err = durationEnv(getenv, "DB_MAX_CONN_IDLE_TIME", cfg.DBMaxConnIdleTime); err != nil {
		return nil, err
	}
	if cfg.DBHealthCheckPeriod, err = durationEnv(getenv, "DB_HEALTH_CHECK_PERIOD", cfg.DBHealthCheckPeriod); err != nil {
		return nil, err
	}
	if cfg.HTTPReadTimeout, err = durationEnv(getenv, "HTTP_READ_TIMEOUT", cfg.HTTPReadTimeout); err != nil {
		return nil, err
	}
	if cfg.HTTPWriteTimeout, err = durationEnv(getenv, "HTTP_WRITE_TIMEOUT", cfg.HTTPWriteTimeout); err != nil {
		return nil, err
	}
	if cfg.HTTPIdleTimeout, err = durationEnv(getenv, "HTTP_IDLE_TIMEOUT", cfg.HTTPIdleTimeout); err != nil {
		return nil, err
	}
	if cfg.ShutdownTimeout, err = durationEnv(getenv, "SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout); err != nil {
		return nil, err
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validate applies cross-field rules that individual readers cannot express.
func (c *Config) validate() error {
	var problems []error

	if c.DatabaseURL == "" {
		problems = append(problems, errors.New("DATABASE_URL is required"))
	}
	if c.HTTPAddr == "" {
		problems = append(problems, errors.New("HTTP_ADDR must not be empty"))
	}
	if c.DBMinConns > c.DBMaxConns {
		problems = append(problems, fmt.Errorf("DB_MIN_CONNS (%d) must not exceed DB_MAX_CONNS (%d)", c.DBMinConns, c.DBMaxConns))
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error, got %q", c.LogLevel))
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration: %w", errors.Join(problems...))
	}
	return nil
}

func valueOr(getenv loader, name, fallback string) string {
	if v := strings.TrimSpace(getenv(name)); v != "" {
		return v
	}
	return fallback
}

func int32Env(getenv loader, name string, fallback int32, min int32) (int32, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", name, raw)
	}
	if int32(n) < min {
		return 0, fmt.Errorf("%s must be at least %d, got %d", name, min, n)
	}
	return int32(n), nil
}

func intEnv(getenv loader, name string, fallback, min int) (int, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", name, raw)
	}
	if n < min {
		return 0, fmt.Errorf("%s must be at least %d, got %d", name, min, n)
	}
	return n, nil
}

func boolEnv(getenv loader, name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be a boolean such as true or false, got %q", name, raw)
	}
}

func durationEnv(getenv loader, name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration such as 30s or 5m, got %q", name, raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %s", name, d)
	}
	return d, nil
}

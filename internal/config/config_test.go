package config

import (
	"strings"
	"testing"
	"time"
)

// envMap turns a map into a loader so tests never touch the real environment.
func envMap(m map[string]string) loader {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(envMap(map[string]string{
		"DATABASE_URL": "postgres://user:pass@localhost:5432/db",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.DBMaxConns != 10 || cfg.DBMinConns != 1 {
		t.Errorf("pool defaults = %d/%d, want 10/1", cfg.DBMaxConns, cfg.DBMinConns)
	}
	if cfg.DBMaxConnLifetime != 30*time.Minute {
		t.Errorf("DBMaxConnLifetime = %s, want 30m", cfg.DBMaxConnLifetime)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", cfg.LogLevel)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := load(envMap(map[string]string{
		"DATABASE_URL":         "postgres://x",
		"HTTP_ADDR":            "127.0.0.1:9000",
		"DB_MAX_CONNS":         "25",
		"DB_MIN_CONNS":         "5",
		"DB_MAX_CONN_LIFETIME": "1h",
		"LOG_LEVEL":            "DEBUG",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9000" {
		t.Errorf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.DBMaxConns != 25 || cfg.DBMinConns != 5 {
		t.Errorf("pool = %d/%d", cfg.DBMaxConns, cfg.DBMinConns)
	}
	if cfg.DBMaxConnLifetime != time.Hour {
		t.Errorf("DBMaxConnLifetime = %s", cfg.DBMaxConnLifetime)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}

func TestTrustProxyHeaders(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"default off", "", false},
		{"enabled", "true", true},
		{"enabled numeric", "1", true},
		{"disabled", "false", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"DATABASE_URL": "postgres://x"}
			if tc.raw != "" {
				env["TRUST_PROXY_HEADERS"] = tc.raw
			}
			cfg, err := load(envMap(env))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.TrustProxyHeaders != tc.want {
				t.Fatalf("TrustProxyHeaders = %v, want %v", cfg.TrustProxyHeaders, tc.want)
			}
		})
	}
	if _, err := load(envMap(map[string]string{"DATABASE_URL": "postgres://x", "TRUST_PROXY_HEADERS": "maybe"})); err == nil {
		t.Fatal("expected error for invalid boolean")
	}
}

func TestLoadValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantSub string
	}{
		{
			name:    "missing database url",
			env:     map[string]string{},
			wantSub: "DATABASE_URL is required",
		},
		{
			name:    "min exceeds max",
			env:     map[string]string{"DATABASE_URL": "postgres://x", "DB_MIN_CONNS": "20", "DB_MAX_CONNS": "10"},
			wantSub: "DB_MIN_CONNS",
		},
		{
			name:    "unknown log level",
			env:     map[string]string{"DATABASE_URL": "postgres://x", "LOG_LEVEL": "verbose"},
			wantSub: "LOG_LEVEL",
		},
		{
			name:    "bad integer",
			env:     map[string]string{"DATABASE_URL": "postgres://x", "DB_MAX_CONNS": "ten"},
			wantSub: "DB_MAX_CONNS must be an integer",
		},
		{
			name:    "bad duration",
			env:     map[string]string{"DATABASE_URL": "postgres://x", "DB_MAX_CONN_LIFETIME": "soon"},
			wantSub: "DB_MAX_CONN_LIFETIME must be a duration",
		},
		{
			name:    "negative duration",
			env:     map[string]string{"DATABASE_URL": "postgres://x", "SHUTDOWN_TIMEOUT": "-5s"},
			wantSub: "SHUTDOWN_TIMEOUT must be positive",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(envMap(tc.env))
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

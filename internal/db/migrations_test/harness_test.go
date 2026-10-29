//go:build integration

// Package migrations_test applies the SQL migrations to a throwaway PostgreSQL
// started by testcontainers-go and proves the schema enforces the ledger rules.
//
// These tests need Docker. They are skipped unless both conditions hold:
//   - the build tag "integration" is set, and
//   - the env var INTEGRATION is "1".
//
// Plain "go test ./..." therefore stays green without Docker.
package migrations_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	dbName = "walletcore_test"
	dbUser = "walletcore"
	dbPass = "walletcore"
)

// migrationsDir returns an absolute path to the migrations folder regardless
// of the working directory the tests run from.
func migrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	// This file lives at <root>/internal/.../migrations_test; walk up to root.
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	return filepath.Join(root, "migrations")
}

// startPostgres starts a disposable PostgreSQL and returns a connected handle,
// the DSN for golang-migrate, and a cleanup that terminates the container.
func startPostgres(t *testing.T) (*sql.DB, string, func()) {
	t.Helper()

	if os.Getenv("INTEGRATION") != "1" {
		t.Skip("integration tests skipped: set INTEGRATION=1 and ensure Docker is available")
	}
	if testing.Short() {
		t.Skip("integration tests skipped in -short mode")
	}

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase(dbName),
		tcpostgres.WithUsername(dbUser),
		tcpostgres.WithPassword(dbPass),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres container (is Docker running?): %v", err)
	}

	cleanup := func() {
		_ = container.Terminate(ctx)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		cleanup()
		t.Fatalf("connection string: %v", err)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		cleanup()
		t.Fatalf("open db: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		if err = db.PingContext(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = db.Close()
			cleanup()
			t.Fatalf("database did not become ready: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	return db, dsn, func() {
		_ = db.Close()
		cleanup()
	}
}

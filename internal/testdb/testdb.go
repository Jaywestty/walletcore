//go:build integration

// Package testdb provides a shared integration-test database harness.
//
// One PostgreSQL container is started for the whole test binary. The migration
// suite is applied once to a template database, and each test receives a fresh
// database cloned from that template with CREATE DATABASE ... TEMPLATE. Cloning
// is cheap (a file copy inside the container) and gives each test a pristine
// schema without paying for a new container.
//
// The ledger's immutability triggers block TRUNCATE, so truncation is not a
// usable reset strategy. A template clone is both faster and safer because it
// never touches a database another test can see.
//
// Usage from a test package:
//
//	func TestMain(m *testing.M) { testdb.Main(m) }
//
//	func TestX(t *testing.T) {
//	    h := testdb.New(t) // fresh cloned database, dropped on cleanup
//	    db := h.DB
//	    ...
//	}
package testdb

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	adminDB  = "postgres"
	template = "walletcore_template"
	user     = "walletcore"
	password = "walletcore"
	baseDB   = "walletcore_base"
)

var (
	enabled      bool
	adminDSN     string
	migrationsOK string
	cloneCounter atomic.Int64
)

// Handle is a freshly cloned database for one test.
type Handle struct {
	// DB is the connection to the clone.
	DB *sql.DB
	// DSN is the postgres:// URL for the clone.
	DSN string
}

// Main starts the shared container, migrates a template database, runs the
// tests and tears everything down. Call it from a package TestMain.
//
// Integration tests are skipped unless INTEGRATION=1 and -short is not set.
func Main(m *testing.M) {
	// Parse flags before consulting testing.Short() or any test flag.
	flag.Parse()

	if os.Getenv("INTEGRATION") != "1" || testing.Short() {
		os.Exit(m.Run())
	}

	// Ryuk (the reaper sidecar) adds a few seconds of startup and is only
	// needed to clean up after a crashed test process. The harness terminates
	// the container itself in a deferred call, so it can be skipped locally.
	if os.Getenv("TESTCONTAINERS_RYUK_DISABLED") == "" {
		_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}

	ctx := context.Background()
	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase(baseDB),
		tcpostgres.WithUsername(user),
		tcpostgres.WithPassword(password),
		testcontainers.WithWaitStrategy(
			// The alpine entrypoint logs readiness twice (init then temp
			// server). Waiting for the second occurrence plus the listening
			// port is the reliable signal; occurrence 1 can fire too early.
			wait.ForAll(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(60*time.Second),
				wait.ForListeningPort("5432/tcp"),
			),
		),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "testdb: start postgres (is Docker running?): %v\n", err)
		os.Exit(1)
	}

	baseDSN, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		fmt.Fprintf(os.Stderr, "testdb: connection string: %v\n", err)
		os.Exit(1)
	}
	adminDSN = withDatabase(baseDSN, adminDB)
	migrationsOK = migrationsDir()

	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		_ = container.Terminate(ctx)
		fmt.Fprintf(os.Stderr, "testdb: open admin db: %v\n", err)
		os.Exit(1)
	}
	if err := waitReady(ctx, admin, 30*time.Second); err != nil {
		_ = admin.Close()
		_ = container.Terminate(ctx)
		fmt.Fprintf(os.Stderr, "testdb: database not ready: %v\n", err)
		os.Exit(1)
	}

	if err := createTemplate(admin, baseDSN); err != nil {
		_ = admin.Close()
		_ = container.Terminate(ctx)
		fmt.Fprintf(os.Stderr, "testdb: create template: %v\n", err)
		os.Exit(1)
	}

	enabled = true
	code := m.Run()

	_ = admin.Close()
	_ = container.Terminate(ctx)
	os.Exit(code)
}

// New returns a handle to a fresh database cloned from the template. It skips
// the test when integration mode is off. The clone is dropped on cleanup.
func New(t *testing.T) Handle {
	t.Helper()
	if !enabled {
		t.Skip("integration tests skipped: set INTEGRATION=1 and ensure Docker is available")
	}

	ctx := context.Background()
	name := fmt.Sprintf("wc_test_%d_%d", os.Getpid(), cloneCounter.Add(1))

	admin, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	defer func() { _ = admin.Close() }()

	// Closing the clone appears to block the template drop; terminate any
	// sessions first, then clone.
	_, _ = admin.ExecContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name)
	if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(name)); err != nil {
		t.Fatalf("drop stale clone: %v", err)
	}
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+quoteIdent(name)+` TEMPLATE `+quoteIdent(template)); err != nil {
		t.Fatalf("clone template database: %v", err)
	}

	dsn := withDatabase(adminDSN, name)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open clone: %v", err)
	}
	db.SetMaxOpenConns(4)

	t.Cleanup(func() {
		_ = db.Close()
		drop, err := sql.Open("pgx", adminDSN)
		if err != nil {
			return
		}
		defer func() { _ = drop.Close() }()
		_, _ = drop.ExecContext(context.Background(),
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, name)
		_, _ = drop.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+quoteIdent(name))
	})

	return Handle{DB: db, DSN: dsn}
}

// MigrateDown rolls every migration back on h's database. Used by the up/down
// test, which needs its own clone to destroy and rebuild.
func MigrateDown(t *testing.T, h Handle) {
	t.Helper()
	m, err := migrateNew(migrationsOK, migrateDSN(h.DSN))
	if err != nil {
		t.Fatalf("create migrator: %v", err)
	}
	if err := m.Down(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate down: %v", err)
	}
	_, _ = m.Close()
}

// MigrateUp applies every outstanding migration on h's database.
func MigrateUp(t *testing.T, h Handle) {
	t.Helper()
	m, err := migrateNew(migrationsOK, migrateDSN(h.DSN))
	if err != nil {
		t.Fatalf("create migrator: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate up: %v", err)
	}
	_, _ = m.Close()
}

func createTemplate(admin *sql.DB, baseDSN string) error {
	ctx := context.Background()
	_, _ = admin.ExecContext(ctx, `DROP DATABASE IF EXISTS `+quoteIdent(template))
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+quoteIdent(template)); err != nil {
		return err
	}

	tmplDSN := withDatabase(baseDSN, template)
	m, err := migrateNew(migrationsOK, migrateDSN(tmplDSN))
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	// Release the migrator's connection so the template can be cloned.
	_, _ = m.Close()
	return nil
}

func migrateNew(dir, databaseURL string) (*migrate.Migrate, error) {
	src, err := iofs.New(os.DirFS(dir), ".")
	if err != nil {
		return nil, err
	}
	return migrate.NewWithSourceInstance("iofs", src, databaseURL)
}

func migrateDSN(u string) string {
	switch {
	case strings.HasPrefix(u, "postgres://"):
		return "pgx5://" + strings.TrimPrefix(u, "postgres://")
	case strings.HasPrefix(u, "postgresql://"):
		return "pgx5://" + strings.TrimPrefix(u, "postgresql://")
	default:
		return u
	}
}

func waitReady(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var err error
	for {
		if err = db.PingContext(ctx); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// withDatabase swaps the database name in a postgres URL.
func withDatabase(dsn, name string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn
	}
	u.Path = "/" + name
	return u.String()
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// migrationsDir locates the repository migrations folder from this file.
func migrationsDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("testdb: runtime.Caller failed")
	}
	// This file lives at <root>/internal/testdb/testdb.go.
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(root, "migrations")
}

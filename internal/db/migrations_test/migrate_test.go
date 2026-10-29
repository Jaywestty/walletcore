//go:build integration

package migrations_test

import (
	"os"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/stretchr/testify/require"
)

// newMigrator builds a golang-migrate instance that reads the real migrations
// directory through os.DirFS. Using an fs.FS source instead of a file:// URL
// avoids URL escaping problems on Windows paths that contain spaces.
func newMigrator(t *testing.T, dbURL string) *migrate.Migrate {
	t.Helper()
	dir := migrationsDir(t)
	require.DirExists(t, dir, "migrations directory")

	src, err := iofs.New(os.DirFS(dir), ".")
	require.NoError(t, err, "iofs source")
	m, err := migrate.NewWithSourceInstance("iofs", src, migrateDSN(dbURL))
	require.NoError(t, err, "create migrator")
	return m
}

// migrateDSN rewrites a postgres:// URL to the pgx5:// scheme that the
// golang-migrate pgx/v5 driver registers. Everything after the scheme is
// preserved.
func migrateDSN(url string) string {
	if strings.HasPrefix(url, "postgres://") {
		return "pgx5://" + strings.TrimPrefix(url, "postgres://")
	}
	if strings.HasPrefix(url, "postgresql://") {
		return "pgx5://" + strings.TrimPrefix(url, "postgresql://")
	}
	return url
}

// migrateUp applies every outstanding migration.
func migrateUp(t *testing.T, dbURL string) {
	t.Helper()
	m := newMigrator(t, dbURL)
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate up: %v", err)
	}
}

// migrateDownAll rolls every migration back.
func migrateDownAll(t *testing.T, dbURL string) {
	t.Helper()
	m := newMigrator(t, dbURL)
	if err := m.Down(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate down: %v", err)
	}
}

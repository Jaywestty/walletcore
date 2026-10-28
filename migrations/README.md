# Migrations

Schema changes land here as versioned SQL migrations run by golang-migrate.
They are never applied implicitly on startup. Use the `migrate-up` and
`migrate-down` tasks.

Naming: `<version>_<name>.up.sql` and `<version>_<name>.down.sql`, for example
`000001_ledger_core.up.sql`. Phase 2 adds the first pair.

Migrations run against PostgreSQL 16 or newer in Docker.

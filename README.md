# WalletCore

WalletCore is a small, self-contained wallet and double-entry ledger written in
Go, ported from a production Python and PostgreSQL system into a generic,
publishable reference.

This is a Phase 0 stub. Sections that need working code are marked
`(coming in Phase N)`; nothing here is presented as runnable until the phase
that lands it.

## Why this exists

I built and operate a production wallet and double-entry ledger for a delivery
platform. Running real money through it taught me how much of "a wallet" is
actually bookkeeping: a balance is a claim, not a number you edit, and the only
honest way to change it is to append a balanced, reversible record of why.
This repository is a clean-room reimplementation of the core ideas in Go. It
exists to share the patterns publicly and to deepen my own Go backend
engineering. No private code, data, or business rules are included.

## The problem it solves

A naive wallet stores `balance` as a column and updates it in place. That model
fails in predictable ways:

- Lost updates: two concurrent spends read the same balance and both write, so
  one debit disappears.
- Double spends: without a lock or a check, a user can spend the same money
  twice across two requests.
- Unauditable history: an in-place update leaves no record of who changed the
  balance or why.

WalletCore fixes this by construction:

- Double-entry: every movement is a journal whose debits equal its credits, so
  nothing appears or disappears.
- Immutability: entries and journals cannot be updated or deleted, enforced by
  database triggers.
- Idempotency: every request carries a key; a retry returns the original result
  instead of posting twice.
- Locking in a deterministic order: concurrent postings serialize per account
  and cannot deadlock.

## Key features

- Double-entry posting with debits equal to credits per currency.
- Money as integer minor units (int64) only. No floats.
- Immutable journals and entries, enforced at the database level.
- Idempotent requests with same-key replay and different-payload conflict.
- Concurrency-safe balance updates using explicit row locks.
- Reversals post compensating journals; history is never edited.
- Reconciliation verifies cached balances against entries.

## Who this is useful for

Developers building wallets, fintech backends, marketplaces, and credits or
points systems, and anyone learning how real ledgers work in Go. Useful pieces
to borrow:

- The schema: accounts, journals, entries, and a balance projection.
- The locking pattern: row locks taken in a fixed account order.
- The idempotency approach: a unique key plus a payload fingerprint.
- The race tests: many goroutines proving no overdraft, no lost updates, no
  deadlocks, and balanced books.

## Architecture

(coming in Phase 7: a Mermaid diagram and a description of the layers)

Planned layers: `cmd/api` (entry point), `internal/httpapi` (router and
handlers), `internal/wallet` (operations), `internal/ledger` (posting engine),
`internal/db` (pgxpool and repositories), `internal/config`.

## Quick start

(coming in Phase 1 and Phase 7: exact Windows cmd and Linux/macOS commands
from clone to a running server with Docker Compose)

## API overview

(coming in Phase 6: curl examples for fund, transfer, withdraw and reverse)

## How correctness is proven

(coming in Phase 4 and Phase 7: race tests and the reconciliation command)

## Database notes

WalletCore targets PostgreSQL 16 or newer and is run locally through Docker
Compose. It does not depend on Supabase or any hosted vendor.

### Supabase pooler gotcha

If you point WalletCore at a Supabase database, do not use Supabase's
transaction-mode pooler (PgBouncer). Transaction-mode pooling does not preserve
session state, which conflicts with pgx's prepared statement caching and can
produce intermittent errors. Use the direct connection string or the
session-mode pooler instead.

## Design decisions and trade-offs

(coming in Phase 7: see `docs/DESIGN.md`)

## Project status and roadmap

Phase 0: analysis and skeleton.

Roadmap: bootstrap (Phase 1), schema and migrations (Phase 2), ledger core
(Phase 3), idempotency and concurrency (Phase 4), wallet operations (Phase 5),
HTTP API (Phase 6), reconciliation, CI and documentation (Phase 7).

## License and contributing

MIT. See `LICENSE` (coming in Phase 7). Contributions are welcome; run
`task lint`, `task test`, and `task test-integration` before opening a pull
request.

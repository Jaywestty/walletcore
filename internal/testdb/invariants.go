//go:build integration

package testdb

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// AssertInvariants checks the ledger-level invariants directly in the
// database. It is shared by the ledger and wallet integration tests.
func AssertInvariants(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	// No negative balance on kinds that forbid it.
	var negative int
	err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM ledger_balances b
		JOIN ledger_accounts a ON a.id = b.account_id
		JOIN ledger_account_kinds k ON k.kind = a.kind
		WHERE b.balance < 0 AND NOT k.allow_negative`).Scan(&negative)
	require.NoError(t, err)
	require.Zero(t, negative, "found negative balance on a kind that forbids it")

	// Cached balance equals sign-normal sum of entries per account.
	var drift int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT a.id,
			       b.balance AS cached,
			       COALESCE(SUM(CASE
			         WHEN k.normal_side = 'credit' AND e.side = 'Cr' THEN e.amount
			         WHEN k.normal_side = 'credit' AND e.side = 'Dr' THEN -e.amount
			         WHEN k.normal_side = 'debit'  AND e.side = 'Dr' THEN e.amount
			         ELSE -e.amount END), 0) AS derived
			FROM ledger_accounts a
			JOIN ledger_account_kinds k ON k.kind = a.kind
			JOIN ledger_balances b ON b.account_id = a.id
			LEFT JOIN ledger_entries e ON e.account_id = a.id
			GROUP BY a.id, b.balance
		) s
		WHERE cached <> derived`).Scan(&drift)
	require.NoError(t, err)
	require.Zero(t, drift, "cached balance drifted from entries")

	// Total debits equal total credits per currency.
	var unbalanced int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT currency,
			       SUM(CASE WHEN side = 'Dr' THEN amount ELSE 0 END) AS dr,
			       SUM(CASE WHEN side = 'Cr' THEN amount ELSE 0 END) AS cr
			FROM ledger_entries GROUP BY currency
		) c WHERE dr <> cr`).Scan(&unbalanced)
	require.NoError(t, err)
	require.Zero(t, unbalanced, "total debits and credits differ per currency")

	// Every journal has at least two entries.
	var short int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM ledger_journals j
		WHERE (SELECT count(*) FROM ledger_entries e WHERE e.journal_id = j.id) < 2`).Scan(&short)
	require.NoError(t, err)
	require.Zero(t, short, "found a journal with fewer than two entries")

	// Accounting identity per currency. Every journal balances, so the
	// settlement account (the only debit-normal kind) holds the negative of the
	// sum of the credit-normal kinds. Stated positively: for each currency the
	// settlement balance equals the sum of user_wallet, holds and fees, because
	// each is the net of the same journal volume, just on opposite sides.
	var identityBreak int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT currency,
			       SUM(CASE WHEN kind = 'settlement' THEN balance ELSE 0 END) AS settlement,
			       SUM(CASE WHEN kind IN ('user_wallet', 'holds', 'fees') THEN balance ELSE 0 END) AS others
			FROM (
				SELECT a.kind, a.currency, b.balance
				FROM ledger_accounts a
				JOIN ledger_balances b ON b.account_id = a.id
			) per_account
			GROUP BY currency
		) c
		WHERE settlement <> others`).Scan(&identityBreak)
	require.NoError(t, err)
	require.Zero(t, identityBreak, "settlement balance must equal the sum of user_wallet, holds and fees per currency")
}

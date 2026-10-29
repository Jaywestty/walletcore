//go:build integration

package migrations_test

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/Jaywestty/walletcore/internal/testdb"
)

// TestMain starts one Postgres container and migrates a template once; every
// test gets a cheap clone.
func TestMain(m *testing.M) {
	testdb.Main(m)
}

// SQLSTATE codes raised by the ledger triggers. They are asserted by code, not
// by message text, so wording changes never break a test.
const (
	sqlstateUnbalanced = "LD001"
	sqlstateCurrency   = "LD002"
	sqlstateNegative   = "LD003"
	sqlstateImmutable  = "LD004"
	sqlstateOutsideTxn = "LD005"

	sqlstateUniqueViolation     = "23505"
	sqlstateForeignKeyViolation = "23503"
	sqlstateCheckViolation      = "23514"
)

// Constraint names the Go error mapper relies on. They must match the DDL.
const (
	conIdempotencyKeyUniq = "ledger_journals_idempotency_key_uniq"
	conAmountPositive     = "ledger_entries_amount_positive_check"
	conReversalPairing    = "ledger_journals_reversal_pairing_check"
	conJournalKind        = "ledger_journals_kind_check"
	conAccountUniq        = "ledger_accounts_kind_owner_currency_uniq"
	conReversesUniq       = "ledger_journals_reverses_uniq"
)

// pgErr extracts the SQLSTATE and constraint name from a PostgreSQL error.
func pgErr(t *testing.T, err error) *pgconn.PgError {
	t.Helper()
	require.Error(t, err)
	var pg *pgconn.PgError
	require.True(t, errors.As(err, &pg), "expected *pgconn.PgError, got %T: %v", err, err)
	return pg
}

// pgErrCode extracts only the SQLSTATE.
func pgErrCode(t *testing.T, err error) string {
	t.Helper()
	return pgErr(t, err).Code
}

// newDB returns a fresh cloned database with the schema applied.
func newDB(t *testing.T) (*sql.DB, testdb.Handle) {
	t.Helper()
	h := testdb.New(t)
	return h.DB, h
}

// createAccount inserts an account and returns its id.
func createAccount(t *testing.T, db *sql.DB, kind, owner, currency string) string {
	t.Helper()
	var id string
	err := db.QueryRow(
		`INSERT INTO ledger_accounts (kind, owner_ref, currency) VALUES ($1, $2, $3) RETURNING id`,
		kind, owner, currency,
	).Scan(&id)
	require.NoError(t, err, "create account")
	return id
}

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// leg is one entry to post.
type leg struct {
	account  string
	side     string
	amount   int64
	currency string
}

// insertEntry inserts one entry through e (a db or a tx).
func insertEntry(e execer, journalID, accountID, side string, amount int64, currency string) error {
	_, err := e.Exec(
		`INSERT INTO ledger_entries (journal_id, account_id, side, amount, currency)
		 VALUES ($1, $2, $3, $4, $5)`,
		journalID, accountID, side, amount, currency,
	)
	return err
}

// postBalanced inserts a journal and its legs in one transaction so the
// deferred journal check sees the complete journal at commit. It returns the
// journal id and the commit error, letting callers assert a rejected commit.
func postBalanced(t *testing.T, db *sql.DB, kind, key string, legs ...leg) (string, error) {
	t.Helper()
	return postBalancedReversing(t, db, kind, key, nil, legs...)
}

// postBalancedReversing is postBalanced with an optional original journal to
// reverse. The reversal link is set at insert time because journals are
// immutable after insert.
func postBalancedReversing(t *testing.T, db *sql.DB, kind, key string, reverses *string, legs ...leg) (string, error) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var journal string
	// A reversal journal must carry reverses_journal_id; the CHECK enforces the
	// pairing, so the column is always written from the argument.
	err = tx.QueryRow(
		`INSERT INTO ledger_journals (kind, idempotency_key, fingerprint, actor, reason, occurred_at, reverses_journal_id)
		 VALUES ($1, $2, $3, 'test', 'test', now(), $4) RETURNING id`,
		kind, key, "fp-"+key, reverses,
	).Scan(&journal)
	if err != nil {
		return "", err
	}

	for _, l := range legs {
		if err := insertEntry(tx, journal, l.account, l.side, l.amount, l.currency); err != nil {
			return journal, err
		}
	}
	return journal, tx.Commit()
}

func TestImmutability(t *testing.T) {
	db, _ := newDB(t)

	user := createAccount(t, db, "user_wallet", "u1", "USD")
	settle := createAccount(t, db, "settlement", "core", "USD")
	journal, err := postBalanced(t, db, "fund", "fund-1",
		leg{settle, "Dr", 100, "USD"},
		leg{user, "Cr", 100, "USD"},
	)
	require.NoError(t, err, "post initial fund journal")

	cases := []struct {
		name string
		sql  string
		args []any
	}{
		{"update journal", `UPDATE ledger_journals SET reason = 'changed' WHERE id = $1`, []any{journal}},
		{"delete journal", `DELETE FROM ledger_journals WHERE id = $1`, []any{journal}},
		{"update entry", `UPDATE ledger_entries SET amount = 1 WHERE journal_id = $1`, []any{journal}},
		{"delete entry", `DELETE FROM ledger_entries WHERE journal_id = $1`, []any{journal}},
		{"delete account", `DELETE FROM ledger_accounts WHERE id = $1`, []any{user}},
		{"change account kind", `UPDATE ledger_accounts SET kind = 'fees' WHERE id = $1`, []any{user}},
		{"change account owner", `UPDATE ledger_accounts SET owner_ref = 'other' WHERE id = $1`, []any{user}},
		{"change account currency", `UPDATE ledger_accounts SET currency = 'EUR' WHERE id = $1`, []any{user}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Exec(tc.sql, tc.args...)
			require.Equal(t, sqlstateImmutable, pgErrCode(t, err))
		})
	}

	// TRUNCATE on journals and entries is rejected. Both tables are the parent
	// or child of a foreign key, so PostgreSQL rejects TRUNCATE before the
	// BEFORE TRUNCATE trigger runs, with feature_not_supported (0A000). The
	// trigger still guards the tables if the foreign keys ever change, and the
	// end result is the same: the history cannot be emptied.
	for _, table := range []string{"ledger_journals", "ledger_entries"} {
		t.Run("truncate "+table+" is rejected", func(t *testing.T) {
			_, err := db.Exec(`TRUNCATE ` + table)
			require.Regexp(t, `^(LD004|0A000)$`, pgErrCode(t, err), "truncate must be rejected")
		})
	}
}

func TestBalanceGuards(t *testing.T) {
	db, _ := newDB(t)
	user := createAccount(t, db, "user_wallet", "u1", "USD")
	_, err := db.Exec(`INSERT INTO ledger_balances (account_id, balance) VALUES ($1, 10)`, user)
	require.NoError(t, err)

	t.Run("update balance is allowed", func(t *testing.T) {
		_, err := db.Exec(`UPDATE ledger_balances SET balance = 20 WHERE account_id = $1`, user)
		require.NoError(t, err)
	})
	t.Run("delete balance is rejected", func(t *testing.T) {
		_, err := db.Exec(`DELETE FROM ledger_balances WHERE account_id = $1`, user)
		require.Equal(t, sqlstateImmutable, pgErrCode(t, err))
	})
	t.Run("truncate balances is rejected", func(t *testing.T) {
		_, err := db.Exec(`TRUNCATE ledger_balances`)
		require.Equal(t, sqlstateImmutable, pgErrCode(t, err))
	})
}

func TestAmountMustBePositive(t *testing.T) {
	db, _ := newDB(t)
	settle := createAccount(t, db, "settlement", "core", "USD")

	// Each attempt inserts its own journal and the bad entry in one
	// transaction, so the CHECK fires on insert and is not masked by LD005.
	for i, bad := range []int64{0, -5} {
		key := fmt.Sprintf("bad-%d", i)
		tx, err := db.Begin()
		require.NoError(t, err)
		var journal string
		require.NoError(t, tx.QueryRow(
			`INSERT INTO ledger_journals (kind, idempotency_key, fingerprint, actor, reason, occurred_at)
			 VALUES ('fund', $1, 'fp', 'test', 'test', now()) RETURNING id`,
			key,
		).Scan(&journal))

		_, err = tx.Exec(
			`INSERT INTO ledger_entries (journal_id, account_id, side, amount, currency)
			 VALUES ($1, $2, 'Dr', $3, 'USD')`,
			journal, settle, bad,
		)
		pg := pgErr(t, err)
		require.Equal(t, sqlstateCheckViolation, pg.Code)
		require.Equal(t, conAmountPositive, pg.ConstraintName)
		_ = tx.Rollback()
	}
}

func TestZeroEntryJournalFailsAtCommit(t *testing.T) {
	db, _ := newDB(t)

	tx, err := db.Begin()
	require.NoError(t, err)
	_, err = tx.Exec(
		`INSERT INTO ledger_journals (kind, idempotency_key, fingerprint, actor, reason, occurred_at)
		 VALUES ('fund', 'empty', 'fp', 'test', 'test', now())`,
	)
	require.NoError(t, err, "journal insert itself succeeds")

	err = tx.Commit()
	require.Equal(t, sqlstateUnbalanced, pgErrCode(t, err), "journal with zero entries must fail at commit")
}

func TestDuplicateIdempotencyKey(t *testing.T) {
	db, _ := newDB(t)
	_, err := postBalanced(t, db, "fund", "same-key",
		leg{createAccount(t, db, "settlement", "core", "USD"), "Dr", 1, "USD"},
		leg{createAccount(t, db, "user_wallet", "u1", "USD"), "Cr", 1, "USD"},
	)
	require.NoError(t, err)

	_, err = postBalanced(t, db, "fund", "same-key",
		leg{createAccount(t, db, "settlement", "core2", "USD"), "Dr", 1, "USD"},
		leg{createAccount(t, db, "user_wallet", "u2", "USD"), "Cr", 1, "USD"},
	)
	pg := pgErr(t, err)
	require.Equal(t, sqlstateUniqueViolation, pg.Code)
	require.Equal(t, conIdempotencyKeyUniq, pg.ConstraintName)
}

func TestSecondReversalRejected(t *testing.T) {
	db, _ := newDB(t)
	settle := createAccount(t, db, "settlement", "core", "USD")
	user := createAccount(t, db, "user_wallet", "u1", "USD")

	original, err := postBalanced(t, db, "fund", "fund-1",
		leg{settle, "Dr", 100, "USD"},
		leg{user, "Cr", 100, "USD"},
	)
	require.NoError(t, err)

	insertReversal := func(key string) error {
		_, err := postBalancedReversing(t, db, "reversal", key, &original,
			leg{settle, "Cr", 100, "USD"},
			leg{user, "Dr", 100, "USD"},
		)
		return err
	}

	require.NoError(t, insertReversal("rev-1"), "first reversal must succeed")
	err = insertReversal("rev-2")
	pg := pgErr(t, err)
	require.Equal(t, sqlstateUniqueViolation, pg.Code)
	require.Equal(t, conReversesUniq, pg.ConstraintName, "second reversal must breach the reversal unique index")
}

func TestReversalKindRequiresTarget(t *testing.T) {
	db, _ := newDB(t)
	settle := createAccount(t, db, "settlement", "core", "USD")
	user := createAccount(t, db, "user_wallet", "u1", "USD")

	t.Run("reversal kind without target is rejected", func(t *testing.T) {
		_, err := postBalanced(t, db, "reversal", "rev-no-target",
			leg{settle, "Dr", 1, "USD"},
			leg{user, "Cr", 1, "USD"},
		)
		pg := pgErr(t, err)
		require.Equal(t, sqlstateCheckViolation, pg.Code)
		require.Equal(t, conReversalPairing, pg.ConstraintName)
	})

	t.Run("non-reversal kind with target is rejected", func(t *testing.T) {
		original, err := postBalanced(t, db, "fund", "fund-1",
			leg{settle, "Dr", 1, "USD"},
			leg{user, "Cr", 1, "USD"},
		)
		require.NoError(t, err)
		_, err = postBalancedReversing(t, db, "fund", "fund-2", &original,
			leg{settle, "Cr", 1, "USD"},
			leg{user, "Dr", 1, "USD"},
		)
		pg := pgErr(t, err)
		require.Equal(t, sqlstateCheckViolation, pg.Code)
		require.Equal(t, conReversalPairing, pg.ConstraintName)
	})
}

func TestUnbalancedJournalFailsAtCommit(t *testing.T) {
	db, _ := newDB(t)
	settle := createAccount(t, db, "settlement", "core", "USD")
	user := createAccount(t, db, "user_wallet", "u1", "USD")

	_, err := postBalanced(t, db, "fund", "fund-1",
		leg{settle, "Dr", 100, "USD"},
		leg{user, "Cr", 90, "USD"},
	)
	require.Equal(t, sqlstateUnbalanced, pgErrCode(t, err), "unbalanced journal must fail at commit")
}

func TestFewerThanTwoLegsFailsAtCommit(t *testing.T) {
	db, _ := newDB(t)
	settle := createAccount(t, db, "settlement", "core", "USD")

	_, err := postBalanced(t, db, "fee", "fee-1",
		leg{settle, "Dr", 100, "USD"},
	)
	require.Equal(t, sqlstateUnbalanced, pgErrCode(t, err), "journal with one leg must fail at commit")
}

func TestCurrencyMismatchFailsAtCommit(t *testing.T) {
	db, _ := newDB(t)
	usd := createAccount(t, db, "user_wallet", "u1", "USD")

	// Both legs balance in EUR and the accounts are USD, so the check reaches
	// the currency comparison and reports LD002.
	_, err := postBalanced(t, db, "fund", "fund-1",
		leg{usd, "Dr", 100, "EUR"},
		leg{usd, "Cr", 100, "EUR"},
	)
	require.Equal(t, sqlstateCurrency, pgErrCode(t, err), "entry currency must match account currency")
}

func TestEntryOutsideJournalTransaction(t *testing.T) {
	db, _ := newDB(t)
	settle := createAccount(t, db, "settlement", "core", "USD")
	user := createAccount(t, db, "user_wallet", "u1", "USD")

	// Commit a complete, balanced journal first.
	journal, err := postBalanced(t, db, "fund", "fund-1",
		leg{settle, "Dr", 100, "USD"},
		leg{user, "Cr", 100, "USD"},
	)
	require.NoError(t, err)

	// A later transaction tries to append another balanced pair. The journal
	// already committed in an earlier transaction, so LD005 rejects it.
	_, err = postBalancedAppend(t, db, journal,
		leg{settle, "Dr", 50, "USD"},
		leg{user, "Cr", 50, "USD"},
	)
	require.Equal(t, sqlstateOutsideTxn, pgErrCode(t, err), "entries may only be written with their journal")
}

// postBalancedAppend inserts legs into an existing journal in a new
// transaction. The guard rejects it; this helper returns that error.
func postBalancedAppend(t *testing.T, db *sql.DB, journal string, legs ...leg) (string, error) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	for _, l := range legs {
		if err := insertEntry(tx, journal, l.account, l.side, l.amount, l.currency); err != nil {
			return journal, err
		}
	}
	return journal, tx.Commit()
}

func TestNegativeBalanceRules(t *testing.T) {
	db, _ := newDB(t)
	user := createAccount(t, db, "user_wallet", "u1", "USD")
	settle := createAccount(t, db, "settlement", "core", "USD")

	t.Run("user_wallet cannot go negative", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO ledger_balances (account_id, balance) VALUES ($1, -1)`, user)
		require.Equal(t, sqlstateNegative, pgErrCode(t, err))
	})

	t.Run("holds cannot go negative", func(t *testing.T) {
		holds := createAccount(t, db, "holds", "u1", "USD")
		_, err := db.Exec(`INSERT INTO ledger_balances (account_id, balance) VALUES ($1, -1)`, holds)
		require.Equal(t, sqlstateNegative, pgErrCode(t, err))
	})

	t.Run("fees cannot go negative", func(t *testing.T) {
		fees := createAccount(t, db, "fees", "core", "USD")
		_, err := db.Exec(`INSERT INTO ledger_balances (account_id, balance) VALUES ($1, -1)`, fees)
		require.Equal(t, sqlstateNegative, pgErrCode(t, err))
	})

	t.Run("settlement may go negative", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO ledger_balances (account_id, balance) VALUES ($1, -500)`, settle)
		require.NoError(t, err)
	})

	t.Run("updating a balance negative is rejected", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO ledger_balances (account_id, balance) VALUES ($1, 10)`, user)
		require.NoError(t, err)
		res, err := db.Exec(`UPDATE ledger_balances SET balance = -1 WHERE account_id = $1`, user)
		require.Equal(t, sqlstateNegative, pgErrCode(t, err))
		if res != nil {
			n, _ := res.RowsAffected()
			require.EqualValues(t, 0, n, "no rows should be changed by a rejected update")
		}
	})
}

func TestOneAccountPerKindOwnerCurrency(t *testing.T) {
	db, _ := newDB(t)
	createAccount(t, db, "user_wallet", "u1", "USD")

	_, err := db.Exec(`INSERT INTO ledger_accounts (kind, owner_ref, currency) VALUES ('user_wallet', 'u1', 'USD')`)
	pg := pgErr(t, err)
	require.Equal(t, sqlstateUniqueViolation, pg.Code)
	require.Equal(t, conAccountUniq, pg.ConstraintName)

	_, err = db.Exec(`INSERT INTO ledger_accounts (kind, owner_ref, currency) VALUES ('user_wallet', 'u1', 'EUR')`)
	require.NoError(t, err)
}

func TestJournalKindConstraint(t *testing.T) {
	db, _ := newDB(t)
	_, err := db.Exec(
		`INSERT INTO ledger_journals (kind, idempotency_key, fingerprint, actor, reason, occurred_at)
		 VALUES ('not_a_kind', 'k', 'f', 'a', 'r', now())`,
	)
	pg := pgErr(t, err)
	require.Equal(t, sqlstateCheckViolation, pg.Code)
	require.Equal(t, conJournalKind, pg.ConstraintName)
}

func TestEntryForMissingJournal(t *testing.T) {
	db, _ := newDB(t)
	settle := createAccount(t, db, "settlement", "core", "USD")

	// A syntactically valid uuid that no journal uses.
	missing := "00000000-0000-0000-0000-000000000000"

	tx, err := db.Begin()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	_, err = tx.Exec(
		`INSERT INTO ledger_entries (journal_id, account_id, side, amount, currency) VALUES ($1, $2, 'Dr', 100, 'USD')`,
		missing, settle,
	)
	require.Equal(t, sqlstateForeignKeyViolation, pgErrCode(t, err),
		"an entry for a missing journal must be reported as a foreign key violation")
}

func TestAccountKindsProtected(t *testing.T) {
	db, _ := newDB(t)

	t.Run("update kind is rejected", func(t *testing.T) {
		_, err := db.Exec(`UPDATE ledger_account_kinds SET allow_negative = TRUE WHERE kind = 'user_wallet'`)
		require.Equal(t, sqlstateImmutable, pgErrCode(t, err))
	})
	t.Run("delete kind is rejected", func(t *testing.T) {
		_, err := db.Exec(`DELETE FROM ledger_account_kinds WHERE kind = 'user_wallet'`)
		require.Equal(t, sqlstateImmutable, pgErrCode(t, err))
	})
	t.Run("truncate kinds is rejected", func(t *testing.T) {
		// The table is the parent of a foreign key, so PostgreSQL may reject the
		// truncate with 0A000 before the trigger runs. Either way it is blocked.
		_, err := db.Exec(`TRUNCATE ledger_account_kinds`)
		require.Regexp(t, `^(LD004|0A000)$`, pgErrCode(t, err))
	})
}

func TestEntrySeqIndexExists(t *testing.T) {
	db, _ := newDB(t)
	var exists bool
	err := db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM pg_indexes WHERE indexname = 'ledger_entries_account_seq_idx'
	)`).Scan(&exists)
	require.NoError(t, err)
	require.True(t, exists, "entry sequence index must exist")
}

func TestMigrateDownThenUp(t *testing.T) {
	db, h := newDB(t)
	createAccount(t, db, "user_wallet", "u1", "USD")

	testdb.MigrateDown(t, h)

	var exists bool
	err := db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.tables WHERE table_name = 'ledger_accounts'
	)`).Scan(&exists)
	require.NoError(t, err)
	require.False(t, exists, "ledger_accounts should be dropped by the down migration")

	testdb.MigrateUp(t, h)

	err = db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.tables WHERE table_name = 'ledger_accounts'
	)`).Scan(&exists)
	require.NoError(t, err)
	require.True(t, exists, "ledger_accounts should exist after migrating up again")
}

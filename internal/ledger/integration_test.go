//go:build integration

package ledger_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Jaywestty/walletcore/internal/ledger"
	"github.com/Jaywestty/walletcore/internal/testdb"
)

func TestMain(m *testing.M) {
	testdb.Main(m)
}

// newRepo returns a Repository over a fresh cloned, migrated database.
func newRepo(t *testing.T) *ledger.Repository {
	t.Helper()
	h := testdb.New(t)
	pool, err := pgxpool.New(context.Background(), h.DSN)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return ledger.NewRepository(pool, ledger.Options{LockTimeout: 5 * time.Second})
}

func ref(kind ledger.AccountKind, owner, currency string) ledger.AccountRef {
	return ledger.AccountRef{Kind: kind, OwnerRef: owner, Currency: currency}
}

func fundReq(key, owner string, amount int64) ledger.PostRequest {
	return ledger.PostRequest{
		IdempotencyKey: key,
		Kind:           ledger.KindFund,
		Actor:          "system",
		Reason:         "fund",
		Legs: []ledger.Leg{
			{Account: ref(ledger.KindSettlement, "core", "USD"), Side: ledger.Debit, Amount: amount},
			{Account: ref(ledger.KindUserWallet, owner, "USD"), Side: ledger.Credit, Amount: amount},
		},
	}
}

func TestFundAndTransfer(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	j, err := repo.PostTransaction(ctx, fundReq("fund-u1", "u1", 1000))
	require.NoError(t, err)
	require.Len(t, j.Entries, 2)

	bal, err := repo.Balance(ctx, ref(ledger.KindUserWallet, "u1", "USD"))
	require.NoError(t, err)
	require.EqualValues(t, 1000, bal)

	// Transfer 400 from u1 to u2.
	tr := ledger.PostRequest{
		IdempotencyKey: "transfer-u1-u2",
		Kind:           ledger.KindTransfer,
		Actor:          "u1",
		Reason:         "transfer",
		Legs: []ledger.Leg{
			{Account: ref(ledger.KindUserWallet, "u1", "USD"), Side: ledger.Debit, Amount: 400},
			{Account: ref(ledger.KindUserWallet, "u2", "USD"), Side: ledger.Credit, Amount: 400},
		},
	}
	_, err = repo.PostTransaction(ctx, tr)
	require.NoError(t, err)

	b1, err := repo.Balance(ctx, ref(ledger.KindUserWallet, "u1", "USD"))
	require.NoError(t, err)
	require.EqualValues(t, 600, b1)
	b2, err := repo.Balance(ctx, ref(ledger.KindUserWallet, "u2", "USD"))
	require.NoError(t, err)
	require.EqualValues(t, 400, b2)
}

func TestOverdraftRejectedAndUnchanged(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	_, err := repo.PostTransaction(ctx, fundReq("fund-u1", "u1", 100))
	require.NoError(t, err)

	over := ledger.PostRequest{
		IdempotencyKey: "over",
		Kind:           ledger.KindTransfer,
		Actor:          "u1",
		Reason:         "overdraft",
		Legs: []ledger.Leg{
			{Account: ref(ledger.KindUserWallet, "u1", "USD"), Side: ledger.Debit, Amount: 500},
			{Account: ref(ledger.KindUserWallet, "u2", "USD"), Side: ledger.Credit, Amount: 500},
		},
	}
	_, err = repo.PostTransaction(ctx, over)
	require.True(t, errors.Is(err, ledger.ErrInsufficientFunds), "want ErrInsufficientFunds, got %v", err)

	// Nothing changed.
	b1, err := repo.Balance(ctx, ref(ledger.KindUserWallet, "u1", "USD"))
	require.NoError(t, err)
	require.EqualValues(t, 100, b1)
	_, err = repo.Balance(ctx, ref(ledger.KindUserWallet, "u2", "USD"))
	require.ErrorIs(t, err, ledger.ErrAccountNotFound, "u2 must not exist")
}

func TestUnbalancedRejected(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	req := ledger.PostRequest{
		IdempotencyKey: "unbalanced",
		Kind:           ledger.KindFund,
		Actor:          "system",
		Reason:         "bad",
		Legs: []ledger.Leg{
			{Account: ref(ledger.KindSettlement, "core", "USD"), Side: ledger.Debit, Amount: 100},
			{Account: ref(ledger.KindUserWallet, "u1", "USD"), Side: ledger.Credit, Amount: 90},
		},
	}
	_, err := repo.PostTransaction(ctx, req)
	require.True(t, errors.Is(err, ledger.ErrUnbalancedJournal), "want ErrUnbalancedJournal, got %v", err)

	// No account or journal rows should remain.
	_, err = repo.Balance(ctx, ref(ledger.KindUserWallet, "u1", "USD"))
	require.ErrorIs(t, err, ledger.ErrAccountNotFound)
}

func TestCrossCurrencyTransferRejectedAsUnbalanced(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	// A debit in USD and a credit in EUR form two one-sided currency groups,
	// so the journal is unbalanced per currency and is rejected as such.
	req := ledger.PostRequest{
		IdempotencyKey: "xcur",
		Kind:           ledger.KindTransfer,
		Actor:          "u1",
		Reason:         "mismatch",
		Legs: []ledger.Leg{
			{Account: ref(ledger.KindUserWallet, "u1", "USD"), Side: ledger.Debit, Amount: 100},
			{Account: ref(ledger.KindUserWallet, "u2", "EUR"), Side: ledger.Credit, Amount: 100},
		},
	}
	_, err := repo.PostTransaction(ctx, req)
	require.True(t, errors.Is(err, ledger.ErrUnbalancedJournal), "want unbalanced, got %v", err)
}

func TestSuspendedAccountRejected(t *testing.T) {
	ctx := context.Background()
	h := testdb.New(t)
	pool, err := pgxpool.New(ctx, h.DSN)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	repo := ledger.NewRepository(pool, ledger.Options{LockTimeout: 5 * time.Second})

	// Create then suspend u1.
	_, err = repo.PostTransaction(ctx, fundReq("fund-u1", "u1", 100))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE ledger_accounts SET status = 'suspended' WHERE kind = 'user_wallet' AND owner_ref = 'u1'`)
	require.NoError(t, err)

	req := ledger.PostRequest{
		IdempotencyKey: "to-suspended",
		Kind:           ledger.KindTransfer,
		Actor:          "u2",
		Reason:         "send",
		Legs: []ledger.Leg{
			{Account: ref(ledger.KindUserWallet, "u2", "USD"), Side: ledger.Debit, Amount: 10},
			{Account: ref(ledger.KindUserWallet, "u1", "USD"), Side: ledger.Credit, Amount: 10},
		},
	}
	// u2 has no balance, so this would fail on funds first; fund u2 first.
	_, err = repo.PostTransaction(ctx, fundReq("fund-u2", "u2", 100))
	require.NoError(t, err)
	_, err = repo.PostTransaction(ctx, req)
	require.True(t, errors.Is(err, ledger.ErrAccountInactive), "want ErrAccountInactive, got %v", err)
}

func TestReversal(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	original, err := repo.PostTransaction(ctx, fundReq("fund-u1", "u1", 500))
	require.NoError(t, err)

	rev := ledger.PostRequest{
		IdempotencyKey:    "rev-1",
		Kind:              ledger.KindReversal,
		Actor:             "system",
		Reason:            "reversal",
		ReversesJournalID: original.ID,
		Legs: []ledger.Leg{
			{Account: ref(ledger.KindSettlement, "core", "USD"), Side: ledger.Credit, Amount: 500},
			{Account: ref(ledger.KindUserWallet, "u1", "USD"), Side: ledger.Debit, Amount: 500},
		},
	}
	_, err = repo.PostTransaction(ctx, rev)
	require.NoError(t, err)

	b, err := repo.Balance(ctx, ref(ledger.KindUserWallet, "u1", "USD"))
	require.NoError(t, err)
	require.EqualValues(t, 0, b)

	// A second reversal is rejected.
	rev2 := rev
	rev2.IdempotencyKey = "rev-2"
	_, err = repo.PostTransaction(ctx, rev2)
	require.True(t, errors.Is(err, ledger.ErrAlreadyReversed), "want ErrAlreadyReversed, got %v", err)
}

func TestReversalMustMirror(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	original, err := repo.PostTransaction(ctx, fundReq("fund-u1", "u1", 500))
	require.NoError(t, err)

	bad := ledger.PostRequest{
		IdempotencyKey:    "rev-bad",
		Kind:              ledger.KindReversal,
		Actor:             "system",
		Reason:            "reversal",
		ReversesJournalID: original.ID,
		Legs: []ledger.Leg{
			{Account: ref(ledger.KindSettlement, "core", "USD"), Side: ledger.Credit, Amount: 400},
			{Account: ref(ledger.KindUserWallet, "u1", "USD"), Side: ledger.Debit, Amount: 400},
		},
	}
	_, err = repo.PostTransaction(ctx, bad)
	require.True(t, errors.Is(err, ledger.ErrInvalidJournal), "want ErrInvalidJournal, got %v", err)
}

func TestDuplicateIdempotencyKey(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)

	_, err := repo.PostTransaction(ctx, fundReq("dup", "u1", 100))
	require.NoError(t, err)

	// Same key, different payload is a conflict.
	_, err = repo.PostTransaction(ctx, fundReq("dup", "u2", 200))
	require.True(t, errors.Is(err, ledger.ErrIdempotencyConflict), "want ErrIdempotencyConflict, got %v", err)
}

func TestNilMetadataStoredAsEmptyObject(t *testing.T) {
	ctx := context.Background()
	h := testdb.New(t)
	pool, err := pgxpool.New(ctx, h.DSN)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	repo := ledger.NewRepository(pool, ledger.Options{LockTimeout: 5 * time.Second})

	req := fundReq("fund-u1", "u1", 100)
	req.Metadata = nil
	j, err := repo.PostTransaction(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, j.Metadata, "returned metadata must never be nil")

	var isNull bool
	var jsonType string
	err = pool.QueryRow(ctx,
		`SELECT metadata = 'null'::jsonb, jsonb_typeof(metadata) FROM ledger_journals WHERE id = $1`, j.ID,
	).Scan(&isNull, &jsonType)
	require.NoError(t, err)
	require.False(t, isNull, "metadata must not be stored as JSON null")
	require.Equal(t, "object", jsonType, "metadata must be stored as a JSON object")
}

func TestFailedPostWritesNothing(t *testing.T) {
	ctx := context.Background()
	h := testdb.New(t)
	pool, err := pgxpool.New(ctx, h.DSN)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	repo := ledger.NewRepository(pool, ledger.Options{LockTimeout: 5 * time.Second})

	_, err = repo.PostTransaction(ctx, fundReq("fund", "u1", 100))
	require.NoError(t, err)

	before := countRows(t, pool, "ledger_journals")

	over := ledger.PostRequest{
		IdempotencyKey: "over",
		Kind:           ledger.KindTransfer,
		Actor:          "u1",
		Reason:         "too much",
		Legs: []ledger.Leg{
			{Account: ref(ledger.KindUserWallet, "u1", "USD"), Side: ledger.Debit, Amount: 1000},
			{Account: ref(ledger.KindUserWallet, "u3", "USD"), Side: ledger.Credit, Amount: 1000},
		},
	}
	_, err = repo.PostTransaction(ctx, over)
	require.Error(t, err)

	after := countRows(t, pool, "ledger_journals")
	require.Equal(t, before, after, "a failed post must not persist a journal")

	_, err = repo.Balance(ctx, ref(ledger.KindUserWallet, "u3", "USD"))
	require.ErrorIs(t, err, ledger.ErrAccountNotFound, "u3 must not exist after a rolled back post")
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n))
	return n
}

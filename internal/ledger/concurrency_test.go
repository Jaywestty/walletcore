//go:build integration

package ledger

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Jaywestty/walletcore/internal/testdb"
)

// newPool returns a pool large enough for the concurrency tests and a fresh
// cloned migrated database.
func newPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := testdb.New(t)
	cfg, err := pgxpool.ParseConfig(h.DSN)
	require.NoError(t, err)
	// Pool must be larger than the maximum number of goroutines.
	cfg.MaxConns = 200
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func uref(owner string) AccountRef {
	return AccountRef{Kind: KindUserWallet, OwnerRef: owner, Currency: "USD"}
}

// fund posts settlement -> user_wallet for owner.
func fund(t *testing.T, repo *Repository, key, owner string, amount int64) {
	t.Helper()
	_, err := repo.PostTransaction(context.Background(), PostRequest{
		IdempotencyKey: key,
		Kind:           KindFund,
		Actor:          "system",
		Reason:         "fund",
		Legs: []Leg{
			{Account: AccountRef{Kind: KindSettlement, OwnerRef: "core", Currency: "USD"}, Side: Debit, Amount: amount},
			{Account: uref(owner), Side: Credit, Amount: amount},
		},
	})
	require.NoError(t, err)
}

func transferReq(key, from, to string, amount int64) PostRequest {
	return PostRequest{
		IdempotencyKey: key,
		Kind:           KindTransfer,
		Actor:          from,
		Reason:         "transfer",
		Legs: []Leg{
			{Account: uref(from), Side: Debit, Amount: amount},
			{Account: uref(to), Side: Credit, Amount: amount},
		},
	}
}

// assertInvariants checks the ledger-level invariants directly in the database.
func assertInvariants(t *testing.T, pool *pgxpool.Pool) {
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
}

func countJournals(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_journals`).Scan(&n))
	return n
}

// 1. 50 goroutines post the same key and payload.
func TestSameKeySamePayloadConcurrent(t *testing.T) {
	pool := newPool(t)
	repo := NewRepository(pool, Options{LockTimeout: 5 * time.Second})
	fund(t, repo, "seed", "u1", 1000)

	const n = 50
	req := transferReq("same", "u1", "u2", 100)
	ids := make([]string, n)
	replayed := make([]bool, n)
	errs := make([]error, n)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			j, err := repo.PostTransaction(context.Background(), req)
			errs[i] = err
			ids[i] = j.ID
			replayed[i] = j.Replayed
		}(i)
	}
	close(start)
	wg.Wait()

	first := ids[0]
	notReplayed := 0
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i], "call %d", i)
		require.Equal(t, first, ids[i], "all calls must return the same journal id")
		if !replayed[i] {
			notReplayed++
		}
	}
	require.Equal(t, 1, notReplayed, "exactly one call must not be a replay")
	require.Equal(t, 2, countJournals(t, pool), "seed plus exactly one transfer journal")
	assertInvariants(t, pool)
}

// 2. 50 goroutines, same key, different amounts.
func TestSameKeyDifferentPayloadConcurrent(t *testing.T) {
	pool := newPool(t)
	repo := NewRepository(pool, Options{LockTimeout: 5 * time.Second})
	fund(t, repo, "seed", "u1", 100000)

	const n = 50
	errs := make([]error, n)
	var ok int32

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := repo.PostTransaction(context.Background(), transferReq("conflict", "u1", "u2", int64(10+i)))
			errs[i] = err
			if err == nil {
				atomic.AddInt32(&ok, 1)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	require.EqualValues(t, 1, atomic.LoadInt32(&ok), "exactly one call succeeds")
	conflicts := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			require.True(t, errors.Is(errs[i], ErrIdempotencyConflict), "call %d err %v", i, errs[i])
			conflicts++
		}
	}
	require.Equal(t, n-1, conflicts)
	require.Equal(t, 2, countJournals(t, pool))
	assertInvariants(t, pool)
}

// 3. Funded for 10, 100 concurrent 1-unit debits.
func TestConcurrentDebitsNoOverdraft(t *testing.T) {
	pool := newPool(t)
	repo := NewRepository(pool, Options{LockTimeout: 5 * time.Second})
	fund(t, repo, "seed", "u1", 10)

	const n = 100
	var success, insufficient int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := repo.PostTransaction(context.Background(), transferReq(fmt.Sprintf("debit-%d", i), "u1", "sink", 1))
			switch {
			case err == nil:
				atomic.AddInt32(&success, 1)
			case errors.Is(err, ErrInsufficientFunds):
				atomic.AddInt32(&insufficient, 1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	require.EqualValues(t, 10, atomic.LoadInt32(&success), "exactly 10 debits succeed")
	require.EqualValues(t, 90, atomic.LoadInt32(&insufficient), "90 must fail for insufficient funds")

	bal, err := repo.Balance(context.Background(), uref("u1"))
	require.NoError(t, err)
	require.Zero(t, bal, "sender must end at zero")
	assertInvariants(t, pool)
}

// 4. Opposite-direction transfers between two accounts, no retries.
func TestOppositeTransfersNoDeadlock(t *testing.T) {
	pool := newPool(t)
	repo := NewRepository(pool, Options{LockTimeout: 5 * time.Second, MaxRetries: 0})

	t.Run("existing accounts", func(t *testing.T) {
		fund(t, repo, "seed-a", "a", 1_000_000)
		fund(t, repo, "seed-b", "b", 1_000_000)
		runOpposite(t, repo, "a", "b")
	})

	t.Run("new accounts", func(t *testing.T) {
		// Fund a third so validation passes; a and b exist from the previous
		// subtest in this same database. Use fresh names to force creation.
		fund(t, repo, "seed-c", "c", 1_000_000)
		fund(t, repo, "seed-d", "d", 1_000_000)
		runOpposite(t, repo, "c", "d")
	})
}

func runOpposite(t *testing.T, repo *Repository, a, b string) {
	t.Helper()
	const each = 200
	var deadlocks int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < each; i++ {
		from, to, key := a, b, fmt.Sprintf("ab-%d", i)
		if i%2 == 1 {
			from, to, key = b, a, fmt.Sprintf("ba-%d", i)
		}
		wg.Add(1)
		go func(from, to, key string) {
			defer wg.Done()
			<-start
			_, err := repo.PostTransaction(context.Background(), transferReq(key, from, to, 1))
			if err != nil && (errors.Is(err, ErrDeadlockDetected) || errors.Is(err, ErrSerializationFailure)) {
				atomic.AddInt32(&deadlocks, 1)
			}
		}(from, to, key)
	}
	close(start)
	wg.Wait()
	require.Zero(t, atomic.LoadInt32(&deadlocks), "no deadlocks expected with ordered locking")
	assertInvariants(t, repo.pool)
}

// 5. Random pairs of not-yet-existing accounts, no retries.
func TestRandomNewAccountsNoDeadlock(t *testing.T) {
	pool := newPool(t)
	// A generous lock timeout: this test is about deadlock freedom under
	// contention, not latency, and heavy fan-in makes waits legitimately long.
	repo := NewRepository(pool, Options{LockTimeout: 60 * time.Second, MaxRetries: 0})

	// Fund many owners up front so transfers have funds; accounts for the
	// transfer pairs themselves are still created lazily by the transfer.
	const accounts = 30
	for i := 0; i < accounts; i++ {
		fund(t, repo, fmt.Sprintf("seed-%d", i), fmt.Sprintf("w%d", i), 1000)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	var failures int32
	for i := 0; i < 300; i++ {
		from := fmt.Sprintf("w%d", i%accounts)
		to := fmt.Sprintf("w%d", (i*7+3)%accounts)
		if from == to {
			continue
		}
		wg.Add(1)
		go func(i int, from, to string) {
			defer wg.Done()
			<-start
			_, err := repo.PostTransaction(context.Background(), transferReq(fmt.Sprintf("rnd-%d", i), from, to, 1))
			if err != nil && !errors.Is(err, ErrInsufficientFunds) {
				atomic.AddInt32(&failures, 1)
				t.Errorf("unexpected error: %v", err)
			}
		}(i, from, to)
	}
	close(start)
	wg.Wait()
	require.Zero(t, atomic.LoadInt32(&failures))
	require.Zero(t, repo.Retries(), "retry counter must stay zero")
	assertInvariants(t, pool)
}

// 6. 20 reversals of the same journal with different keys.
func TestConcurrentReversals(t *testing.T) {
	pool := newPool(t)
	repo := NewRepository(pool, Options{LockTimeout: 5 * time.Second})

	orig, err := repo.PostTransaction(context.Background(), PostRequest{
		IdempotencyKey: "orig",
		Kind:           KindFund,
		Actor:          "system",
		Reason:         "fund",
		Legs: []Leg{
			{Account: AccountRef{Kind: KindSettlement, OwnerRef: "core", Currency: "USD"}, Side: Debit, Amount: 500},
			{Account: uref("u1"), Side: Credit, Amount: 500},
		},
	})
	require.NoError(t, err)

	const n = 20
	var success, already int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := repo.PostTransaction(context.Background(), PostRequest{
				IdempotencyKey:    fmt.Sprintf("rev-%d", i),
				Kind:              KindReversal,
				Actor:             "system",
				Reason:            "reversal",
				ReversesJournalID: orig.ID,
				Legs: []Leg{
					{Account: AccountRef{Kind: KindSettlement, OwnerRef: "core", Currency: "USD"}, Side: Credit, Amount: 500},
					{Account: uref("u1"), Side: Debit, Amount: 500},
				},
			})
			switch {
			case err == nil:
				atomic.AddInt32(&success, 1)
			case errors.Is(err, ErrAlreadyReversed):
				atomic.AddInt32(&already, 1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	require.EqualValues(t, 1, atomic.LoadInt32(&success), "exactly one reversal succeeds")
	require.EqualValues(t, n-1, atomic.LoadInt32(&already))
	bal, err := repo.Balance(context.Background(), uref("u1"))
	require.NoError(t, err)
	require.Zero(t, bal, "balance restored once")
	assertInvariants(t, pool)
}

// 7. Conservation across many transfers.
func TestConservation(t *testing.T) {
	pool := newPool(t)
	repo := NewRepository(pool, Options{LockTimeout: 5 * time.Second})

	const accounts = 10
	const perAccount = 1000
	for i := 0; i < accounts; i++ {
		fund(t, repo, fmt.Sprintf("seed-%d", i), fmt.Sprintf("w%d", i), perAccount)
	}

	const workers = 32
	const transfers = 500
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				from := fmt.Sprintf("w%d", i%accounts)
				to := fmt.Sprintf("w%d", (i*3+1)%accounts)
				if from == to {
					continue
				}
				_, err := repo.PostTransaction(context.Background(), transferReq(fmt.Sprintf("cons-%d", i), from, to, 1))
				if err != nil && !errors.Is(err, ErrInsufficientFunds) {
					t.Errorf("unexpected error: %v", err)
				}
			}
		}()
	}
	for i := 0; i < transfers; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	var total int64
	err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(b.balance), 0)
		FROM ledger_balances b
		JOIN ledger_accounts a ON a.id = b.account_id
		WHERE a.kind = 'user_wallet'`).Scan(&total)
	require.NoError(t, err)
	require.EqualValues(t, accounts*perAccount, total, "total wallet balances must equal total funded")
	assertInvariants(t, pool)
}

// 8. Replay after state change.
func TestReplayAfterStateChange(t *testing.T) {
	pool := newPool(t)
	repo := NewRepository(pool, Options{LockTimeout: 5 * time.Second})
	fund(t, repo, "seed", "u1", 1000)

	req := transferReq("replay", "u1", "u2", 400)
	first, err := repo.PostTransaction(context.Background(), req)
	require.NoError(t, err)
	require.False(t, first.Replayed)

	// Drain u1 so a fresh post would now fail.
	_, err = repo.PostTransaction(context.Background(), transferReq("drain", "u1", "sink", 600))
	require.NoError(t, err)

	before := countJournals(t, pool)
	replayed, err := repo.PostTransaction(context.Background(), req)
	require.NoError(t, err, "replay must succeed even when funds are now too low")
	require.True(t, replayed.Replayed)
	require.Equal(t, first.ID, replayed.ID, "replay returns the original journal")
	require.Equal(t, before, countJournals(t, pool), "no new journal on replay")
}

// 9. Ambiguous commit via terminating the backend just before commit.
func TestAmbiguousCommit(t *testing.T) {
	pool := newPool(t)
	repo := NewRepository(pool, Options{LockTimeout: 5 * time.Second})

	repo.beforeCommit = func(ctx context.Context, tx pgx.Tx) error {
		var pid int
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			return err
		}
		// Terminate this backend from a second connection.
		killer, err := pgx.Connect(ctx, pool.Config().ConnString())
		if err != nil {
			return err
		}
		defer func() { _ = killer.Close(ctx) }()
		_, _ = killer.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid)
		return nil
	}

	req := transferReq("ambiguous", "u1", "u2", 100)
	// u1 does not exist yet; fund it first. But beforeCommit fires for the fund
	// too, so fund with the hook disabled.
	repo.beforeCommit = nil
	fund(t, repo, "seed", "u1", 1000)
	repo.beforeCommit = func(ctx context.Context, tx pgx.Tx) error {
		var pid int
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			return err
		}
		killer, err := pgx.Connect(ctx, pool.Config().ConnString())
		if err != nil {
			return err
		}
		defer func() { _ = killer.Close(ctx) }()
		_, _ = killer.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid)
		return nil
	}

	_, err := repo.PostTransaction(context.Background(), req)
	require.True(t, errors.Is(err, ErrOutcomeUnknown), "want ErrOutcomeUnknown, got %v", err)

	// Retry with the same key after clearing the hook. It posts exactly once.
	repo.beforeCommit = nil
	final, err := repo.PostTransaction(context.Background(), req)
	require.NoError(t, err)

	b1, err := repo.Balance(context.Background(), uref("u1"))
	require.NoError(t, err)
	require.EqualValues(t, 900, b1)
	b2, err := repo.Balance(context.Background(), uref("u2"))
	require.NoError(t, err)
	require.EqualValues(t, 100, b2)
	require.NotEmpty(t, final.ID)
	assertInvariants(t, pool)
}

// 10. Context cancellation while blocked on a row lock.
func TestContextCancelWhileBlocked(t *testing.T) {
	pool := newPool(t)
	repo := NewRepository(pool, Options{LockTimeout: 30 * time.Second})

	fund(t, repo, "seed", "u1", 1000)

	// Hold a FOR UPDATE lock on u1's balance row in a separate transaction.
	holder, err := pool.Begin(context.Background())
	require.NoError(t, err)
	var u1ID string
	require.NoError(t, holder.QueryRow(context.Background(),
		`SELECT id::text FROM ledger_accounts WHERE kind = 'user_wallet' AND owner_ref = 'u1'`).Scan(&u1ID))
	_, err = holder.Exec(context.Background(), `SELECT 1 FROM ledger_balances WHERE account_id = $1 FOR UPDATE`, u1ID)
	require.NoError(t, err)

	// The posting blocks on the lock, then its context expires.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = repo.PostTransaction(ctx, transferReq("blocked", "u1", "u2", 100))
	require.Error(t, err)
	require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrOutcomeUnknown) || errors.Is(err, ErrLockTimeout),
		"want a context or lock error, got %v", err)

	// Release the lock.
	require.NoError(t, holder.Rollback(context.Background()))

	// The same key now posts successfully (nothing persisted while blocked).
	_, err = repo.PostTransaction(context.Background(), transferReq("blocked", "u1", "u2", 100))
	require.NoError(t, err)

	b1, err := repo.Balance(context.Background(), uref("u1"))
	require.NoError(t, err)
	require.EqualValues(t, 900, b1)
	assertInvariants(t, pool)
}

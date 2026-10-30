//go:build integration

package wallet_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Jaywestty/walletcore/internal/ledger"
	"github.com/Jaywestty/walletcore/internal/testdb"
	"github.com/Jaywestty/walletcore/internal/wallet"
)

func TestMain(m *testing.M) {
	testdb.Main(m)
}

func newService(t *testing.T) (*wallet.Service, *pgxpool.Pool) {
	t.Helper()
	h := testdb.New(t)
	cfg, err := pgxpool.ParseConfig(h.DSN)
	require.NoError(t, err)
	cfg.MaxConns = 100
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	repo := ledger.NewRepository(pool, ledger.Options{LockTimeout: 10 * time.Second, MaxRetries: 3})
	return wallet.New(repo), pool
}

func fund(t *testing.T, s *wallet.Service, owner string, amount int64) {
	t.Helper()
	_, err := s.Fund(context.Background(), wallet.FundRequest{
		IdempotencyKey: fmt.Sprintf("fund-%s-%d-%d", owner, amount, timeNano()),
		Actor:          "system",
		Owner:          owner,
		Amount:         amount,
		Currency:       "USD",
	})
	require.NoError(t, err)
}

// keyCounter gives distinct keys without importing time in helpers repeatedly.
var keyCounter atomic.Int64

func timeNano() int64 {
	return keyCounter.Add(1)
}

func TestFundThenBalancesAndReplay(t *testing.T) {
	ctx := context.Background()
	s, pool := newService(t)

	j, err := s.Fund(ctx, wallet.FundRequest{
		IdempotencyKey: "fund-once", Actor: "system", Owner: "u1", Amount: 500, Currency: "USD",
	})
	require.NoError(t, err)

	avail, held, err := s.Balances(ctx, "u1", "USD")
	require.NoError(t, err)
	require.EqualValues(t, 500, avail)
	require.EqualValues(t, 0, held)

	replay, err := s.Fund(ctx, wallet.FundRequest{
		IdempotencyKey: "fund-once", Actor: "system", Owner: "u1", Amount: 500, Currency: "USD",
	})
	require.NoError(t, err)
	require.True(t, replay.Replayed)
	require.Equal(t, j.ID, replay.ID)

	avail, _, err = s.Balances(ctx, "u1", "USD")
	require.NoError(t, err)
	require.EqualValues(t, 500, avail, "replay must not change the balance")
	testdb.AssertInvariants(t, pool)
}

func TestTransferWithFee(t *testing.T) {
	ctx := context.Background()
	s, pool := newService(t)
	fund(t, s, "u1", 1000)

	_, err := s.Transfer(ctx, wallet.TransferRequest{
		IdempotencyKey: "tf", Actor: "u1", From: "u1", To: "u2", Amount: 200, Fee: 5, Currency: "USD",
	})
	require.NoError(t, err)

	a1, _, _ := s.Balances(ctx, "u1", "USD")
	a2, _, _ := s.Balances(ctx, "u2", "USD")
	require.EqualValues(t, 795, a1, "sender pays amount + fee")
	require.EqualValues(t, 200, a2, "receiver gets amount")

	fee, err := ledger.NewRepository(pool, ledger.Options{}).Balance(ctx, ledger.AccountRef{
		Kind: ledger.KindFees, OwnerRef: "core", Currency: "USD",
	})
	require.NoError(t, err)
	require.EqualValues(t, 5, fee, "fees account gets the fee")
	testdb.AssertInvariants(t, pool)
}

func TestTransferRejections(t *testing.T) {
	ctx := context.Background()
	s, pool := newService(t)
	fund(t, s, "u1", 100)

	// Insufficient funds.
	_, err := s.Transfer(ctx, wallet.TransferRequest{
		IdempotencyKey: "insufficient", Actor: "u1", From: "u1", To: "u2", Amount: 500, Currency: "USD",
	})
	require.True(t, errors.Is(err, ledger.ErrInsufficientFunds), "want insufficient funds, got %v", err)
	a1, _, _ := s.Balances(ctx, "u1", "USD")
	require.EqualValues(t, 100, a1, "failed transfer changes nothing")

	// Self transfer.
	_, err = s.Transfer(ctx, wallet.TransferRequest{
		IdempotencyKey: "self", Actor: "u1", From: "u1", To: "u1", Amount: 10, Currency: "USD",
	})
	require.True(t, errors.Is(err, ledger.ErrInvalidJournal), "want invalid journal, got %v", err)

	// A user with only EUR cannot spend USD.
	_, err = s.Fund(ctx, wallet.FundRequest{
		IdempotencyKey: "eur", Actor: "system", Owner: "u2", Amount: 1000, Currency: "EUR",
	})
	require.NoError(t, err)
	_, err = s.Transfer(ctx, wallet.TransferRequest{
		IdempotencyKey: "cur", Actor: "u2", From: "u2", To: "u1", Amount: 10, Currency: "USD",
	})
	require.True(t, errors.Is(err, ledger.ErrInsufficientFunds), "want insufficient funds (no USD), got %v", err)
	testdb.AssertInvariants(t, pool)
}

func TestWithdrawalHappyPathAndOverDraw(t *testing.T) {
	ctx := context.Background()
	s, pool := newService(t)
	fund(t, s, "u1", 1000)

	req, err := s.RequestWithdrawal(ctx, wallet.WithdrawRequest{
		IdempotencyKey: "wd-1", Actor: "u1", Owner: "u1", Amount: 300, Currency: "USD",
	})
	require.NoError(t, err)

	avail, held, err := s.Balances(ctx, "u1", "USD")
	require.NoError(t, err)
	require.EqualValues(t, 700, avail, "available drops by the requested amount")
	require.EqualValues(t, 300, held, "held holds the requested amount")

	_, err = s.FinalizeWithdrawal(ctx, wallet.SettleWithdrawalRequest{
		IdempotencyKey: "fin-1", Actor: "system", RequestID: req.ID,
	})
	require.NoError(t, err)

	avail, held, err = s.Balances(ctx, "u1", "USD")
	require.NoError(t, err)
	require.EqualValues(t, 700, avail)
	require.EqualValues(t, 0, held, "held is emptied by finalize")

	settle, err := ledger.NewRepository(pool, ledger.Options{}).Balance(ctx, ledger.AccountRef{
		Kind: ledger.KindSettlement, OwnerRef: "core", Currency: "USD",
	})
	require.NoError(t, err)
	// Settlement is debit-normal: fund debits it 1000, finalize credits it 300,
	// so the net settlement balance is 700.
	require.EqualValues(t, 700, settle, "settlement reflects net flows")

	// A request over the available balance fails.
	_, err = s.RequestWithdrawal(ctx, wallet.WithdrawRequest{
		IdempotencyKey: "wd-over", Actor: "u1", Owner: "u1", Amount: 5000, Currency: "USD",
	})
	require.True(t, errors.Is(err, ledger.ErrInsufficientFunds), "want insufficient funds, got %v", err)
	testdb.AssertInvariants(t, pool)
}

func TestWithdrawalFailRestores(t *testing.T) {
	ctx := context.Background()
	s, pool := newService(t)
	fund(t, s, "u1", 1000)

	req, err := s.RequestWithdrawal(ctx, wallet.WithdrawRequest{
		IdempotencyKey: "wd", Actor: "u1", Owner: "u1", Amount: 400, Currency: "USD",
	})
	require.NoError(t, err)

	_, err = s.FailWithdrawal(ctx, wallet.SettleWithdrawalRequest{
		IdempotencyKey: "fail", Actor: "system", RequestID: req.ID,
	})
	require.NoError(t, err)

	avail, held, err := s.Balances(ctx, "u1", "USD")
	require.NoError(t, err)
	require.EqualValues(t, 1000, avail, "wallet restored exactly")
	require.EqualValues(t, 0, held)
	testdb.AssertInvariants(t, pool)
}

func TestDoubleSettlementMatrix(t *testing.T) {
	ctx := context.Background()
	s, pool := newService(t)

	setup := func(t *testing.T) string {
		fund(t, s, "u1", 1000)
		req, err := s.RequestWithdrawal(ctx, wallet.WithdrawRequest{
			IdempotencyKey: fmt.Sprintf("wd-%d", timeNano()), Actor: "u1", Owner: "u1", Amount: 100, Currency: "USD",
		})
		require.NoError(t, err)
		return req.ID
	}

	t.Run("finalize twice", func(t *testing.T) {
		id := setup(t)
		_, err := s.FinalizeWithdrawal(ctx, wallet.SettleWithdrawalRequest{IdempotencyKey: "f1", Actor: "system", RequestID: id})
		require.NoError(t, err)
		_, err = s.FinalizeWithdrawal(ctx, wallet.SettleWithdrawalRequest{IdempotencyKey: "f2", Actor: "system", RequestID: id})
		require.True(t, errors.Is(err, ledger.ErrWithdrawalAlreadySettled), "got %v", err)
	})

	t.Run("fail twice", func(t *testing.T) {
		id := setup(t)
		_, err := s.FailWithdrawal(ctx, wallet.SettleWithdrawalRequest{IdempotencyKey: "x1", Actor: "system", RequestID: id})
		require.NoError(t, err)
		_, err = s.FailWithdrawal(ctx, wallet.SettleWithdrawalRequest{IdempotencyKey: "x2", Actor: "system", RequestID: id})
		require.True(t, errors.Is(err, ledger.ErrWithdrawalAlreadySettled), "got %v", err)
	})

	t.Run("finalize then fail", func(t *testing.T) {
		id := setup(t)
		_, err := s.FinalizeWithdrawal(ctx, wallet.SettleWithdrawalRequest{IdempotencyKey: "a1", Actor: "system", RequestID: id})
		require.NoError(t, err)
		_, err = s.FailWithdrawal(ctx, wallet.SettleWithdrawalRequest{IdempotencyKey: "a2", Actor: "system", RequestID: id})
		require.True(t, errors.Is(err, ledger.ErrWithdrawalAlreadySettled), "got %v", err)
	})

	t.Run("fail then finalize", func(t *testing.T) {
		id := setup(t)
		_, err := s.FailWithdrawal(ctx, wallet.SettleWithdrawalRequest{IdempotencyKey: "b1", Actor: "system", RequestID: id})
		require.NoError(t, err)
		_, err = s.FinalizeWithdrawal(ctx, wallet.SettleWithdrawalRequest{IdempotencyKey: "b2", Actor: "system", RequestID: id})
		require.True(t, errors.Is(err, ledger.ErrWithdrawalAlreadySettled), "got %v", err)
	})
	testdb.AssertInvariants(t, pool)
}

func TestReversalMatrix(t *testing.T) {
	ctx := context.Background()
	s, pool := newService(t)

	t.Run("reverse fund while intact", func(t *testing.T) {
		fund(t, s, "r1", 500)
		// Find the fund journal by reversing the most recent fund we know the
		// key of is awkward; instead post a dedicated fund and reverse it.
		j, err := s.Fund(ctx, wallet.FundRequest{IdempotencyKey: "revfund-1", Actor: "system", Owner: "r1b", Amount: 200, Currency: "USD"})
		require.NoError(t, err)
		_, err = s.Reverse(ctx, wallet.ReverseRequest{IdempotencyKey: "revfund-1-rev", Actor: "system", JournalID: j.ID})
		require.NoError(t, err)
		avail, _, _ := s.Balances(ctx, "r1b", "USD")
		require.Zero(t, avail, "fund reversed restores zero")
	})

	t.Run("reverse fund after spent fails", func(t *testing.T) {
		j, err := s.Fund(ctx, wallet.FundRequest{IdempotencyKey: "revspend", Actor: "system", Owner: "r2", Amount: 300, Currency: "USD"})
		require.NoError(t, err)
		// Spend part of it.
		_, err = s.Transfer(ctx, wallet.TransferRequest{IdempotencyKey: "spend", Actor: "r2", From: "r2", To: "r2b", Amount: 100, Currency: "USD"})
		require.NoError(t, err)
		// Reverse the original fund: the wallet no longer has 300, so this
		// fails on funds.
		_, err = s.Reverse(ctx, wallet.ReverseRequest{IdempotencyKey: "revspend-rev", Actor: "system", JournalID: j.ID})
		require.True(t, errors.Is(err, ledger.ErrInsufficientFunds), "want insufficient funds, got %v", err)
	})

	t.Run("reverse transfer with fee restores all", func(t *testing.T) {
		fund(t, s, "f1", 1000)
		j, err := s.Transfer(ctx, wallet.TransferRequest{IdempotencyKey: "tfee", Actor: "f1", From: "f1", To: "f2", Amount: 200, Fee: 5, Currency: "USD"})
		require.NoError(t, err)
		_, err = s.Reverse(ctx, wallet.ReverseRequest{IdempotencyKey: "tfee-rev", Actor: "system", JournalID: j.ID})
		require.NoError(t, err)
		f1, _, _ := s.Balances(ctx, "f1", "USD")
		f2, _, _ := s.Balances(ctx, "f2", "USD")
		require.EqualValues(t, 1000, f1)
		require.Zero(t, f2)
	})

	t.Run("reverse a reversal rejected", func(t *testing.T) {
		j, err := s.Fund(ctx, wallet.FundRequest{IdempotencyKey: "rr-f", Actor: "system", Owner: "rr1", Amount: 50, Currency: "USD"})
		require.NoError(t, err)
		rev, err := s.Reverse(ctx, wallet.ReverseRequest{IdempotencyKey: "rr-r", Actor: "system", JournalID: j.ID})
		require.NoError(t, err)
		_, err = s.Reverse(ctx, wallet.ReverseRequest{IdempotencyKey: "rr-rr", Actor: "system", JournalID: rev.ID})
		require.True(t, errors.Is(err, ledger.ErrInvalidJournal), "got %v", err)
	})

	t.Run("reverse a withdrawal rejected", func(t *testing.T) {
		fund(t, s, "w1", 1000)
		req, err := s.RequestWithdrawal(ctx, wallet.WithdrawRequest{IdempotencyKey: "w-req", Actor: "w1", Owner: "w1", Amount: 100, Currency: "USD"})
		require.NoError(t, err)
		_, err = s.Reverse(ctx, wallet.ReverseRequest{IdempotencyKey: "w-rev", Actor: "system", JournalID: req.ID})
		require.True(t, errors.Is(err, ledger.ErrInvalidJournal), "got %v", err)
	})

	t.Run("reverse the same journal twice", func(t *testing.T) {
		j, err := s.Fund(ctx, wallet.FundRequest{IdempotencyKey: "tw-f", Actor: "system", Owner: "tw1", Amount: 80, Currency: "USD"})
		require.NoError(t, err)
		_, err = s.Reverse(ctx, wallet.ReverseRequest{IdempotencyKey: "tw-r1", Actor: "system", JournalID: j.ID})
		require.NoError(t, err)
		_, err = s.Reverse(ctx, wallet.ReverseRequest{IdempotencyKey: "tw-r2", Actor: "system", JournalID: j.ID})
		require.True(t, errors.Is(err, ledger.ErrAlreadyReversed), "got %v", err)
	})

	testdb.AssertInvariants(t, pool)
}

func TestRandomisedWorkload(t *testing.T) {
	ctx := context.Background()
	s, pool := newService(t)

	const users = 10
	// Seed every user so transfers and withdrawals have funds.
	for i := 0; i < users; i++ {
		fund(t, s, fmt.Sprintf("w%d", i), 1000)
	}

	const workers = 32
	const ops = 500
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				from := fmt.Sprintf("w%d", i%users)
				to := fmt.Sprintf("w%d", (i*3+1)%users)
				key := fmt.Sprintf("op-%d", i)
				switch i % 4 {
				case 0:
					_, err := s.Transfer(ctx, wallet.TransferRequest{IdempotencyKey: key, Actor: from, From: from, To: to, Amount: 1, Currency: "USD"})
					if err != nil && !errors.Is(err, ledger.ErrInsufficientFunds) && !errors.Is(err, ledger.ErrInvalidJournal) {
						t.Errorf("transfer %d: %v", i, err)
					}
				case 1:
					_, err := s.Transfer(ctx, wallet.TransferRequest{IdempotencyKey: key, Actor: from, From: from, To: to, Amount: 2, Fee: 1, Currency: "USD"})
					if err != nil && !errors.Is(err, ledger.ErrInsufficientFunds) && !errors.Is(err, ledger.ErrInvalidJournal) {
						t.Errorf("fee transfer %d: %v", i, err)
					}
				case 2:
					_, err := s.RequestWithdrawal(ctx, wallet.WithdrawRequest{IdempotencyKey: key, Actor: from, Owner: from, Amount: 1, Currency: "USD"})
					if err != nil && !errors.Is(err, ledger.ErrInsufficientFunds) {
						t.Errorf("withdraw %d: %v", i, err)
					}
				case 3:
					_, err := s.Fund(ctx, wallet.FundRequest{IdempotencyKey: key, Actor: "system", Owner: from, Amount: 1, Currency: "USD"})
					if err != nil {
						t.Errorf("fund %d: %v", i, err)
					}
				}
			}
		}()
	}
	for i := 0; i < ops; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	testdb.AssertInvariants(t, pool)
}

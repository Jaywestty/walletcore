//go:build integration && concurrency

package wallet_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Jaywestty/walletcore/internal/ledger"
	"github.com/Jaywestty/walletcore/internal/testdb"
	"github.com/Jaywestty/walletcore/internal/wallet"
)

// TestConcurrentSettlement fires 20 goroutines at the same withdrawal request,
// a mix of finalize and fail with distinct keys. Exactly one must win.
func TestConcurrentSettlement(t *testing.T) {
	ctx := context.Background()
	s, pool := newService(t)
	fund(t, s, "u1", 1000)

	req, err := s.RequestWithdrawal(ctx, wallet.WithdrawRequest{
		IdempotencyKey: "req", Actor: "u1", Owner: "u1", Amount: 400, Currency: "USD",
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
			key := fmt.Sprintf("settle-%d", i)
			var err error
			if i%2 == 0 {
				_, err = s.FinalizeWithdrawal(ctx, wallet.SettleWithdrawalRequest{IdempotencyKey: key, Actor: "system", RequestID: req.ID})
			} else {
				_, err = s.FailWithdrawal(ctx, wallet.SettleWithdrawalRequest{IdempotencyKey: key, Actor: "system", RequestID: req.ID})
			}
			switch {
			case err == nil:
				atomic.AddInt32(&success, 1)
			case errors.Is(err, ledger.ErrWithdrawalAlreadySettled):
				atomic.AddInt32(&already, 1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	require.EqualValues(t, 1, atomic.LoadInt32(&success), "exactly one settlement succeeds")
	require.EqualValues(t, n-1, atomic.LoadInt32(&already), "the rest see ErrWithdrawalAlreadySettled")
	testdb.AssertInvariants(t, pool)
}

package ledger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestClassifyCommitError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantUnknown bool
		wantDef     error
	}{
		{"nil", nil, false, nil},
		// Definite database rejections with a sentinel.
		{"LD001", &pgconn.PgError{Code: sqlstateUnbalanced}, false, ErrUnbalancedJournal},
		{"LD002", &pgconn.PgError{Code: sqlstateCurrency}, false, ErrCurrencyMismatch},
		{"23505 key", &pgconn.PgError{Code: sqlstateUniqueViolation, ConstraintName: constraintIdempotencyKeyUniq}, false, ErrIdempotencyConflict},
		{"23514 amount", &pgconn.PgError{Code: sqlstateCheckViolation, ConstraintName: constraintAmountPositiveChk}, false, ErrInvalidAmount},
		{"23xxx foreign key", &pgconn.PgError{Code: "23503"}, false, nil},
		{"40001", &pgconn.PgError{Code: sqlstateSerializationFail}, false, ErrSerializationFailure},
		{"40P01", &pgconn.PgError{Code: sqlstateDeadlockDetected}, false, ErrDeadlockDetected},
		// pgx reports an aborted transaction at commit as a definite failure.
		{"tx commit rollback", pgx.ErrTxCommitRollback, false, nil},
		// Ambiguous outcomes.
		{"class 08", &pgconn.PgError{Code: "08006"}, true, nil},
		{"57P01 admin shutdown", &pgconn.PgError{Code: sqlstateAdminShutdown}, true, nil},
		{"context deadline", context.DeadlineExceeded, true, nil},
		{"io unexpected eof", io.ErrUnexpectedEOF, true, nil},
		{"net op error", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("reset")}, true, nil},
		{"plain error", errors.New("network reset"), true, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyCommitError(tc.err)
			if tc.err == nil {
				if got != nil {
					t.Fatalf("nil err => %v, want nil", got)
				}
				return
			}
			if tc.wantUnknown {
				if !errors.Is(got, ErrOutcomeUnknown) {
					t.Fatalf("want ErrOutcomeUnknown, got %v", got)
				}
				return
			}
			if errors.Is(got, ErrOutcomeUnknown) {
				t.Fatalf("did not want ErrOutcomeUnknown, got %v", got)
			}
			if tc.wantDef != nil && !errors.Is(got, tc.wantDef) {
				t.Fatalf("want %v, got %v", tc.wantDef, got)
			}
		})
	}
}

func TestClassifyCommitErrorWrappedPgError(t *testing.T) {
	wrapped := fmt.Errorf("commit: %w", &pgconn.PgError{Code: sqlstateUnbalanced})
	if got := classifyCommitError(wrapped); !errors.Is(got, ErrUnbalancedJournal) {
		t.Fatalf("wrapped LD001 must be definite, got %v", got)
	}
}

func TestRetryable(t *testing.T) {
	if !retryable(fmt.Errorf("x: %w", ErrDeadlockDetected)) {
		t.Fatal("deadlock must be retryable")
	}
	if !retryable(ErrSerializationFailure) {
		t.Fatal("serialization must be retryable")
	}
	for _, e := range []error{ErrOutcomeUnknown, ErrInsufficientFunds, ErrLockTimeout, errors.New("x")} {
		if retryable(e) {
			t.Fatalf("%v must not be retryable", e)
		}
	}
}

func TestRetryLoopDeadlockTwiceThenSuccess(t *testing.T) {
	var counter atomic.Int64
	calls := 0
	j, err := retryLoop(context.Background(), 3, &counter, func(attempt int) (Journal, error) {
		calls++
		if attempt < 2 {
			return Journal{}, fmt.Errorf("x: %w", ErrDeadlockDetected)
		}
		return Journal{ID: "j"}, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if j.ID != "j" {
		t.Fatalf("journal = %q", j.ID)
	}
	if calls != 3 {
		t.Fatalf("attempts = %d, want 3", calls)
	}
	if got := counter.Load(); got != 2 {
		t.Fatalf("retry counter = %d, want 2", got)
	}
}

func TestRetryLoopDeadlockForever(t *testing.T) {
	var counter atomic.Int64
	calls := 0
	_, err := retryLoop(context.Background(), 3, &counter, func(int) (Journal, error) {
		calls++
		return Journal{}, fmt.Errorf("x: %w", ErrDeadlockDetected)
	})
	if !errors.Is(err, ErrDeadlockDetected) {
		t.Fatalf("want ErrDeadlockDetected, got %v", err)
	}
	if calls != 4 {
		t.Fatalf("attempts = %d, want 4 (1 + 3 retries)", calls)
	}
	if got := counter.Load(); got != 3 {
		t.Fatalf("retry counter = %d, want 3", got)
	}
}

func TestRetryLoopOutcomeUnknownNoRetry(t *testing.T) {
	var counter atomic.Int64
	calls := 0
	_, err := retryLoop(context.Background(), 3, &counter, func(int) (Journal, error) {
		calls++
		return Journal{}, fmt.Errorf("x: %w", ErrOutcomeUnknown)
	})
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("want ErrOutcomeUnknown, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry)", calls)
	}
	if counter.Load() != 0 {
		t.Fatalf("retry counter = %d, want 0", counter.Load())
	}
}

func TestRetryLoopNonRetryableNoRetry(t *testing.T) {
	var counter atomic.Int64
	calls := 0
	_, err := retryLoop(context.Background(), 3, &counter, func(int) (Journal, error) {
		calls++
		return Journal{}, fmt.Errorf("x: %w", ErrInsufficientFunds)
	})
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("want ErrInsufficientFunds, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("attempts = %d, want 1", calls)
	}
}

func TestRetryLoopContextCancelledDuringBackoff(t *testing.T) {
	var counter atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := retryLoop(ctx, 5, &counter, func(attempt int) (Journal, error) {
		calls++
		cancel()
		return Journal{}, fmt.Errorf("x: %w", ErrDeadlockDetected)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("attempts = %d, want 1", calls)
	}
}

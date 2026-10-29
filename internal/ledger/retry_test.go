package ledger

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestClassifyCommitError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantDef     error
		wantUnknown bool
	}{
		{"nil", nil, nil, false},
		{"unbalanced definite", &pgconn.PgError{Code: sqlstateUnbalanced}, ErrUnbalancedJournal, false},
		{"currency definite", &pgconn.PgError{Code: sqlstateCurrency}, ErrCurrencyMismatch, false},
		{"unique definite", &pgconn.PgError{Code: sqlstateUniqueViolation, ConstraintName: constraintIdempotencyKeyUniq}, ErrIdempotencyConflict, false},
		{"check definite", &pgconn.PgError{Code: sqlstateCheckViolation, ConstraintName: constraintAmountPositiveChk}, ErrInvalidAmount, false},
		{"deadlock definite", &pgconn.PgError{Code: sqlstateDeadlockDetected}, ErrDeadlockDetected, false},
		{"serialization definite", &pgconn.PgError{Code: sqlstateSerializationFail}, ErrSerializationFailure, false},
		{"conn failure ambiguous", &pgconn.PgError{Code: sqlstateConnectionFailure}, nil, true},
		{"admin shutdown ambiguous", &pgconn.PgError{Code: sqlstateAdminShutdown}, nil, true},
		{"query canceled ambiguous", &pgconn.PgError{Code: sqlstateQueryCanceled}, nil, true},
		{"unknown class ambiguous", &pgconn.PgError{Code: "XX000"}, nil, true},
		{"context deadline ambiguous", context.DeadlineExceeded, nil, true},
		{"context canceled ambiguous", context.Canceled, nil, true},
		{"plain error ambiguous", errors.New("network reset"), nil, true},
		{"wrapped context deadline ambiguous", fmt.Errorf("commit: %w", context.DeadlineExceeded), nil, true},
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
			if !errors.Is(got, tc.wantDef) {
				t.Fatalf("want %v, got %v", tc.wantDef, got)
			}
		})
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

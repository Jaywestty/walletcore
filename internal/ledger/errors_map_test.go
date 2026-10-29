package ledger

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestMapError(t *testing.T) {
	tests := []struct {
		name       string
		code       string
		constraint string
		want       error
	}{
		{"unbalanced", sqlstateUnbalanced, "", ErrUnbalancedJournal},
		{"currency", sqlstateCurrency, "", ErrCurrencyMismatch},
		{"negative", sqlstateNegative, "", ErrInsufficientFunds},
		{"immutable", sqlstateImmutable, "", ErrImmutableLedger},
		{"outside txn", sqlstateOutsideJournalTxn, "", ErrEntryOutsideJournal},
		{"truncate fk", sqlstateFeatureNotSupported, "", ErrImmutableLedger},
		{"missing journal fk", sqlstateForeignKeyViolation, "", ErrAccountNotFound},
		{"dup key", sqlstateUniqueViolation, constraintIdempotencyKeyUniq, ErrDuplicateIdempotencyKey},
		{"already reversed", sqlstateUniqueViolation, constraintReversesUniq, ErrAlreadyReversed},
		{"account uniq", sqlstateUniqueViolation, constraintAccountUniq, ErrInvalidJournal},
		{"amount check", sqlstateCheckViolation, constraintAmountPositiveChk, ErrInvalidAmount},
		{"reversal pairing", sqlstateCheckViolation, constraintReversalPairingChk, ErrInvalidJournal},
		{"journal kind", sqlstateCheckViolation, constraintJournalKindChk, ErrInvalidJournal},
		{"key check", sqlstateCheckViolation, constraintIdempotencyKeyChk, ErrInvalidJournal},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pg := &pgconn.PgError{Code: tc.code, ConstraintName: tc.constraint}
			got := MapError(pg)
			if !errors.Is(got, tc.want) {
				t.Fatalf("MapError = %v, want %v", got, tc.want)
			}
			// The original PgError must remain reachable.
			var want *pgconn.PgError
			if !errors.As(got, &want) {
				t.Fatalf("wrapped error does not expose *pgconn.PgError")
			}
		})
	}
}

func TestMapErrorUnknownAndNil(t *testing.T) {
	if err := MapError(nil); err != nil {
		t.Fatalf("nil should map to nil, got %v", err)
	}
	// An unrecognised SQLSTATE is returned unchanged.
	pg := &pgconn.PgError{Code: "XX000"}
	if got := MapError(pg); got != pg {
		t.Fatalf("unknown code should pass through, got %v", got)
	}
	// A non-database error is returned unchanged.
	plain := errors.New("boom")
	if got := MapError(plain); got != plain {
		t.Fatalf("plain error should pass through, got %v", got)
	}
}

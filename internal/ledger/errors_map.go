package ledger

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// Constraint names the mapper recognises. They must match the DDL exactly.
const (
	constraintIdempotencyKeyUniq = "ledger_journals_idempotency_key_uniq"
	constraintIdempotencyKeyChk  = "ledger_journals_idempotency_key_check"
	constraintAmountPositiveChk  = "ledger_entries_amount_positive_check"
	constraintReversalPairingChk = "ledger_journals_reversal_pairing_check"
	constraintJournalKindChk     = "ledger_journals_kind_check"
	constraintAccountUniq        = "ledger_accounts_kind_owner_currency_uniq"
	constraintReversesUniq       = "ledger_journals_reverses_uniq"
)

// SQLSTATE codes raised by the schema triggers.
const (
	sqlstateFeatureNotSupported = "0A000"
	sqlstateForeignKeyViolation = "23503"
	sqlstateUniqueViolation     = "23505"
	sqlstateCheckViolation      = "23514"
	sqlstateUnbalanced          = "LD001"
	sqlstateCurrency            = "LD002"
	sqlstateNegative            = "LD003"
	sqlstateImmutable           = "LD004"
	sqlstateOutsideJournalTxn   = "LD005"
)

// MapError converts a database error to a sentinel domain error. It matches on
// SQLSTATE and, for plain constraints, on ConstraintName. It never inspects
// message text. A non-database error is returned unchanged.
//
// The returned error wraps the original so callers can still reach the
// *pgconn.PgError with errors.As while matching the sentinel with errors.Is.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return err
	}

	sentinel := sentinelFor(pg)
	if sentinel == nil {
		return err
	}
	// Wrap both the sentinel and the original so errors.Is(err, sentinel) and
	// errors.As(err, *pgconn.PgError) both succeed.
	return fmt.Errorf("%w: %w", sentinel, err)
}

func sentinelFor(pg *pgconn.PgError) error {
	switch pg.Code {
	case sqlstateUnbalanced:
		return ErrUnbalancedJournal
	case sqlstateCurrency:
		return ErrCurrencyMismatch
	case sqlstateNegative:
		return ErrInsufficientFunds
	case sqlstateImmutable:
		return ErrImmutableLedger
	case sqlstateOutsideJournalTxn:
		return ErrEntryOutsideJournal
	case sqlstateFeatureNotSupported:
		// 0A000 here means a TRUNCATE on a protected table was refused because
		// of a foreign key. Treat it as an attempt to mutate immutable history.
		return ErrImmutableLedger
	case sqlstateForeignKeyViolation:
		// An entry for a missing journal is reported as a foreign key breach.
		return ErrAccountNotFound
	case sqlstateUniqueViolation:
		switch pg.ConstraintName {
		case constraintIdempotencyKeyUniq:
			return ErrDuplicateIdempotencyKey
		case constraintReversesUniq:
			return ErrAlreadyReversed
		case constraintAccountUniq:
			return ErrInvalidJournal
		}
	case sqlstateCheckViolation:
		switch pg.ConstraintName {
		case constraintAmountPositiveChk:
			return ErrInvalidAmount
		case constraintReversalPairingChk, constraintJournalKindChk, constraintIdempotencyKeyChk:
			return ErrInvalidJournal
		}
	}
	return nil
}

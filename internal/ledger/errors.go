package ledger

import "errors"

// Sentinel domain errors. Callers match them with errors.Is after the service
// wraps them with context. They correspond to the SQLSTATE and constraint
// mapping in docs/DESIGN.md and to the pure validation rules.
var (
	// ErrUnbalancedJournal means debits do not equal credits per currency, or
	// the journal has fewer than two (or more than twenty) legs. Mapped from
	// SQLSTATE LD001 at commit.
	ErrUnbalancedJournal = errors.New("ledger: journal is unbalanced")

	// ErrCurrencyMismatch means a leg currency differs from its account
	// currency, or two currencies appear in one journal group. Mapped from
	// SQLSTATE LD002.
	ErrCurrencyMismatch = errors.New("ledger: currency mismatch")

	// ErrInsufficientFunds means a balance would go negative on a kind that
	// forbids it. Mapped from SQLSTATE LD003.
	ErrInsufficientFunds = errors.New("ledger: insufficient funds")

	// ErrImmutableLedger means a write attempted to change immutable history.
	// Mapped from SQLSTATE LD004 and from 0A000 on a protected-table truncate.
	ErrImmutableLedger = errors.New("ledger: immutable ledger")

	// ErrEntryOutsideJournal means entries were written outside the
	// transaction that created their journal. Mapped from SQLSTATE LD005.
	ErrEntryOutsideJournal = errors.New("ledger: entry outside journal transaction")

	// ErrAlreadyReversed means the original journal was already reversed.
	// Mapped from constraint ledger_journals_reverses_uniq.
	ErrAlreadyReversed = errors.New("ledger: journal already reversed")

	// ErrInvalidAmount means an amount is not strictly positive.
	ErrInvalidAmount = errors.New("ledger: invalid amount")

	// ErrInvalidJournal means the journal shape or kind is invalid.
	ErrInvalidJournal = errors.New("ledger: invalid journal")

	// ErrIdempotencyConflict means the key was reused with a different payload.
	ErrIdempotencyConflict = errors.New("ledger: idempotency conflict")

	// ErrOutcomeUnknown means a commit returned an error that is not a definite
	// database rejection, so the transaction may or may not have committed.
	// The caller must resolve it by retrying with the same idempotency key.
	ErrOutcomeUnknown = errors.New("ledger: outcome unknown")

	// ErrLockTimeout means a row or table lock could not be acquired in time.
	ErrLockTimeout = errors.New("ledger: lock timeout")

	// ErrDeadlockDetected means the database detected a deadlock and aborted
	// this transaction.
	ErrDeadlockDetected = errors.New("ledger: deadlock detected")

	// ErrSerializationFailure means the transaction could not be serialized.
	ErrSerializationFailure = errors.New("ledger: serialization failure")

	// ErrAccountInactive means the account exists but is not active.
	ErrAccountInactive = errors.New("ledger: account inactive")

	// ErrAccountNotFound means no account exists for the reference.
	ErrAccountNotFound = errors.New("ledger: account not found")
)

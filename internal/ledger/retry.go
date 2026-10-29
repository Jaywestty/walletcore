package ledger

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// classifyCommitError decides whether a COMMIT error is a definite database
// rejection or an ambiguous outcome.
//
// It returns MapError(err) for definite rejections: a PgError with a constraint,
// trigger, ledger SQLSTATE, or a serialization/deadlock rejection, all of which
// mean the transaction did not commit. For anything else (a context error, a
// closed connection, SQLSTATE class 08, 57P01, 57014, or an unknown SQLSTATE)
// it returns an error wrapping both ErrOutcomeUnknown and the cause, because the
// outcome cannot be known from the error.
func classifyCommitError(err error) error {
	if err == nil {
		return nil
	}

	var pg *pgconn.PgError
	if errors.As(err, &pg) && isDefiniteRejection(pg.Code) {
		return MapError(err)
	}

	// Context cancellation or deadline during commit is inherently ambiguous.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
	}

	return fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
}

// isDefiniteRejection reports whether a SQLSTATE means the commit definitely
// failed and had no effect.
func isDefiniteRejection(code string) bool {
	switch code {
	case sqlstateUnbalanced, sqlstateCurrency, sqlstateNegative,
		sqlstateImmutable, sqlstateOutsideJournalTxn:
		return true
	}
	// Any 23xxx integrity constraint violation is a definite rejection.
	if len(code) >= 2 && code[:2] == "23" {
		return true
	}
	// Ledger SQLSTATE class LD is a definite rejection.
	if len(code) >= 2 && code[:2] == "LD" {
		return true
	}
	// Serialization and deadlock rejections are definite; the transaction was
	// rolled back by the server.
	switch code {
	case sqlstateDeadlockDetected, sqlstateSerializationFail, sqlstateLockNotAvailable:
		return true
	}
	return false
}

// retryable reports whether a posting error is safe to retry automatically.
func retryable(err error) bool {
	return errors.Is(err, ErrDeadlockDetected) || errors.Is(err, ErrSerializationFailure)
}

// backoff returns a jittered delay for the given attempt (1-based).
func backoff(attempt int) time.Duration {
	base := time.Duration(attempt) * 10 * time.Millisecond
	jitter := time.Duration(rand.Int64N(int64(base/2) + 1))
	return base + jitter
}

// sleep honours the context.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

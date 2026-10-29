package ledger

import "fmt"

// Delta computes the signed change to an account's cached balance for one leg.
//
// The cached balance is stored normal-side positive for every kind. A debit
// increases a debit-normal account and decreases a credit-normal one; a credit
// does the opposite. The result is the amount to add to the cached balance, so
// it can be negative.
func Delta(kind AccountKind, side Side, amount int64) (int64, error) {
	if amount < 0 {
		return 0, fmt.Errorf("%w: %d", ErrInvalidAmount, amount)
	}
	if !side.Valid() {
		return 0, fmt.Errorf("%w: side %q", ErrInvalidJournal, side)
	}

	normal := kind.NormalSide()
	if side == normal {
		return amount, nil
	}
	return -amount, nil
}

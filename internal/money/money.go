// Package money represents amounts as integer minor units in a currency.
//
// Amounts are int64 minor units (for example cents or kobo). There are no
// floats anywhere. A Money value is an amount plus an ISO 4217 currency code.
package money

import (
	"errors"
	"fmt"
	"math"
)

// ErrCurrencyMismatch is returned when an operation combines two different
// currencies.
var ErrCurrencyMismatch = errors.New("money: currency mismatch")

// ErrNegativeAmount is returned when a constructor is given a negative value.
var ErrNegativeAmount = errors.New("money: negative amount")

// ErrOverflow is returned when an arithmetic result would not fit in int64.
var ErrOverflow = errors.New("money: integer overflow")

// ErrInvalidCurrency is returned when a currency code is not three ASCII
// letters.
var ErrInvalidCurrency = errors.New("money: invalid currency code")

// Money is an amount in minor units with a currency.
type Money struct {
	amount   int64
	currency string
}

// New builds a Money from an amount in minor units and a currency code.
// The amount must be non-negative and the currency must be three ASCII
// letters A to Z.
func New(amount int64, currency string) (Money, error) {
	if amount < 0 {
		return Money{}, fmt.Errorf("%w: %d", ErrNegativeAmount, amount)
	}
	if !validCurrency(currency) {
		return Money{}, fmt.Errorf("%w: %q", ErrInvalidCurrency, currency)
	}
	return Money{amount: amount, currency: currency}, nil
}

// Zero returns a zero amount in the given currency.
func Zero(currency string) (Money, error) {
	return New(0, currency)
}

// Amount returns the amount in minor units.
func (m Money) Amount() int64 { return m.amount }

// Currency returns the ISO 4217 code.
func (m Money) Currency() string { return m.currency }

// IsZero reports whether the amount is zero.
func (m Money) IsZero() bool { return m.amount == 0 }

// String renders the amount and currency, for logs and errors only. It is not
// a display formatter with decimal places.
func (m Money) String() string { return fmt.Sprintf("%d %s", m.amount, m.currency) }

// Add returns m plus o. The currencies must match. The result must fit in
// int64; a wrapped value is reported as ErrOverflow instead of being returned.
func (m Money) Add(o Money) (Money, error) {
	if m.currency != o.currency {
		return Money{}, fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, o.currency)
	}
	sum := m.amount + o.amount
	// Addition overflow: both are non-negative, so overflow means the result
	// wrapped to a negative number.
	if sum < 0 {
		return Money{}, ErrOverflow
	}
	return Money{amount: sum, currency: m.currency}, nil
}

// Sub returns m minus o. The currencies must match and the result must not be
// negative. Subtraction below zero returns ErrNegativeAmount, and an int64
// overflow returns ErrOverflow.
func (m Money) Sub(o Money) (Money, error) {
	if m.currency != o.currency {
		return Money{}, fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, o.currency)
	}
	if o.amount > m.amount {
		return Money{}, fmt.Errorf("%w: %d minus %d", ErrNegativeAmount, m.amount, o.amount)
	}
	// Both amounts are non-negative and o.amount <= m.amount, so the result is
	// non-negative; this subtraction cannot wrap.
	return Money{amount: m.amount - o.amount, currency: m.currency}, nil
}

// Neg returns the signed negation of the amount, for balance arithmetic. It is
// the only place a negative minor-unit value is produced and is never exposed
// as a Money.
func (m Money) Neg() int64 {
	if m.amount == math.MinInt64 {
		// Only reachable if someone constructs MinInt64 directly; Money cannot
		// hold a negative amount through New, so this branch is defensive.
		return math.MaxInt64
	}
	return -m.amount
}

func validCurrency(c string) bool {
	if len(c) != 3 {
		return false
	}
	for i := 0; i < len(c); i++ {
		if c[i] < 'A' || c[i] > 'Z' {
			return false
		}
	}
	return true
}

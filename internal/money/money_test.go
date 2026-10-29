package money

import (
	"errors"
	"math"
	"testing"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name     string
		amount   int64
		currency string
		wantErr  error
	}{
		{"valid", 100, "USD", nil},
		{"zero", 0, "EUR", nil},
		{"max", math.MaxInt64, "USD", nil},
		{"negative", -1, "USD", ErrNegativeAmount},
		{"empty currency", 1, "", ErrInvalidCurrency},
		{"short currency", 1, "US", ErrInvalidCurrency},
		{"lowercase", 1, "usd", ErrInvalidCurrency},
		{"digits", 1, "US1", ErrInvalidCurrency},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.amount, tc.currency)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("New(%d, %q) err = %v, want %v", tc.amount, tc.currency, err, tc.wantErr)
			}
		})
	}
}

func TestAdd(t *testing.T) {
	a, _ := New(100, "USD")
	b, _ := New(50, "USD")
	eur, _ := New(50, "EUR")
	max, _ := New(math.MaxInt64, "USD")
	one, _ := New(1, "USD")

	tests := []struct {
		name    string
		x, y    Money
		want    int64
		wantErr error
	}{
		{"basic", a, b, 150, nil},
		{"zero", a, func() Money { z, _ := Zero("USD"); return z }(), 100, nil},
		{"currency mismatch", a, eur, 0, ErrCurrencyMismatch},
		{"overflow", max, one, 0, ErrOverflow},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.x.Add(tc.y)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && got.Amount() != tc.want {
				t.Fatalf("amount = %d, want %d", got.Amount(), tc.want)
			}
		})
	}
}

func TestSub(t *testing.T) {
	a, _ := New(100, "USD")
	b, _ := New(40, "USD")
	big, _ := New(200, "USD")
	eur, _ := New(40, "EUR")

	tests := []struct {
		name    string
		x, y    Money
		want    int64
		wantErr error
	}{
		{"basic", a, b, 60, nil},
		{"to zero", a, a, 0, nil},
		{"below zero", b, a, 0, ErrNegativeAmount},
		{"currency mismatch", a, eur, 0, ErrCurrencyMismatch},
		{"subtract bigger", b, big, 0, ErrNegativeAmount},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.x.Sub(tc.y)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && got.Amount() != tc.want {
				t.Fatalf("amount = %d, want %d", got.Amount(), tc.want)
			}
		})
	}
}

func TestNeg(t *testing.T) {
	m, _ := New(7, "USD")
	if got := m.Neg(); got != -7 {
		t.Fatalf("Neg = %d, want -7", got)
	}
}

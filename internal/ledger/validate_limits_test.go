package ledger

import (
	"errors"
	"math"
	"testing"
)

func TestMaxAmountBoundaries(t *testing.T) {
	mk := func(amount int64) PostRequest {
		return PostRequest{
			IdempotencyKey: "k",
			Kind:           KindFund,
			Actor:          "a",
			Reason:         "r",
			Legs: []Leg{
				{Account: AccountRef{Kind: KindSettlement, OwnerRef: "core", Currency: "USD"}, Side: Debit, Amount: amount},
				{Account: AccountRef{Kind: KindUserWallet, OwnerRef: "u1", Currency: "USD"}, Side: Credit, Amount: amount},
			},
		}
	}

	tests := []struct {
		name    string
		amount  int64
		wantErr error
	}{
		{"one", 1, nil},
		{"at max", MaxAmount, nil},
		{"above max", MaxAmount + 1, ErrInvalidAmount},
		{"max int64", math.MaxInt64, ErrInvalidAmount},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(mk(tc.amount)); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate(%d) = %v, want %v", tc.amount, err, tc.wantErr)
			}
		})
	}
}

func TestCheckedAdd(t *testing.T) {
	tests := []struct {
		a, b   int64
		want   int64
		wantOK bool
	}{
		{0, 0, 0, true},
		{1, 2, 3, true},
		{math.MaxInt64 - 1, 1, math.MaxInt64, true},
		{math.MaxInt64, 1, 0, false},
		{math.MaxInt64, math.MaxInt64, 0, false},
	}
	for _, tc := range tests {
		got, ok := checkedAdd(tc.a, tc.b)
		if ok != tc.wantOK {
			t.Fatalf("checkedAdd(%d,%d) ok = %v, want %v", tc.a, tc.b, ok, tc.wantOK)
		}
		if ok && got != tc.want {
			t.Fatalf("checkedAdd(%d,%d) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

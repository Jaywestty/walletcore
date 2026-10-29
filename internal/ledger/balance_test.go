package ledger

import (
	"errors"
	"testing"
)

func TestDelta(t *testing.T) {
	tests := []struct {
		name   string
		kind   AccountKind
		side   Side
		amount int64
		want   int64
	}{
		{"user wallet credit", KindUserWallet, Credit, 100, 100},
		{"user wallet debit", KindUserWallet, Debit, 100, -100},
		{"holds credit", KindHolds, Credit, 50, 50},
		{"holds debit", KindHolds, Debit, 50, -50},
		{"fees credit", KindFees, Credit, 10, 10},
		{"fees debit", KindFees, Debit, 10, -10},
		{"settlement debit", KindSettlement, Debit, 200, 200},
		{"settlement credit", KindSettlement, Credit, 200, -200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Delta(tc.kind, tc.side, tc.amount)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Delta = %d, want %d", got, tc.want)
			}
		})
	}

	if _, err := Delta(KindUserWallet, Debit, -1); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("negative amount: err = %v", err)
	}
	if _, err := Delta(KindUserWallet, Side("X"), 1); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("bad side: err = %v", err)
	}
}

func TestNormalSideAndNegative(t *testing.T) {
	for _, k := range []AccountKind{KindUserWallet, KindHolds, KindFees} {
		if k.NormalSide() != Credit {
			t.Errorf("%s normal side = %s, want Cr", k, k.NormalSide())
		}
		if k.AllowsNegative() {
			t.Errorf("%s must not allow negative", k)
		}
	}
	if KindSettlement.NormalSide() != Debit {
		t.Errorf("settlement normal side = %s, want Dr", KindSettlement.NormalSide())
	}
	if !KindSettlement.AllowsNegative() {
		t.Errorf("settlement must allow negative")
	}
}

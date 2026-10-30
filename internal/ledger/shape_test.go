package ledger

import (
	"errors"
	"testing"
)

func sRef(owner, currency string) AccountRef {
	return AccountRef{Kind: KindSettlement, OwnerRef: owner, Currency: currency}
}
func wRef(owner, currency string) AccountRef {
	return AccountRef{Kind: KindUserWallet, OwnerRef: owner, Currency: currency}
}
func hRef(owner, currency string) AccountRef {
	return AccountRef{Kind: KindHolds, OwnerRef: owner, Currency: currency}
}
func fRef(currency string) AccountRef {
	return AccountRef{Kind: KindFees, OwnerRef: "core", Currency: currency}
}

func TestValidateShapeFund(t *testing.T) {
	good := []Leg{
		{Account: sRef("core", "USD"), Side: Debit, Amount: 100},
		{Account: wRef("u1", "USD"), Side: Credit, Amount: 100},
	}
	if err := ValidateShape(KindFund, good); err != nil {
		t.Fatalf("valid fund rejected: %v", err)
	}
	bad := map[string][]Leg{
		"one leg": {good[0]},
		"wrong debit kind": {
			{Account: wRef("u1", "USD"), Side: Debit, Amount: 100},
			{Account: wRef("u2", "USD"), Side: Credit, Amount: 100},
		},
		"wrong settlement owner": {
			{Account: sRef("x", "USD"), Side: Debit, Amount: 100},
			{Account: wRef("u1", "USD"), Side: Credit, Amount: 100},
		},
		"amount mismatch": {
			{Account: sRef("core", "USD"), Side: Debit, Amount: 100},
			{Account: wRef("u1", "USD"), Side: Credit, Amount: 90},
		},
		"currency mismatch": {
			{Account: sRef("core", "USD"), Side: Debit, Amount: 100},
			{Account: wRef("u1", "EUR"), Side: Credit, Amount: 100},
		},
	}
	for name, legs := range bad {
		if err := ValidateShape(KindFund, legs); !errors.Is(err, ErrInvalidJournal) {
			t.Errorf("%s: want ErrInvalidJournal, got %v", name, err)
		}
	}
}

func TestValidateShapeTransfer(t *testing.T) {
	two := []Leg{
		{Account: wRef("u1", "USD"), Side: Debit, Amount: 100},
		{Account: wRef("u2", "USD"), Side: Credit, Amount: 100},
	}
	three := []Leg{
		{Account: wRef("u1", "USD"), Side: Debit, Amount: 105},
		{Account: wRef("u2", "USD"), Side: Credit, Amount: 100},
		{Account: fRef("USD"), Side: Credit, Amount: 5},
	}
	if err := ValidateShape(KindTransfer, two); err != nil {
		t.Fatalf("two-leg transfer rejected: %v", err)
	}
	if err := ValidateShape(KindTransfer, three); err != nil {
		t.Fatalf("three-leg transfer rejected: %v", err)
	}
	bad := map[string][]Leg{
		"one leg": two[:1],
		"same wallet": {
			{Account: wRef("u1", "USD"), Side: Debit, Amount: 100},
			{Account: wRef("u1", "USD"), Side: Credit, Amount: 100},
		},
		"amount not plus fee": {
			{Account: wRef("u1", "USD"), Side: Debit, Amount: 100},
			{Account: wRef("u2", "USD"), Side: Credit, Amount: 100},
			{Account: fRef("USD"), Side: Credit, Amount: 5},
		},
		"fee wrong owner": {
			{Account: wRef("u1", "USD"), Side: Debit, Amount: 105},
			{Account: wRef("u2", "USD"), Side: Credit, Amount: 100},
			{Account: AccountRef{Kind: KindFees, OwnerRef: "x", Currency: "USD"}, Side: Credit, Amount: 5},
		},
	}
	for name, legs := range bad {
		if err := ValidateShape(KindTransfer, legs); !errors.Is(err, ErrInvalidJournal) {
			t.Errorf("%s: want ErrInvalidJournal, got %v", name, err)
		}
	}
}

func TestValidateShapeFee(t *testing.T) {
	good := []Leg{
		{Account: wRef("u1", "USD"), Side: Debit, Amount: 7},
		{Account: fRef("USD"), Side: Credit, Amount: 7},
	}
	if err := ValidateShape(KindFee, good); err != nil {
		t.Fatalf("valid fee rejected: %v", err)
	}
	bad := []Leg{
		{Account: wRef("u1", "USD"), Side: Debit, Amount: 7},
		{Account: fRef("USD"), Side: Credit, Amount: 6},
	}
	if err := ValidateShape(KindFee, bad); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("amount mismatch: %v", err)
	}
}

func TestValidateShapeWithdrawalRequest(t *testing.T) {
	good := []Leg{
		{Account: wRef("u1", "USD"), Side: Debit, Amount: 40},
		{Account: hRef("u1", "USD"), Side: Credit, Amount: 40},
	}
	if err := ValidateShape(KindWithdrawalRequest, good); err != nil {
		t.Fatalf("valid withdrawal_request rejected: %v", err)
	}
	bad := []Leg{
		{Account: wRef("u1", "USD"), Side: Debit, Amount: 40},
		{Account: hRef("u2", "USD"), Side: Credit, Amount: 40},
	}
	if err := ValidateShape(KindWithdrawalRequest, bad); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("owner mismatch: %v", err)
	}
}

func TestValidateShapeTwoLegKinds(t *testing.T) {
	one := []Leg{{Account: hRef("u1", "USD"), Side: Debit, Amount: 1}}
	for _, k := range []JournalKind{KindWithdrawalFinalize, KindWithdrawalFail} {
		if err := ValidateShape(k, one); !errors.Is(err, ErrInvalidJournal) {
			t.Errorf("%s one leg: want ErrInvalidJournal, got %v", k, err)
		}
		two := []Leg{
			{Account: hRef("u1", "USD"), Side: Debit, Amount: 1},
			{Account: sRef("core", "USD"), Side: Credit, Amount: 1},
		}
		if err := ValidateShape(k, two); err != nil {
			t.Errorf("%s two legs rejected: %v", k, err)
		}
	}
}

func TestIsUUID(t *testing.T) {
	good := []string{"11111111-1111-1111-1111-111111111111", "ABCdef00-1234-5678-9abc-def012345678"}
	for _, s := range good {
		if !isUUID(s) {
			t.Errorf("%q should be a uuid", s)
		}
	}
	bad := []string{"", "not-a-uuid", "11111111-1111-1111-1111-11111111111", "11111111_1111_1111_1111_111111111111"}
	for _, s := range bad {
		if isUUID(s) {
			t.Errorf("%q should not be a uuid", s)
		}
	}
}

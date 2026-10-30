package wallet

import (
	"errors"
	"strings"
	"testing"

	"github.com/Jaywestty/walletcore/internal/ledger"
)

func TestValidateFund(t *testing.T) {
	base := FundRequest{IdempotencyKey: "k", Actor: "a", Owner: "u1", Amount: 10, Currency: "USD"}
	tests := []struct {
		name    string
		mutate  func(*FundRequest)
		wantErr error
	}{
		{"valid", func(*FundRequest) {}, nil},
		{"no key", func(r *FundRequest) { r.IdempotencyKey = "" }, ErrInvalidRequest},
		{"no actor", func(r *FundRequest) { r.Actor = "" }, ErrInvalidRequest},
		{"no owner", func(r *FundRequest) { r.Owner = "" }, ErrInvalidRequest},
		{"zero amount", func(r *FundRequest) { r.Amount = 0 }, ledger.ErrInvalidAmount},
		{"negative amount", func(r *FundRequest) { r.Amount = -1 }, ledger.ErrInvalidAmount},
		{"over max", func(r *FundRequest) { r.Amount = ledger.MaxAmount + 1 }, ledger.ErrInvalidAmount},
		{"bad currency", func(r *FundRequest) { r.Currency = "usd" }, ErrInvalidRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.mutate(&r)
			if err := validateFund(r); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateTransfer(t *testing.T) {
	base := TransferRequest{IdempotencyKey: "k", Actor: "a", From: "u1", To: "u2", Amount: 10, Currency: "USD"}
	tests := []struct {
		name    string
		mutate  func(*TransferRequest)
		wantErr error
	}{
		{"valid", func(*TransferRequest) {}, nil},
		{"valid with fee", func(r *TransferRequest) { r.Fee = 1 }, nil},
		{"self transfer", func(r *TransferRequest) { r.To = "u1" }, ledger.ErrInvalidJournal},
		{"no from", func(r *TransferRequest) { r.From = "" }, ErrInvalidRequest},
		{"no to", func(r *TransferRequest) { r.To = "" }, ErrInvalidRequest},
		{"zero amount", func(r *TransferRequest) { r.Amount = 0 }, ledger.ErrInvalidAmount},
		{"negative fee", func(r *TransferRequest) { r.Fee = -1 }, ledger.ErrInvalidAmount},
		{"amount plus fee over max", func(r *TransferRequest) { r.Amount = ledger.MaxAmount; r.Fee = 1 }, ledger.ErrInvalidAmount},
		{"bad currency", func(r *TransferRequest) { r.Currency = "US" }, ErrInvalidRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.mutate(&r)
			if err := validateTransfer(r); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateChargeFeeAndWithdraw(t *testing.T) {
	fee := ChargeFeeRequest{IdempotencyKey: "k", Actor: "a", Owner: "u1", Amount: 5, Currency: "USD"}
	if err := validateChargeFee(fee); err != nil {
		t.Fatalf("valid fee rejected: %v", err)
	}
	fee.Amount = 0
	if err := validateChargeFee(fee); !errors.Is(err, ledger.ErrInvalidAmount) {
		t.Fatalf("zero fee amount: %v", err)
	}

	wd := WithdrawRequest{IdempotencyKey: "k", Actor: "a", Owner: "u1", Amount: 5, Currency: "USD"}
	if err := validateWithdraw(wd); err != nil {
		t.Fatalf("valid withdraw rejected: %v", err)
	}
	wd.Owner = ""
	if err := validateWithdraw(wd); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing owner: %v", err)
	}
}

func TestValidateSettleAndReverse(t *testing.T) {
	s := SettleWithdrawalRequest{IdempotencyKey: "k", Actor: "a", RequestID: "11111111-1111-1111-1111-111111111111"}
	if err := validateSettle(s); err != nil {
		t.Fatalf("valid settle rejected: %v", err)
	}
	s.RequestID = ""
	if err := validateSettle(s); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing request id: %v", err)
	}

	r := ReverseRequest{IdempotencyKey: "k", Actor: "a", JournalID: "33333333-3333-3333-3333-333333333333"}
	if err := validateReverse(r); err != nil {
		t.Fatalf("valid reverse rejected: %v", err)
	}
	r.JournalID = ""
	if err := validateReverse(r); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("missing journal id: %v", err)
	}
}

func TestValidateOwnerAndActorBounds(t *testing.T) {
	long := strings.Repeat("a", 129)
	ok128 := strings.Repeat("a", 128)

	base := FundRequest{IdempotencyKey: "k", Actor: "a", Owner: ok128, Amount: 1, Currency: "USD"}
	if err := validateFund(base); err != nil {
		t.Fatalf("128-char owner should be valid: %v", err)
	}

	base.Owner = long
	if err := validateFund(base); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("129-char owner should be rejected, got %v", err)
	}

	base.Owner = "u1"
	base.Actor = long
	if err := validateFund(base); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("129-char actor should be rejected, got %v", err)
	}

	base.Actor = "a\x00b"
	if err := validateFund(base); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("control char in actor should be rejected, got %v", err)
	}
}

func TestValidateSettleUUID(t *testing.T) {
	s := SettleWithdrawalRequest{IdempotencyKey: "k", Actor: "a", RequestID: "not-a-uuid"}
	if err := validateSettle(s); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("non-uuid request id should be rejected, got %v", err)
	}
	r := ReverseRequest{IdempotencyKey: "k", Actor: "a", JournalID: "not-a-uuid"}
	if err := validateReverse(r); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("non-uuid journal id should be rejected, got %v", err)
	}
}

package wallet

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Jaywestty/walletcore/internal/ledger"
)

// Reasons used for each operation. Fixed strings keep journals readable and
// stable.
const (
	reasonFund               = "wallet fund"
	reasonTransfer           = "wallet transfer"
	reasonChargeFee          = "wallet fee"
	reasonRequestWithdrawal  = "withdrawal request"
	reasonFinalizeWithdrawal = "withdrawal finalize"
	reasonFailWithdrawal     = "withdrawal fail"
	reasonReversal           = "journal reversal"
)

// ErrInvalidRequest covers wallet-level validation failures.
var ErrInvalidRequest = errors.New("wallet: invalid request")

// FundRequest credits a user's wallet from settlement.
type FundRequest struct {
	IdempotencyKey string
	Actor          string
	Owner          string
	Amount         int64
	Currency       string
}

// TransferRequest moves funds between two users, optionally charging a fee to
// the sender.
type TransferRequest struct {
	IdempotencyKey string
	Actor          string
	From           string
	To             string
	Amount         int64
	Fee            int64
	Currency       string
}

// ChargeFeeRequest debits a user's wallet into the fees account.
type ChargeFeeRequest struct {
	IdempotencyKey string
	Actor          string
	Owner          string
	Amount         int64
	Currency       string
}

// WithdrawRequest moves funds from a user's wallet into holds.
type WithdrawRequest struct {
	IdempotencyKey string
	Actor          string
	Owner          string
	Amount         int64
	Currency       string
}

// SettleWithdrawalRequest resolves a withdrawal request. The owner, amount and
// currency are derived from the request journal, so only the request id is
// needed.
type SettleWithdrawalRequest struct {
	IdempotencyKey string
	Actor          string
	RequestID      string
}

// ReverseRequest reverses an existing journal by id.
type ReverseRequest struct {
	IdempotencyKey string
	Actor          string
	JournalID      string
}

// validateCommon checks fields every operation shares.
func validateCommon(key, actor string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("%w: idempotency key is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(actor) == "" {
		return fmt.Errorf("%w: actor is required", ErrInvalidRequest)
	}
	return nil
}

func validateOwner(owner string) error {
	if strings.TrimSpace(owner) == "" {
		return fmt.Errorf("%w: owner is required", ErrInvalidRequest)
	}
	return nil
}

func validateAmount(amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("%w: amount must be positive", ledger.ErrInvalidAmount)
	}
	if amount > ledger.MaxAmount {
		return fmt.Errorf("%w: amount exceeds maximum %d", ledger.ErrInvalidAmount, ledger.MaxAmount)
	}
	return nil
}

func validateCurrency(currency string) error {
	if len(currency) != 3 {
		return fmt.Errorf("%w: currency must be a 3-letter code", ErrInvalidRequest)
	}
	for i := 0; i < len(currency); i++ {
		if currency[i] < 'A' || currency[i] > 'Z' {
			return fmt.Errorf("%w: currency must be an uppercase 3-letter code", ErrInvalidRequest)
		}
	}
	return nil
}

func validateFund(r FundRequest) error {
	if err := validateCommon(r.IdempotencyKey, r.Actor); err != nil {
		return err
	}
	if err := validateOwner(r.Owner); err != nil {
		return err
	}
	if err := validateAmount(r.Amount); err != nil {
		return err
	}
	return validateCurrency(r.Currency)
}

func validateTransfer(r TransferRequest) error {
	if err := validateCommon(r.IdempotencyKey, r.Actor); err != nil {
		return err
	}
	if err := validateOwner(r.From); err != nil {
		return err
	}
	if err := validateOwner(r.To); err != nil {
		return err
	}
	if strings.TrimSpace(r.From) == strings.TrimSpace(r.To) {
		return fmt.Errorf("%w: cannot transfer to self", ledger.ErrInvalidJournal)
	}
	if err := validateAmount(r.Amount); err != nil {
		return err
	}
	if r.Fee < 0 {
		return fmt.Errorf("%w: fee must not be negative", ledger.ErrInvalidAmount)
	}
	if r.Fee > ledger.MaxAmount {
		return fmt.Errorf("%w: fee exceeds maximum %d", ledger.ErrInvalidAmount, ledger.MaxAmount)
	}
	// The sender is debited amount + fee; that total must also fit the bound.
	if r.Amount > ledger.MaxAmount-r.Fee {
		return fmt.Errorf("%w: amount plus fee exceeds maximum %d", ledger.ErrInvalidAmount, ledger.MaxAmount)
	}
	return validateCurrency(r.Currency)
}

func validateChargeFee(r ChargeFeeRequest) error {
	if err := validateCommon(r.IdempotencyKey, r.Actor); err != nil {
		return err
	}
	if err := validateOwner(r.Owner); err != nil {
		return err
	}
	if err := validateAmount(r.Amount); err != nil {
		return err
	}
	return validateCurrency(r.Currency)
}

func validateWithdraw(r WithdrawRequest) error {
	if err := validateCommon(r.IdempotencyKey, r.Actor); err != nil {
		return err
	}
	if err := validateOwner(r.Owner); err != nil {
		return err
	}
	if err := validateAmount(r.Amount); err != nil {
		return err
	}
	return validateCurrency(r.Currency)
}

func validateSettle(r SettleWithdrawalRequest) error {
	if err := validateCommon(r.IdempotencyKey, r.Actor); err != nil {
		return err
	}
	if strings.TrimSpace(r.RequestID) == "" {
		return fmt.Errorf("%w: request id is required", ErrInvalidRequest)
	}
	return nil
}

func validateReverse(r ReverseRequest) error {
	if err := validateCommon(r.IdempotencyKey, r.Actor); err != nil {
		return err
	}
	if strings.TrimSpace(r.JournalID) == "" {
		return fmt.Errorf("%w: journal id is required", ErrInvalidRequest)
	}
	return nil
}

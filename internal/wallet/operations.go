package wallet

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Jaywestty/walletcore/internal/ledger"
)

// Fund credits a user's wallet from settlement.
func (s *Service) Fund(ctx context.Context, r FundRequest) (ledger.Journal, error) {
	if err := validateFund(r); err != nil {
		return ledger.Journal{}, err
	}
	return s.ledger.PostTransaction(ctx, ledger.PostRequest{
		IdempotencyKey: r.IdempotencyKey,
		Kind:           ledger.KindFund,
		Actor:          r.Actor,
		Reason:         reasonFund,
		Legs: []ledger.Leg{
			{Account: settlementRef(r.Currency), Side: ledger.Debit, Amount: r.Amount},
			{Account: walletRef(r.Owner, r.Currency), Side: ledger.Credit, Amount: r.Amount},
		},
	})
}

// Transfer moves funds from one user to another, optionally charging a fee to
// the sender. All legs post in one transfer journal.
func (s *Service) Transfer(ctx context.Context, r TransferRequest) (ledger.Journal, error) {
	if err := validateTransfer(r); err != nil {
		return ledger.Journal{}, err
	}

	legs := []ledger.Leg{
		{Account: walletRef(r.From, r.Currency), Side: ledger.Debit, Amount: r.Amount + r.Fee},
		{Account: walletRef(r.To, r.Currency), Side: ledger.Credit, Amount: r.Amount},
	}
	if r.Fee > 0 {
		legs = append(legs, ledger.Leg{Account: feesRef(r.Currency), Side: ledger.Credit, Amount: r.Fee})
	}

	return s.ledger.PostTransaction(ctx, ledger.PostRequest{
		IdempotencyKey: r.IdempotencyKey,
		Kind:           ledger.KindTransfer,
		Actor:          r.Actor,
		Reason:         reasonTransfer,
		Legs:           legs,
	})
}

// ChargeFee debits a user's wallet into the fees account.
func (s *Service) ChargeFee(ctx context.Context, r ChargeFeeRequest) (ledger.Journal, error) {
	if err := validateChargeFee(r); err != nil {
		return ledger.Journal{}, err
	}
	return s.ledger.PostTransaction(ctx, ledger.PostRequest{
		IdempotencyKey: r.IdempotencyKey,
		Kind:           ledger.KindFee,
		Actor:          r.Actor,
		Reason:         reasonChargeFee,
		Legs: []ledger.Leg{
			{Account: walletRef(r.Owner, r.Currency), Side: ledger.Debit, Amount: r.Amount},
			{Account: feesRef(r.Currency), Side: ledger.Credit, Amount: r.Amount},
		},
	})
}

// RequestWithdrawal moves funds from a user's wallet into holds.
func (s *Service) RequestWithdrawal(ctx context.Context, r WithdrawRequest) (ledger.Journal, error) {
	if err := validateWithdraw(r); err != nil {
		return ledger.Journal{}, err
	}
	return s.ledger.PostTransaction(ctx, ledger.PostRequest{
		IdempotencyKey: r.IdempotencyKey,
		Kind:           ledger.KindWithdrawalRequest,
		Actor:          r.Actor,
		Reason:         reasonRequestWithdrawal,
		Legs: []ledger.Leg{
			{Account: walletRef(r.Owner, r.Currency), Side: ledger.Debit, Amount: r.Amount},
			{Account: holdsRef(r.Owner, r.Currency), Side: ledger.Credit, Amount: r.Amount},
		},
	})
}

// FinalizeWithdrawal settles a withdrawal request by moving the held funds to
// settlement. The owner, amount and currency come from the request journal.
func (s *Service) FinalizeWithdrawal(ctx context.Context, r SettleWithdrawalRequest) (ledger.Journal, error) {
	if err := validateSettle(r); err != nil {
		return ledger.Journal{}, err
	}
	owner, amount, currency, err := s.requestTerms(ctx, r.RequestID)
	if err != nil {
		return ledger.Journal{}, err
	}
	return s.ledger.PostTransaction(ctx, ledger.PostRequest{
		IdempotencyKey:   r.IdempotencyKey,
		Kind:             ledger.KindWithdrawalFinalize,
		Actor:            r.Actor,
		Reason:           reasonFinalizeWithdrawal,
		SettlesJournalID: r.RequestID,
		Legs: []ledger.Leg{
			{Account: holdsRef(owner, currency), Side: ledger.Debit, Amount: amount},
			{Account: settlementRef(currency), Side: ledger.Credit, Amount: amount},
		},
	})
}

// FailWithdrawal settles a withdrawal request by returning the held funds to
// the user's wallet.
func (s *Service) FailWithdrawal(ctx context.Context, r SettleWithdrawalRequest) (ledger.Journal, error) {
	if err := validateSettle(r); err != nil {
		return ledger.Journal{}, err
	}
	owner, amount, currency, err := s.requestTerms(ctx, r.RequestID)
	if err != nil {
		return ledger.Journal{}, err
	}
	return s.ledger.PostTransaction(ctx, ledger.PostRequest{
		IdempotencyKey:   r.IdempotencyKey,
		Kind:             ledger.KindWithdrawalFail,
		Actor:            r.Actor,
		Reason:           reasonFailWithdrawal,
		SettlesJournalID: r.RequestID,
		Legs: []ledger.Leg{
			{Account: holdsRef(owner, currency), Side: ledger.Debit, Amount: amount},
			{Account: walletRef(owner, currency), Side: ledger.Credit, Amount: amount},
		},
	})
}

// Reverse reverses an existing journal by posting its mirror.
func (s *Service) Reverse(ctx context.Context, r ReverseRequest) (ledger.Journal, error) {
	if err := validateReverse(r); err != nil {
		return ledger.Journal{}, err
	}
	original, err := s.ledger.GetJournal(ctx, r.JournalID)
	if err != nil {
		return ledger.Journal{}, err
	}
	if len(original.Legs) == 0 {
		return ledger.Journal{}, fmt.Errorf("%w: original has no legs", ledger.ErrInvalidJournal)
	}
	return s.ledger.PostTransaction(ctx, ledger.PostRequest{
		IdempotencyKey:    r.IdempotencyKey,
		Kind:              ledger.KindReversal,
		Actor:             r.Actor,
		Reason:            reasonReversal,
		ReversesJournalID: r.JournalID,
		Legs:              ledger.FailLegs(original.Legs),
	})
}

// Balances returns a user's available (wallet) and held balances. A missing
// account counts as zero, so a user who never transacted sees zero rather than
// an error.
func (s *Service) Balances(ctx context.Context, owner, currency string) (available, held int64, err error) {
	if strings.TrimSpace(owner) == "" {
		return 0, 0, fmt.Errorf("%w: owner is required", ErrInvalidRequest)
	}
	if err := validateCurrency(currency); err != nil {
		return 0, 0, err
	}
	available, err = s.balanceOrZero(ctx, walletRef(owner, currency))
	if err != nil {
		return 0, 0, err
	}
	held, err = s.balanceOrZero(ctx, holdsRef(owner, currency))
	if err != nil {
		return 0, 0, err
	}
	return available, held, nil
}

// balanceOrZero returns 0 for an account that does not exist.
func (s *Service) balanceOrZero(ctx context.Context, ref ledger.AccountRef) (int64, error) {
	b, err := s.ledger.Balance(ctx, ref)
	if err != nil {
		if errors.Is(err, ledger.ErrAccountNotFound) {
			return 0, nil
		}
		return 0, err
	}
	return b, nil
}

// requestTerms loads a withdrawal request and derives owner, amount and
// currency from its legs.
func (s *Service) requestTerms(ctx context.Context, requestID string) (owner string, amount int64, currency string, err error) {
	request, err := s.ledger.GetJournal(ctx, requestID)
	if err != nil {
		return "", 0, "", err
	}
	if request.Kind != ledger.KindWithdrawalRequest {
		return "", 0, "", fmt.Errorf("%w: journal %s is not a withdrawal request", ledger.ErrInvalidJournal, requestID)
	}
	for _, l := range request.Legs {
		if l.Account.Kind == ledger.KindUserWallet && l.Side == ledger.Debit {
			return l.Account.OwnerRef, l.Amount, l.Account.Currency, nil
		}
	}
	return "", 0, "", fmt.Errorf("%w: request has no wallet debit leg", ledger.ErrInvalidJournal)
}

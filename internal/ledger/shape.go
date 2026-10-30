package ledger

import "fmt"

// ValidateShape checks that the legs match the expected shape for a journal
// kind. It assumes amounts, currencies, sides and account kinds were already
// validated, and focuses on structure: how many legs, which account roles, and
// how the amounts relate. Settlement and fee accounts must use owner "core".
//
// Kinds that depend on a target journal (reversal, finalize, fail) get only a
// structural check here; their exact legs are verified against the target in
// the repository.
func ValidateShape(kind JournalKind, legs []Leg) error {
	switch kind {
	case KindFund:
		return shapeFund(legs)
	case KindTransfer:
		return shapeTransfer(legs)
	case KindFee:
		return shapeFee(legs)
	case KindWithdrawalRequest:
		return shapeWithdrawalRequest(legs)
	case KindWithdrawalFinalize, KindWithdrawalFail:
		if len(legs) != 2 {
			return fmt.Errorf("%w: %s needs exactly two legs", ErrInvalidJournal, kind)
		}
		return nil
	case KindReversal:
		return nil
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidJournal, kind)
	}
}

// shapeFund checks: one Dr settlement:core and one Cr user_wallet, equal amount
// and currency.
func shapeFund(legs []Leg) error {
	if len(legs) != 2 {
		return fmt.Errorf("%w: fund needs exactly two legs", ErrInvalidJournal)
	}
	dr, cr := legs[0], legs[1]
	if dr.Side != Debit || cr.Side != Credit {
		return fmt.Errorf("%w: fund needs a debit then a credit", ErrInvalidJournal)
	}
	if dr.Account.Kind != KindSettlement || dr.Account.OwnerRef != "core" {
		return fmt.Errorf("%w: fund debit must be settlement:core", ErrInvalidJournal)
	}
	if cr.Account.Kind != KindUserWallet {
		return fmt.Errorf("%w: fund credit must be user_wallet", ErrInvalidJournal)
	}
	if dr.Amount != cr.Amount || dr.Account.Currency != cr.Account.Currency {
		return fmt.Errorf("%w: fund legs must match", ErrInvalidJournal)
	}
	return nil
}

// shapeTransfer checks: Dr user_wallet (amount+fee), Cr user_wallet (amount),
// optional Cr fees:core (fee), one currency, and two distinct wallets.
func shapeTransfer(legs []Leg) error {
	if len(legs) != 2 && len(legs) != 3 {
		return fmt.Errorf("%w: transfer needs two or three legs", ErrInvalidJournal)
	}
	dr := legs[0]
	cr := legs[1]
	if dr.Side != Debit || dr.Account.Kind != KindUserWallet {
		return fmt.Errorf("%w: transfer must debit a user_wallet", ErrInvalidJournal)
	}
	if cr.Side != Credit || cr.Account.Kind != KindUserWallet {
		return fmt.Errorf("%w: transfer must credit a user_wallet", ErrInvalidJournal)
	}
	if refKey(dr.Account) == refKey(cr.Account) {
		return fmt.Errorf("%w: transfer wallets must differ", ErrInvalidJournal)
	}
	currency := dr.Account.Currency

	var fee int64
	if len(legs) == 3 {
		f := legs[2]
		if f.Side != Credit || f.Account.Kind != KindFees || f.Account.OwnerRef != "core" {
			return fmt.Errorf("%w: transfer fee leg must credit fees:core", ErrInvalidJournal)
		}
		if f.Account.Currency != currency {
			return fmt.Errorf("%w: transfer legs must share a currency", ErrInvalidJournal)
		}
		fee = f.Amount
	}
	if dr.Amount != cr.Amount+fee {
		return fmt.Errorf("%w: transfer debit must equal amount plus fee", ErrInvalidJournal)
	}
	if cr.Amount <= 0 {
		return fmt.Errorf("%w: transfer amount must be positive", ErrInvalidAmount)
	}
	return nil
}

// shapeFee checks: Dr user_wallet and Cr fees:core for equal amounts.
func shapeFee(legs []Leg) error {
	if len(legs) != 2 {
		return fmt.Errorf("%w: fee needs exactly two legs", ErrInvalidJournal)
	}
	dr, cr := legs[0], legs[1]
	if dr.Side != Debit || dr.Account.Kind != KindUserWallet {
		return fmt.Errorf("%w: fee must debit a user_wallet", ErrInvalidJournal)
	}
	if cr.Side != Credit || cr.Account.Kind != KindFees || cr.Account.OwnerRef != "core" {
		return fmt.Errorf("%w: fee must credit fees:core", ErrInvalidJournal)
	}
	if dr.Amount != cr.Amount || dr.Account.Currency != cr.Account.Currency {
		return fmt.Errorf("%w: fee legs must match", ErrInvalidJournal)
	}
	return nil
}

// shapeWithdrawalRequest checks: Dr user_wallet:X and Cr holds:X, same owner,
// equal amounts.
func shapeWithdrawalRequest(legs []Leg) error {
	if len(legs) != 2 {
		return fmt.Errorf("%w: withdrawal_request needs exactly two legs", ErrInvalidJournal)
	}
	dr, cr := legs[0], legs[1]
	if dr.Side != Debit || dr.Account.Kind != KindUserWallet {
		return fmt.Errorf("%w: withdrawal_request must debit a user_wallet", ErrInvalidJournal)
	}
	if cr.Side != Credit || cr.Account.Kind != KindHolds {
		return fmt.Errorf("%w: withdrawal_request must credit holds", ErrInvalidJournal)
	}
	if dr.Account.OwnerRef != cr.Account.OwnerRef {
		return fmt.Errorf("%w: withdrawal_request legs must share an owner", ErrInvalidJournal)
	}
	if dr.Amount != cr.Amount || dr.Account.Currency != cr.Account.Currency {
		return fmt.Errorf("%w: withdrawal_request legs must match", ErrInvalidJournal)
	}
	return nil
}

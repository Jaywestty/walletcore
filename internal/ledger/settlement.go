package ledger

// FailLegs builds the mirror of a withdrawal request's legs with opposite
// sides. Used to validate a withdrawal_fail.
func FailLegs(requestLegs []Leg) []Leg {
	out := make([]Leg, len(requestLegs))
	for i, l := range requestLegs {
		l.Side = opposite(l.Side)
		out[i] = l
	}
	return out
}

// FinalizeMatches reports whether the request legs describe exactly the
// expected finalize: a debit on the request owner's holds account and a credit
// on settlement:core, both for the request's amount and currency.
//
// The owner, amount and currency are derived from the withdrawal request's own
// legs so the caller never has to restate them.
func FinalizeMatches(requestLegs, finalizeLegs []Leg) bool {
	owner, amount, currency, ok := requestOwnerAmountCurrency(requestLegs)
	if !ok {
		return false
	}
	if len(finalizeLegs) != 2 {
		return false
	}

	holds := AccountRef{Kind: KindHolds, OwnerRef: owner, Currency: currency}
	settlement := AccountRef{Kind: KindSettlement, OwnerRef: "core", Currency: currency}

	var sawDebitHolds, sawCreditSettlement bool
	for _, l := range finalizeLegs {
		switch {
		case l.Account == holds && l.Side == Debit && l.Amount == amount:
			sawDebitHolds = true
		case l.Account == settlement && l.Side == Credit && l.Amount == amount:
			sawCreditSettlement = true
		}
	}
	return sawDebitHolds && sawCreditSettlement
}

// requestOwnerAmountCurrency extracts the owner, amount and currency from a
// withdrawal_request, which is a debit on user_wallet:owner and a credit on
// holds:owner for the same amount and currency.
func requestOwnerAmountCurrency(requestLegs []Leg) (owner string, amount int64, currency string, ok bool) {
	if len(requestLegs) != 2 {
		return "", 0, "", false
	}
	var debit, credit *Leg
	for i := range requestLegs {
		switch requestLegs[i].Side {
		case Debit:
			debit = &requestLegs[i]
		case Credit:
			credit = &requestLegs[i]
		}
	}
	if debit == nil || credit == nil {
		return "", 0, "", false
	}
	if debit.Account.Kind != KindUserWallet || credit.Account.Kind != KindHolds {
		return "", 0, "", false
	}
	if debit.Account.OwnerRef != credit.Account.OwnerRef {
		return "", 0, "", false
	}
	if debit.Account.Currency != credit.Account.Currency || debit.Amount != credit.Amount {
		return "", 0, "", false
	}
	return debit.Account.OwnerRef, debit.Amount, debit.Account.Currency, true
}

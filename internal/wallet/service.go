// Package wallet provides user-facing wallet operations built on the ledger
// core: fund, transfer, fee, withdrawal request, withdrawal settlement and
// reversal. It composes balanced journals; it never writes balances itself.
package wallet

import (
	"context"

	"github.com/Jaywestty/walletcore/internal/ledger"
)

// CoreAccount is the fixed owner reference for platform accounts (settlement
// and fees).
const CoreAccount = "core"

// Ledger is the subset of the ledger the wallet service consumes. It is
// defined here, at the consumer, so the service can be tested with a fake and
// never depends on pgx.
type Ledger interface {
	PostTransaction(ctx context.Context, req ledger.PostRequest) (ledger.Journal, error)
	Balance(ctx context.Context, ref ledger.AccountRef) (int64, error)
	GetJournal(ctx context.Context, id string) (ledger.Journal, error)
}

// Service runs wallet operations.
type Service struct {
	ledger Ledger
}

// New builds a wallet Service over a ledger implementation.
func New(l Ledger) *Service {
	return &Service{ledger: l}
}

func settlementRef(currency string) ledger.AccountRef {
	return ledger.AccountRef{Kind: ledger.KindSettlement, OwnerRef: CoreAccount, Currency: currency}
}

func feesRef(currency string) ledger.AccountRef {
	return ledger.AccountRef{Kind: ledger.KindFees, OwnerRef: CoreAccount, Currency: currency}
}

func walletRef(owner, currency string) ledger.AccountRef {
	return ledger.AccountRef{Kind: ledger.KindUserWallet, OwnerRef: owner, Currency: currency}
}

func holdsRef(owner, currency string) ledger.AccountRef {
	return ledger.AccountRef{Kind: ledger.KindHolds, OwnerRef: owner, Currency: currency}
}

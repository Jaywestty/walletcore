package httpapi

import (
	"context"

	"github.com/Jaywestty/walletcore/internal/ledger"
	"github.com/Jaywestty/walletcore/internal/wallet"
)

// WalletService is the subset of the wallet service the HTTP layer needs. It is
// declared here, at the consumer, so httpapi does not import pgx.
type WalletService interface {
	Fund(ctx context.Context, r wallet.FundRequest) (ledger.Journal, error)
	Transfer(ctx context.Context, r wallet.TransferRequest) (ledger.Journal, error)
	ChargeFee(ctx context.Context, r wallet.ChargeFeeRequest) (ledger.Journal, error)
	RequestWithdrawal(ctx context.Context, r wallet.WithdrawRequest) (ledger.Journal, error)
	FinalizeWithdrawal(ctx context.Context, r wallet.SettleWithdrawalRequest) (ledger.Journal, error)
	FailWithdrawal(ctx context.Context, r wallet.SettleWithdrawalRequest) (ledger.Journal, error)
	Reverse(ctx context.Context, r wallet.ReverseRequest) (ledger.Journal, error)
	Balances(ctx context.Context, owner, currency string) (available, held int64, err error)
	Statement(ctx context.Context, owner, currency string, afterSeq int64, limit int) (ledger.StatementPage, error)
	Journal(ctx context.Context, id string) (ledger.Journal, error)
}

var _ WalletService = (*wallet.Service)(nil)

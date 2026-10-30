// Package ledger is the double-entry core: domain types, validation, the
// posting service and the pgx repository.
package ledger

import (
	"time"

	"github.com/Jaywestty/walletcore/internal/money"
)

// JournalKind is the business meaning of a journal. The set is fixed and
// mirrored by a CHECK constraint in the schema.
type JournalKind string

const (
	KindFund               JournalKind = "fund"
	KindTransfer           JournalKind = "transfer"
	KindWithdrawalRequest  JournalKind = "withdrawal_request"
	KindWithdrawalFinalize JournalKind = "withdrawal_finalize"
	KindWithdrawalFail     JournalKind = "withdrawal_fail"
	KindFee                JournalKind = "fee"
	KindReversal           JournalKind = "reversal"
)

// Valid reports whether k is one of the allowed journal kinds.
func (k JournalKind) Valid() bool {
	switch k {
	case KindFund, KindTransfer, KindWithdrawalRequest, KindWithdrawalFinalize,
		KindWithdrawalFail, KindFee, KindReversal:
		return true
	default:
		return false
	}
}

// Side is a debit or a credit.
type Side string

const (
	Debit  Side = "Dr"
	Credit Side = "Cr"
)

// Valid reports whether s is Dr or Cr.
func (s Side) Valid() bool { return s == Debit || s == Credit }

// Action is the AccountKind, the classification of an account. It determines
// the normal side and whether a negative balance is allowed.
type AccountKind string

const (
	KindUserWallet AccountKind = "user_wallet"
	KindSettlement AccountKind = "settlement"
	KindFees       AccountKind = "fees"
	KindHolds      AccountKind = "holds"
)

// NormalSide returns the side on which a positive balance is held for the kind.
//
// Sign convention, used everywhere: the cached balance is stored normal-side
// positive for every kind. For user_wallet, holds and fees (credit normal) the
// balance is credits minus debits. For settlement (debit normal) the balance is
// debits minus credits. A positive balance therefore always means the account
// holds value on its normal side.
func (k AccountKind) NormalSide() Side {
	if k == KindSettlement {
		return Debit
	}
	return Credit
}

// AllowsNegative reports whether the kind may hold a negative normal-side
// balance. Only settlement may, because it represents money held outside the
// system.
func (k AccountKind) AllowsNegative() bool { return k == KindSettlement }

// AccountRef identifies an account without exposing its uuid. Callers address
// accounts by kind, owner and currency, and the repository resolves the uuid.
type AccountRef struct {
	Kind     AccountKind
	OwnerRef string
	Currency string
}

// Leg is one entry to post: an account reference, a side and an amount. The
// amount must be positive; the currency must match the referenced account.
type Leg struct {
	Account AccountRef
	Side    Side
	Amount  int64
}

// Account is a persisted account.
type Account struct {
	ID       string
	Kind     AccountKind
	OwnerRef string
	Currency string
	Status   string
}

// Journal is a persisted journal header with its entries and the resulting
// balances for the accounts it touched.
type Journal struct {
	ID                string
	Kind              JournalKind
	IdempotencyKey    string
	Fingerprint       string
	Actor             string
	Reason            string
	Metadata          map[string]string
	ReversesJournalID string
	SettlesJournalID  string
	OccurredAt        time.Time
	PostedAt          time.Time
	Entries           []Entry
	Balances          []Balance
	// Legs expresses the journal's entries as account references, resolved from
	// the stored accounts. Populated by GetJournal and by replays.
	Legs []Leg

	// Replayed is true when this result came from an existing journal that was
	// returned for an idempotent retry rather than posted now. Balances on a
	// replay are the current values, not the values at original posting time.
	Replayed bool
}

// Entry is one persisted leg.
type Entry struct {
	ID        string
	JournalID string
	AccountID string
	Side      Side
	Amount    int64
	Currency  string
	EntrySeq  int64
}

// Balance is the cached balance for one account, held normal-side positive.
type Balance struct {
	AccountID string
	Account   AccountRef
	Amount    int64
	Currency  string
}

// AsMoney converts a balance amount to Money for display or tests.
func (b Balance) AsMoney() (money.Money, error) {
	return money.New(b.Amount, b.Currency)
}

// PostRequest is a single request to post a balanced journal.
type PostRequest struct {
	IdempotencyKey    string
	Kind              JournalKind
	Actor             string
	Reason            string
	Metadata          map[string]string
	OccurredAt        time.Time
	ReversesJournalID string
	// SettlesJournalID links a withdrawal_finalize or withdrawal_fail journal
	// to the withdrawal_request it resolves. Empty for every other kind.
	SettlesJournalID string
	Legs             []Leg
}

// SettlesKind reports whether a journal kind resolves a withdrawal request.
func (k JournalKind) SettlesKind() bool {
	return k == KindWithdrawalFinalize || k == KindWithdrawalFail
}

// ReversibleKind reports whether a journal kind may be reversed. Withdrawal
// journals are resolved by finalize or fail, never reversed.
func (k JournalKind) ReversibleKind() bool {
	switch k {
	case KindFund, KindTransfer, KindFee:
		return true
	default:
		return false
	}
}

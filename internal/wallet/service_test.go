package wallet

import (
	"context"
	"errors"
	"testing"

	"github.com/Jaywestty/walletcore/internal/ledger"
)

// fakeLedger records posted requests and serves canned journals, so the
// service can be tested without a database.
type fakeLedger struct {
	posted   []ledger.PostRequest
	balances map[string]int64
	journals map[string]ledger.Journal
	postErr  error
}

func newFake() *fakeLedger {
	return &fakeLedger{
		balances: map[string]int64{},
		journals: map[string]ledger.Journal{},
	}
}

func (f *fakeLedger) PostTransaction(_ context.Context, req ledger.PostRequest) (ledger.Journal, error) {
	if f.postErr != nil {
		return ledger.Journal{}, f.postErr
	}
	f.posted = append(f.posted, req)
	return ledger.Journal{ID: "j", Kind: req.Kind, Legs: req.Legs}, nil
}

func (f *fakeLedger) Balance(_ context.Context, ref ledger.AccountRef) (int64, error) {
	v, ok := f.balances[ref.OwnerRef+"|"+string(ref.Kind)+"|"+ref.Currency]
	if !ok {
		return 0, ledger.ErrAccountNotFound
	}
	return v, nil
}

func (f *fakeLedger) GetJournal(_ context.Context, id string) (ledger.Journal, error) {
	j, ok := f.journals[id]
	if !ok {
		return ledger.Journal{}, ledger.ErrJournalNotFound
	}
	return j, nil
}

func lastPosted(t *testing.T, f *fakeLedger) ledger.PostRequest {
	t.Helper()
	if len(f.posted) == 0 {
		t.Fatal("nothing was posted")
	}
	return f.posted[len(f.posted)-1]
}

func leg(account ledger.AccountRef, side ledger.Side, amount int64) ledger.Leg {
	return ledger.Leg{Account: account, Side: side, Amount: amount}
}

func TestFundLegs(t *testing.T) {
	f := newFake()
	s := New(f)
	_, err := s.Fund(context.Background(), FundRequest{
		IdempotencyKey: "k", Actor: "system", Owner: "u1", Amount: 100, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req := lastPosted(t, f)
	requireLegs(t, req.Legs, []ledger.Leg{
		leg(ledger.AccountRef{Kind: ledger.KindSettlement, OwnerRef: "core", Currency: "USD"}, ledger.Debit, 100),
		leg(ledger.AccountRef{Kind: ledger.KindUserWallet, OwnerRef: "u1", Currency: "USD"}, ledger.Credit, 100),
	})
	if req.Kind != ledger.KindFund {
		t.Fatalf("kind = %s", req.Kind)
	}
}

func TestTransferLegs(t *testing.T) {
	tests := []struct {
		name string
		fee  int64
		want []ledger.Leg
	}{
		{
			name: "no fee",
			fee:  0,
			want: []ledger.Leg{
				leg(ledger.AccountRef{Kind: ledger.KindUserWallet, OwnerRef: "u1", Currency: "USD"}, ledger.Debit, 100),
				leg(ledger.AccountRef{Kind: ledger.KindUserWallet, OwnerRef: "u2", Currency: "USD"}, ledger.Credit, 100),
			},
		},
		{
			name: "with fee",
			fee:  5,
			want: []ledger.Leg{
				leg(ledger.AccountRef{Kind: ledger.KindUserWallet, OwnerRef: "u1", Currency: "USD"}, ledger.Debit, 105),
				leg(ledger.AccountRef{Kind: ledger.KindUserWallet, OwnerRef: "u2", Currency: "USD"}, ledger.Credit, 100),
				leg(ledger.AccountRef{Kind: ledger.KindFees, OwnerRef: "core", Currency: "USD"}, ledger.Credit, 5),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			s := New(f)
			_, err := s.Transfer(context.Background(), TransferRequest{
				IdempotencyKey: "k", Actor: "u1", From: "u1", To: "u2", Amount: 100, Fee: tc.fee, Currency: "USD",
			})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			req := lastPosted(t, f)
			requireLegs(t, req.Legs, tc.want)
			if req.Kind != ledger.KindTransfer {
				t.Fatalf("kind = %s", req.Kind)
			}
		})
	}
}

func TestChargeFeeLegs(t *testing.T) {
	f := newFake()
	s := New(f)
	_, err := s.ChargeFee(context.Background(), ChargeFeeRequest{
		IdempotencyKey: "k", Actor: "system", Owner: "u1", Amount: 7, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req := lastPosted(t, f)
	requireLegs(t, req.Legs, []ledger.Leg{
		leg(ledger.AccountRef{Kind: ledger.KindUserWallet, OwnerRef: "u1", Currency: "USD"}, ledger.Debit, 7),
		leg(ledger.AccountRef{Kind: ledger.KindFees, OwnerRef: "core", Currency: "USD"}, ledger.Credit, 7),
	})
}

func TestRequestWithdrawalLegs(t *testing.T) {
	f := newFake()
	s := New(f)
	_, err := s.RequestWithdrawal(context.Background(), WithdrawRequest{
		IdempotencyKey: "k", Actor: "u1", Owner: "u1", Amount: 40, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req := lastPosted(t, f)
	requireLegs(t, req.Legs, []ledger.Leg{
		leg(ledger.AccountRef{Kind: ledger.KindUserWallet, OwnerRef: "u1", Currency: "USD"}, ledger.Debit, 40),
		leg(ledger.AccountRef{Kind: ledger.KindHolds, OwnerRef: "u1", Currency: "USD"}, ledger.Credit, 40),
	})
}

// requestJournal builds an in-memory withdrawal_request as GetJournal would
// return it.
func requestJournal(id, owner, currency string, amount int64) ledger.Journal {
	return ledger.Journal{
		ID:   id,
		Kind: ledger.KindWithdrawalRequest,
		Legs: []ledger.Leg{
			leg(ledger.AccountRef{Kind: ledger.KindUserWallet, OwnerRef: owner, Currency: currency}, ledger.Debit, amount),
			leg(ledger.AccountRef{Kind: ledger.KindHolds, OwnerRef: owner, Currency: currency}, ledger.Credit, amount),
		},
	}
}

func TestFinalizeDerivesFromRequest(t *testing.T) {
	f := newFake()
	f.journals["11111111-1111-1111-1111-111111111111"] = requestJournal("11111111-1111-1111-1111-111111111111", "u1", "EUR", 250)
	s := New(f)

	_, err := s.FinalizeWithdrawal(context.Background(), SettleWithdrawalRequest{
		IdempotencyKey: "fin", Actor: "system", RequestID: "11111111-1111-1111-1111-111111111111",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req := lastPosted(t, f)
	if req.SettlesJournalID != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("settles = %q", req.SettlesJournalID)
	}
	requireLegs(t, req.Legs, []ledger.Leg{
		leg(ledger.AccountRef{Kind: ledger.KindHolds, OwnerRef: "u1", Currency: "EUR"}, ledger.Debit, 250),
		leg(ledger.AccountRef{Kind: ledger.KindSettlement, OwnerRef: "core", Currency: "EUR"}, ledger.Credit, 250),
	})
}

func TestFailDerivesFromRequest(t *testing.T) {
	f := newFake()
	f.journals["11111111-1111-1111-1111-111111111111"] = requestJournal("11111111-1111-1111-1111-111111111111", "u1", "EUR", 250)
	s := New(f)

	_, err := s.FailWithdrawal(context.Background(), SettleWithdrawalRequest{
		IdempotencyKey: "fail", Actor: "system", RequestID: "11111111-1111-1111-1111-111111111111",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req := lastPosted(t, f)
	requireLegs(t, req.Legs, []ledger.Leg{
		leg(ledger.AccountRef{Kind: ledger.KindHolds, OwnerRef: "u1", Currency: "EUR"}, ledger.Debit, 250),
		leg(ledger.AccountRef{Kind: ledger.KindUserWallet, OwnerRef: "u1", Currency: "EUR"}, ledger.Credit, 250),
	})
}

func TestSettleNonWithdrawalRejected(t *testing.T) {
	f := newFake()
	f.journals["22222222-2222-2222-2222-222222222222"] = ledger.Journal{ID: "22222222-2222-2222-2222-222222222222", Kind: ledger.KindFund}
	s := New(f)
	_, err := s.FinalizeWithdrawal(context.Background(), SettleWithdrawalRequest{
		IdempotencyKey: "fin", Actor: "system", RequestID: "22222222-2222-2222-2222-222222222222",
	})
	if !errors.Is(err, ledger.ErrInvalidJournal) {
		t.Fatalf("want ErrInvalidJournal, got %v", err)
	}
}

func TestReverseBuildsMirror(t *testing.T) {
	f := newFake()
	f.journals["33333333-3333-3333-3333-333333333333"] = ledger.Journal{
		ID:   "33333333-3333-3333-3333-333333333333",
		Kind: ledger.KindFund,
		Legs: []ledger.Leg{
			leg(ledger.AccountRef{Kind: ledger.KindSettlement, OwnerRef: "core", Currency: "USD"}, ledger.Debit, 100),
			leg(ledger.AccountRef{Kind: ledger.KindUserWallet, OwnerRef: "u1", Currency: "USD"}, ledger.Credit, 100),
		},
	}
	s := New(f)
	_, err := s.Reverse(context.Background(), ReverseRequest{
		IdempotencyKey: "rev", Actor: "system", JournalID: "33333333-3333-3333-3333-333333333333",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req := lastPosted(t, f)
	if req.ReversesJournalID != "33333333-3333-3333-3333-333333333333" {
		t.Fatalf("reverses = %q", req.ReversesJournalID)
	}
	requireLegs(t, req.Legs, []ledger.Leg{
		leg(ledger.AccountRef{Kind: ledger.KindSettlement, OwnerRef: "core", Currency: "USD"}, ledger.Credit, 100),
		leg(ledger.AccountRef{Kind: ledger.KindUserWallet, OwnerRef: "u1", Currency: "USD"}, ledger.Debit, 100),
	})
}

func TestBalancesMissingAccountIsZero(t *testing.T) {
	f := newFake()
	s := New(f)
	avail, held, err := s.Balances(context.Background(), "u1", "USD")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if avail != 0 || held != 0 {
		t.Fatalf("balances = %d/%d, want 0/0", avail, held)
	}

	f.balances["u1|user_wallet|USD"] = 500
	f.balances["u1|holds|USD"] = 40
	avail, held, err = s.Balances(context.Background(), "u1", "USD")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if avail != 500 || held != 40 {
		t.Fatalf("balances = %d/%d, want 500/40", avail, held)
	}
}

func requireLegs(t *testing.T, got, want []ledger.Leg) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("leg count = %d, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("leg %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

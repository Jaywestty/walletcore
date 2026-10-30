package ledger

import "testing"

func requestLegsFor(owner, currency string, amount int64) []Leg {
	return []Leg{
		{Account: AccountRef{Kind: KindUserWallet, OwnerRef: owner, Currency: currency}, Side: Debit, Amount: amount},
		{Account: AccountRef{Kind: KindHolds, OwnerRef: owner, Currency: currency}, Side: Credit, Amount: amount},
	}
}

func TestFailLegs(t *testing.T) {
	req := requestLegsFor("u1", "USD", 100)
	got := FailLegs(req)
	want := []Leg{
		{Account: AccountRef{Kind: KindUserWallet, OwnerRef: "u1", Currency: "USD"}, Side: Credit, Amount: 100},
		{Account: AccountRef{Kind: KindHolds, OwnerRef: "u1", Currency: "USD"}, Side: Debit, Amount: 100},
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("leg %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if !MirrorMatches(req, got) {
		t.Fatal("fail legs must mirror the request")
	}
}

func TestFinalizeMatches(t *testing.T) {
	req := requestLegsFor("u1", "USD", 250)

	good := []Leg{
		{Account: AccountRef{Kind: KindHolds, OwnerRef: "u1", Currency: "USD"}, Side: Debit, Amount: 250},
		{Account: AccountRef{Kind: KindSettlement, OwnerRef: "core", Currency: "USD"}, Side: Credit, Amount: 250},
	}

	tests := []struct {
		name string
		legs []Leg
		want bool
	}{
		{"exact", good, true},
		{"reversed order", []Leg{good[1], good[0]}, true},
		{"wrong amount", []Leg{
			{Account: good[0].Account, Side: Debit, Amount: 249},
			{Account: good[1].Account, Side: Credit, Amount: 249},
		}, false},
		{"wrong side", []Leg{
			{Account: good[0].Account, Side: Credit, Amount: 250},
			{Account: good[1].Account, Side: Debit, Amount: 250},
		}, false},
		{"wrong settlement owner", []Leg{
			{Account: good[0].Account, Side: Debit, Amount: 250},
			{Account: AccountRef{Kind: KindSettlement, OwnerRef: "x", Currency: "USD"}, Side: Credit, Amount: 250},
		}, false},
		{"wrong holds owner", []Leg{
			{Account: AccountRef{Kind: KindHolds, OwnerRef: "u2", Currency: "USD"}, Side: Debit, Amount: 250},
			{Account: good[1].Account, Side: Credit, Amount: 250},
		}, false},
		{"three legs", append(good, Leg{Account: good[0].Account, Side: Debit, Amount: 1}), false},
		{"one leg", good[:1], false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FinalizeMatches(req, tc.legs); got != tc.want {
				t.Fatalf("FinalizeMatches = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequestOwnerAmountCurrency(t *testing.T) {
	if _, _, _, ok := requestOwnerAmountCurrency(requestLegsFor("u1", "USD", 100)); !ok {
		t.Fatal("valid request should parse")
	}
	// Mismatched owners.
	bad := []Leg{
		{Account: AccountRef{Kind: KindUserWallet, OwnerRef: "u1", Currency: "USD"}, Side: Debit, Amount: 100},
		{Account: AccountRef{Kind: KindHolds, OwnerRef: "u2", Currency: "USD"}, Side: Credit, Amount: 100},
	}
	if _, _, _, ok := requestOwnerAmountCurrency(bad); ok {
		t.Fatal("mismatched owners must not parse")
	}
	// Wrong kinds.
	wrongKinds := []Leg{
		{Account: AccountRef{Kind: KindSettlement, OwnerRef: "core", Currency: "USD"}, Side: Debit, Amount: 100},
		{Account: AccountRef{Kind: KindHolds, OwnerRef: "core", Currency: "USD"}, Side: Credit, Amount: 100},
	}
	if _, _, _, ok := requestOwnerAmountCurrency(wrongKinds); ok {
		t.Fatal("wrong kinds must not parse")
	}
}

func TestReversibleKind(t *testing.T) {
	reversible := []JournalKind{KindFund, KindTransfer, KindFee}
	for _, k := range reversible {
		if !k.ReversibleKind() {
			t.Errorf("%s must be reversible", k)
		}
	}
	notReversible := []JournalKind{KindWithdrawalRequest, KindWithdrawalFinalize, KindWithdrawalFail, KindReversal}
	for _, k := range notReversible {
		if k.ReversibleKind() {
			t.Errorf("%s must not be reversible", k)
		}
	}
}

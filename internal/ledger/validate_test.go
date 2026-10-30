package ledger

import (
	"errors"
	"strings"
	"testing"
)

func goodLegs() []Leg {
	return []Leg{
		{Account: AccountRef{Kind: KindSettlement, OwnerRef: "core", Currency: "USD"}, Side: Debit, Amount: 100},
		{Account: AccountRef{Kind: KindUserWallet, OwnerRef: "u1", Currency: "USD"}, Side: Credit, Amount: 100},
	}
}

func baseReq() PostRequest {
	return PostRequest{
		IdempotencyKey: "k1",
		Kind:           KindFund,
		Actor:          "system",
		Reason:         "test",
		Legs:           goodLegs(),
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*PostRequest)
		wantErr error
	}{
		{"valid", func(*PostRequest) {}, nil},
		{"empty key", func(r *PostRequest) { r.IdempotencyKey = "" }, ErrInvalidJournal},
		{"long key", func(r *PostRequest) { r.IdempotencyKey = string(make([]byte, 201)) }, ErrInvalidJournal},
		{"unknown kind", func(r *PostRequest) { r.Kind = "nope" }, ErrInvalidJournal},
		{"empty actor", func(r *PostRequest) { r.Actor = "" }, ErrInvalidJournal},
		{"empty reason", func(r *PostRequest) { r.Reason = "" }, ErrInvalidJournal},
		{"one leg", func(r *PostRequest) { r.Legs = r.Legs[:1] }, ErrUnbalancedJournal},
		{"unbalanced", func(r *PostRequest) { r.Legs[1].Amount = 90 }, ErrUnbalancedJournal},
		{"zero amount", func(r *PostRequest) { r.Legs[0].Amount = 0; r.Legs[1].Amount = 0 }, ErrInvalidAmount},
		{"negative amount", func(r *PostRequest) { r.Legs[0].Amount = -5 }, ErrInvalidAmount},
		{"bad side", func(r *PostRequest) { r.Legs[0].Side = "X" }, ErrInvalidJournal},
		{"bad currency", func(r *PostRequest) {
			r.Legs[0].Account.Currency = "usd"
			r.Legs[1].Account.Currency = "usd"
		}, ErrInvalidJournal},
		{"empty owner", func(r *PostRequest) { r.Legs[0].Account.OwnerRef = "" }, ErrInvalidJournal},
		{"bad account kind", func(r *PostRequest) { r.Legs[0].Account.Kind = "nope" }, ErrInvalidJournal},
		{"reversal without target", func(r *PostRequest) { r.Kind = KindReversal }, ErrInvalidJournal},
		{"non-reversal with target", func(r *PostRequest) { r.ReversesJournalID = "j" }, ErrInvalidJournal},
		{"finalize without target", func(r *PostRequest) { r.Kind = KindWithdrawalFinalize }, ErrInvalidJournal},
		{"fail without target", func(r *PostRequest) { r.Kind = KindWithdrawalFail }, ErrInvalidJournal},
		{"fund with settle target", func(r *PostRequest) { r.SettlesJournalID = "j" }, ErrInvalidJournal},
		{"both reverse and settle", func(r *PostRequest) {
			r.Kind = KindReversal
			r.ReversesJournalID = "a"
			r.SettlesJournalID = "b"
		}, ErrInvalidJournal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := baseReq()
			tc.mutate(&req)
			err := Validate(req)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestIdempotencyKeyLengthByRunes(t *testing.T) {
	// 200 multi-byte characters is 400 bytes but 200 runes, so it is valid.
	// The database counts characters with char_length, which also counts runes.
	multibyte := strings.Repeat("é", 200)
	if len(multibyte) != 400 {
		t.Fatalf("test setup: expected 400 bytes, got %d", len(multibyte))
	}
	req := baseReq()
	req.IdempotencyKey = multibyte
	if err := Validate(req); err != nil {
		t.Fatalf("200 runes (400 bytes) should be valid, got %v", err)
	}

	// 201 characters is too long.
	req.IdempotencyKey = strings.Repeat("é", 201)
	if err := Validate(req); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("201 runes should be rejected, got %v", err)
	}
}

func TestValidateTooManyLegs(t *testing.T) {
	req := baseReq()
	var legs []Leg
	for i := 0; i < 21; i++ {
		legs = append(legs, Leg{Account: AccountRef{Kind: KindUserWallet, OwnerRef: "x", Currency: "USD"}, Side: Credit, Amount: 1})
	}
	req.Legs = legs
	if err := Validate(req); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("err = %v, want ErrInvalidJournal", err)
	}
}

func TestValidateBalancesPerCurrency(t *testing.T) {
	// Two currencies each balanced independently are valid, even though the
	// total across all legs is not meaningful.
	req := baseReq()
	req.Legs = []Leg{
		{Account: AccountRef{Kind: KindSettlement, OwnerRef: "core", Currency: "USD"}, Side: Debit, Amount: 100},
		{Account: AccountRef{Kind: KindUserWallet, OwnerRef: "u1", Currency: "USD"}, Side: Credit, Amount: 100},
		{Account: AccountRef{Kind: KindSettlement, OwnerRef: "core", Currency: "EUR"}, Side: Debit, Amount: 50},
		{Account: AccountRef{Kind: KindUserWallet, OwnerRef: "u1", Currency: "EUR"}, Side: Credit, Amount: 50},
	}
	if err := Validate(req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMirrorMatches(t *testing.T) {
	original := goodLegs()
	mirror := []Leg{
		{Account: AccountRef{Kind: KindUserWallet, OwnerRef: "u1", Currency: "USD"}, Side: Debit, Amount: 100},
		{Account: AccountRef{Kind: KindSettlement, OwnerRef: "core", Currency: "USD"}, Side: Credit, Amount: 100},
	}
	if !MirrorMatches(original, mirror) {
		t.Fatal("expected mirror to match (order independent)")
	}

	sameSides := []Leg{
		{Account: AccountRef{Kind: KindSettlement, OwnerRef: "core", Currency: "USD"}, Side: Debit, Amount: 100},
		{Account: AccountRef{Kind: KindUserWallet, OwnerRef: "u1", Currency: "USD"}, Side: Credit, Amount: 100},
	}
	if MirrorMatches(original, sameSides) {
		t.Fatal("same sides must not mirror")
	}

	wrongAmount := []Leg{
		{Account: AccountRef{Kind: KindUserWallet, OwnerRef: "u1", Currency: "USD"}, Side: Debit, Amount: 99},
		{Account: AccountRef{Kind: KindSettlement, OwnerRef: "core", Currency: "USD"}, Side: Credit, Amount: 99},
	}
	if MirrorMatches(original, wrongAmount) {
		t.Fatal("different amount must not mirror")
	}

	if MirrorMatches(original, mirror[:1]) {
		t.Fatal("different length must not mirror")
	}
}

func TestFingerprintOrderIndependent(t *testing.T) {
	a := baseReq()
	b := baseReq()
	// Reverse leg order.
	b.Legs = []Leg{a.Legs[1], a.Legs[0]}

	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("fingerprint must not depend on leg order")
	}
}

func TestFingerprintSensitiveToContent(t *testing.T) {
	a := baseReq()
	fa := Fingerprint(a)

	changes := []func(*PostRequest){
		func(r *PostRequest) { r.IdempotencyKey = "k2" },
		func(r *PostRequest) { r.Actor = "other" },
		func(r *PostRequest) { r.Reason = "other" },
		func(r *PostRequest) { r.Legs[1].Amount = 99; r.Legs[0].Amount = 99 },
	}
	for i, change := range changes {
		b := baseReq()
		change(&b)
		if Fingerprint(b) == fa {
			t.Fatalf("change %d did not alter the fingerprint", i)
		}
	}
}

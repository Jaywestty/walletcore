package ledger

import "testing"

func fingerprintReq() PostRequest {
	return PostRequest{
		IdempotencyKey: "key-1",
		Kind:           KindTransfer,
		Actor:          "u1",
		Reason:         "transfer",
		Legs: []Leg{
			{Account: AccountRef{Kind: KindUserWallet, OwnerRef: "u1", Currency: "USD"}, Side: Debit, Amount: 100},
			{Account: AccountRef{Kind: KindUserWallet, OwnerRef: "u2", Currency: "USD"}, Side: Credit, Amount: 100},
		},
	}
}

func TestFingerprintLegOrderIndependent(t *testing.T) {
	a := fingerprintReq()
	b := fingerprintReq()
	b.Legs = []Leg{a.Legs[1], a.Legs[0]}
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("leg order must not change the fingerprint")
	}
}

func TestFingerprintNewlineInjection(t *testing.T) {
	// A naive newline-joined encoding would let a value containing
	// "\nactor=..." forge a different field. JSON encoding must distinguish
	// these two requests.
	a := fingerprintReq()
	a.Actor = "x\nreason=y"
	a.Reason = "r"

	b := fingerprintReq()
	b.Actor = "x"
	b.Reason = "y\nreason=r"

	if Fingerprint(a) == Fingerprint(b) {
		t.Fatal("embedded newline plus forged field name must change the fingerprint")
	}
}

func TestFingerprintEachFieldMatters(t *testing.T) {
	base := fingerprintReq()
	baseFP := Fingerprint(base)

	changes := map[string]func(*PostRequest){
		"kind":     func(r *PostRequest) { r.Kind = KindFund },
		"actor":    func(r *PostRequest) { r.Actor = "someone" },
		"reason":   func(r *PostRequest) { r.Reason = "other" },
		"reversal": func(r *PostRequest) { r.ReversesJournalID = "j2" },
		"key":      func(r *PostRequest) { r.IdempotencyKey = "key-2" },
		"leg amount": func(r *PostRequest) {
			r.Legs[0].Amount = 101
			r.Legs[1].Amount = 101
		},
		"leg side": func(r *PostRequest) {
			r.Legs[0].Side = Credit
			r.Legs[1].Side = Debit
		},
		"leg owner": func(r *PostRequest) { r.Legs[0].Account.OwnerRef = "u9" },
		"leg currency": func(r *PostRequest) {
			r.Legs[0].Account.Currency = "EUR"
			r.Legs[1].Account.Currency = "EUR"
		},
		"leg kind": func(r *PostRequest) { r.Legs[0].Account.Kind = KindHolds },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			r := fingerprintReq()
			change(&r)
			if Fingerprint(r) == baseFP {
				t.Fatalf("changing %s must change the fingerprint", name)
			}
		})
	}
}

func TestFingerprintIgnoresMetadataAndOccurredAt(t *testing.T) {
	a := fingerprintReq()
	a.Metadata = map[string]string{"note": "a"}
	b := fingerprintReq()
	b.Metadata = map[string]string{"note": "b"}
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatal("metadata must not change the fingerprint")
	}
}

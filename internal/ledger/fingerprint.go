package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// canonicalLeg is the stable, orderable encoding of one leg used for
// fingerprinting. A slice of structs, not a map, so encoding is deterministic.
type canonicalLeg struct {
	Kind     string `json:"kind"`
	Owner    string `json:"owner"`
	Currency string `json:"currency"`
	Side     string `json:"side"`
	Amount   int64  `json:"amount"`
}

// canonicalRequest is the fingerprint payload. JSON encoding gives an
// unambiguous, collision-safe serialization: field boundaries are explicit, so
// a value containing an embedded quote, newline or forged field name cannot be
// confused with a different field.
type canonicalRequest struct {
	Kind              string         `json:"kind"`
	Actor             string         `json:"actor"`
	Reason            string         `json:"reason"`
	ReversesJournalID string         `json:"reverses_journal_id"`
	IdempotencyKey    string         `json:"idempotency_key"`
	Legs              []canonicalLeg `json:"legs"`
}

// Fingerprint returns a stable SHA-256 hex digest of the request's identity.
//
// The payload is the journal kind, actor, reason, reversal target, idempotency
// key and the legs sorted by a fixed key. Metadata and occurrence time are not
// part of the identity, so they may differ on a retry without changing the
// fingerprint. The same logical request yields the same fingerprint regardless
// of the order the caller listed its legs in.
func Fingerprint(req PostRequest) string {
	legs := make([]canonicalLeg, len(req.Legs))
	for i, l := range req.Legs {
		legs[i] = canonicalLeg{
			Kind:     string(l.Account.Kind),
			Owner:    l.Account.OwnerRef,
			Currency: l.Account.Currency,
			Side:     string(l.Side),
			Amount:   l.Amount,
		}
	}
	sort.Slice(legs, func(i, j int) bool {
		return canonicalLegLess(legs[i], legs[j])
	})

	payload := canonicalRequest{
		Kind:              string(req.Kind),
		Actor:             req.Actor,
		Reason:            req.Reason,
		ReversesJournalID: req.ReversesJournalID,
		IdempotencyKey:    req.IdempotencyKey,
		Legs:              legs,
	}

	// json.Marshal on a struct with exported fields cannot fail.
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func canonicalLegLess(a, b canonicalLeg) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	if a.Owner != b.Owner {
		return a.Owner < b.Owner
	}
	if a.Currency != b.Currency {
		return a.Currency < b.Currency
	}
	if a.Side != b.Side {
		return a.Side < b.Side
	}
	return a.Amount < b.Amount
}

// SortedLegKeys returns the canonical leg ordering for tests and diagnostics.
func SortedLegKeys(legs []Leg) []string {
	keys := canonicalLegKeys(legs)
	sort.Strings(keys)
	return keys
}

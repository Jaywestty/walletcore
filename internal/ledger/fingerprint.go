package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Fingerprint returns a stable SHA-256 hex digest of the request's identity.
//
// It is computed over a canonical encoding of the journal kind, the legs
// sorted by a fixed key, the reversal target, the actor, the reason and the
// idempotency key. Metadata and occurrence time are not part of the identity.
// The same logical request therefore yields the same fingerprint regardless of
// the order the caller listed its legs in. The repository stores it on the
// journal row and compares it on a duplicate key to tell a replay from a
// conflict.
func Fingerprint(req PostRequest) string {
	var b strings.Builder

	b.WriteString("kind=")
	b.WriteString(string(req.Kind))
	b.WriteString("\nactor=")
	b.WriteString(req.Actor)
	b.WriteString("\nreason=")
	b.WriteString(req.Reason)
	b.WriteString("\nreverses=")
	b.WriteString(req.ReversesJournalID)
	b.WriteString("\nkey=")
	b.WriteString(req.IdempotencyKey)
	b.WriteString("\nlegs=")

	for _, k := range canonicalLegKeys(req.Legs) {
		b.WriteString(k)
		b.WriteString("\n")
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// SortedLegKeys exposes the canonical leg ordering for tests and diagnostics.
func SortedLegKeys(legs []Leg) []string {
	keys := canonicalLegKeys(legs)
	sort.Strings(keys)
	return keys
}

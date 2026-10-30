package ledger

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxLegs bounds a journal, matching the posting rules.
const maxLegs = 20

// MaxAmount is the largest amount allowed on a single leg, in minor units.
// It is 1,000,000,000,000 (one trillion minor units). With at most 20 legs the
// largest possible per-currency total is 2e13, which fits comfortably in int64,
// so per-leg checks alone would not overflow. The bound also keeps amounts
// within a range any consumer can handle and rejects absurd values early.
const MaxAmount int64 = 1_000_000_000_000

// Validate checks a posting request without touching the database. It returns
// a sentinel error describing the first problem found.
func Validate(req PostRequest) error {
	if strings.TrimSpace(req.IdempotencyKey) == "" || utf8.RuneCountInString(req.IdempotencyKey) > 200 {
		return fmt.Errorf("%w: idempotency key must be 1 to 200 characters", ErrInvalidJournal)
	}
	if !req.Kind.Valid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidJournal, req.Kind)
	}
	if strings.TrimSpace(req.Actor) == "" {
		return fmt.Errorf("%w: actor is required", ErrInvalidJournal)
	}
	if strings.TrimSpace(req.Reason) == "" {
		return fmt.Errorf("%w: reason is required", ErrInvalidJournal)
	}

	// Reversal fields must agree with the kind.
	isReversal := req.Kind == KindReversal
	hasTarget := strings.TrimSpace(req.ReversesJournalID) != ""
	if isReversal != hasTarget {
		return fmt.Errorf("%w: reversal kind and reverses_journal_id must agree", ErrInvalidJournal)
	}

	// Settlement fields must agree with the kind.
	settles := strings.TrimSpace(req.SettlesJournalID) != ""
	if req.Kind.SettlesKind() != settles {
		return fmt.Errorf("%w: settlement kind and settles_journal_id must agree", ErrInvalidJournal)
	}
	if hasTarget && settles {
		return fmt.Errorf("%w: a journal cannot both reverse and settle", ErrInvalidJournal)
	}

	if len(req.Legs) < 2 {
		return fmt.Errorf("%w: a journal needs at least two legs", ErrUnbalancedJournal)
	}
	if len(req.Legs) > maxLegs {
		return fmt.Errorf("%w: a journal takes at most %d legs", ErrInvalidJournal, maxLegs)
	}

	dr := map[string]int64{}
	cr := map[string]int64{}
	for i, leg := range req.Legs {
		if !leg.Account.Kind.ValidKind() {
			return fmt.Errorf("%w: leg %d has unknown account kind %q", ErrInvalidJournal, i, leg.Account.Kind)
		}
		if strings.TrimSpace(leg.Account.OwnerRef) == "" {
			return fmt.Errorf("%w: leg %d has empty owner", ErrInvalidJournal, i)
		}
		if !validCurrency(leg.Account.Currency) {
			return fmt.Errorf("%w: leg %d has invalid currency %q", ErrInvalidJournal, i, leg.Account.Currency)
		}
		if !leg.Side.Valid() {
			return fmt.Errorf("%w: leg %d has invalid side %q", ErrInvalidJournal, i, leg.Side)
		}
		if leg.Amount <= 0 {
			return fmt.Errorf("%w: leg %d amount must be positive", ErrInvalidAmount, i)
		}
		if leg.Amount > MaxAmount {
			return fmt.Errorf("%w: leg %d amount exceeds maximum %d", ErrInvalidAmount, i, MaxAmount)
		}

		// Sum with a checked add so a per-currency total can never wrap.
		target := cr
		if leg.Side == Debit {
			target = dr
		}
		next, ok := checkedAdd(target[leg.Account.Currency], leg.Amount)
		if !ok {
			return fmt.Errorf("%w: %s total would overflow", ErrInvalidJournal, leg.Account.Currency)
		}
		target[leg.Account.Currency] = next
	}

	// Every currency that appears must balance.
	currencies := map[string]struct{}{}
	for c := range dr {
		currencies[c] = struct{}{}
	}
	for c := range cr {
		currencies[c] = struct{}{}
	}
	for c := range currencies {
		if dr[c] != cr[c] {
			return fmt.Errorf("%w: %s debits %d credits %d", ErrUnbalancedJournal, c, dr[c], cr[c])
		}
	}

	return nil
}

// checkedAdd returns a+b and whether the result fits in int64. Both inputs are
// non-negative in this package, so overflow is detected by a negative result.
func checkedAdd(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, false
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, false
	}
	return a + b, true
}

// ValidKind reports whether k is one of the four account kinds.
func (k AccountKind) ValidKind() bool {
	switch k {
	case KindUserWallet, KindSettlement, KindFees, KindHolds:
		return true
	default:
		return false
	}
}

// MirrorMatches reports whether legs exactly mirror original with opposite
// sides. Two legs match when they share account reference and amount but the
// side is flipped. This underpins reversal posting.
func MirrorMatches(original, reversing []Leg) bool {
	if len(original) != len(reversing) {
		return false
	}
	return sameLegMultiset(original, flipSides(reversing))
}

func flipSides(legs []Leg) []Leg {
	out := make([]Leg, len(legs))
	for i, l := range legs {
		l.Side = opposite(l.Side)
		out[i] = l
	}
	return out
}

func opposite(s Side) Side {
	if s == Debit {
		return Credit
	}
	return Debit
}

// sameLegMultiset compares two leg slices ignoring order.
func sameLegMultiset(a, b []Leg) bool {
	ka := canonicalLegKeys(a)
	kb := canonicalLegKeys(b)
	if len(ka) != len(kb) {
		return false
	}
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

func canonicalLegKeys(legs []Leg) []string {
	keys := make([]string, len(legs))
	for i, l := range legs {
		keys[i] = strings.Join([]string{
			string(l.Account.Kind),
			l.Account.OwnerRef,
			l.Account.Currency,
			string(l.Side),
			fmt.Sprintf("%d", l.Amount),
		}, "|")
	}
	sort.Strings(keys)
	return keys
}

func validCurrency(c string) bool {
	if len(c) != 3 {
		return false
	}
	for i := 0; i < len(c); i++ {
		if c[i] < 'A' || c[i] > 'Z' {
			return false
		}
	}
	return true
}

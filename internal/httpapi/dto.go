package httpapi

import (
	"time"

	"github.com/Jaywestty/walletcore/internal/ledger"
)

// Request bodies. Amounts are integers in minor units. The actor is never read
// from the body: unknown fields are rejected.

type fundBody struct {
	Owner    string `json:"owner"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type transferBody struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Amount   int64  `json:"amount"`
	Fee      int64  `json:"fee"`
	Currency string `json:"currency"`
}

type feeBody struct {
	Owner    string `json:"owner"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

type withdrawBody struct {
	Owner    string `json:"owner"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

// Response bodies.

type entryJSON struct {
	ID          string `json:"id"`
	JournalID   string `json:"journal_id"`
	JournalKind string `json:"journal_kind"`
	AccountID   string `json:"account_id"`
	Side        string `json:"side"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	EntrySeq    int64  `json:"entry_seq"`
}

type journalJSON struct {
	ID                string            `json:"id"`
	Kind              string            `json:"kind"`
	IdempotencyKey    string            `json:"idempotency_key"`
	Actor             string            `json:"actor"`
	Reason            string            `json:"reason"`
	Metadata          map[string]string `json:"metadata"`
	ReversesJournalID string            `json:"reverses_journal_id,omitempty"`
	SettlesJournalID  string            `json:"settles_journal_id,omitempty"`
	OccurredAt        string            `json:"occurred_at"`
	PostedAt          string            `json:"posted_at"`
	Replayed          bool              `json:"replayed"`
	Entries           []entryJSON       `json:"entries"`
}

type balancesJSON struct {
	Owner     string `json:"owner"`
	Currency  string `json:"currency"`
	Available int64  `json:"available"`
	Held      int64  `json:"held"`
}

type statementJSON struct {
	Owner     string      `json:"owner"`
	Currency  string      `json:"currency"`
	Entries   []entryJSON `json:"entries"`
	NextAfter int64       `json:"next_after_seq"`
}

func toJournalJSON(j ledger.Journal) journalJSON {
	entries := make([]entryJSON, 0, len(j.Entries))
	for _, e := range j.Entries {
		entries = append(entries, entryJSON{
			ID:          e.ID,
			JournalID:   e.JournalID,
			JournalKind: string(j.Kind),
			AccountID:   e.AccountID,
			Side:        string(e.Side),
			Amount:      e.Amount,
			Currency:    e.Currency,
			EntrySeq:    e.EntrySeq,
		})
	}
	return journalJSON{
		ID:                j.ID,
		Kind:              string(j.Kind),
		IdempotencyKey:    j.IdempotencyKey,
		Actor:             j.Actor,
		Reason:            j.Reason,
		Metadata:          j.Metadata,
		ReversesJournalID: j.ReversesJournalID,
		SettlesJournalID:  j.SettlesJournalID,
		OccurredAt:        j.OccurredAt.UTC().Format(time.RFC3339),
		PostedAt:          j.PostedAt.UTC().Format(time.RFC3339),
		Replayed:          j.Replayed,
		Entries:           entries,
	}
}

func toEntryJSON(e ledger.Entry) entryJSON {
	return entryJSON{
		ID:          e.ID,
		JournalID:   e.JournalID,
		JournalKind: string(e.JournalKind),
		AccountID:   e.AccountID,
		Side:        string(e.Side),
		Amount:      e.Amount,
		Currency:    e.Currency,
		EntrySeq:    e.EntrySeq,
	}
}

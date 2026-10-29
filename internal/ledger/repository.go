package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store persists and reads the ledger. It is defined here, in the package that
// consumes it, so callers depend on behaviour rather than on pgx.
type Store interface {
	PostTransaction(ctx context.Context, req PostRequest) (Journal, error)
	Balance(ctx context.Context, ref AccountRef) (int64, error)
}

// Repository is the pgx implementation of Store. It owns one shared pool and
// never mutates server state outside an explicit transaction.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository over a shared pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// compile-time check.
var _ Store = (*Repository)(nil)

// PostTransaction validates the request, then posts the journal and its
// entries and updates the cached balances in a single database transaction.
//
// The database enforces the hard rules as a safety net: LD001 and LD002 are
// raised by a deferred constraint trigger at COMMIT, so a failed commit must
// never be reported as success. Errors from every step, including Commit, are
// passed through MapError.
func (r *Repository) PostTransaction(ctx context.Context, req PostRequest) (Journal, error) {
	if err := Validate(req); err != nil {
		return Journal{}, err
	}
	req.OccurredAt = req.OccurredAt.UTC()
	if req.OccurredAt.IsZero() {
		req.OccurredAt = time.Now().UTC()
	}
	fingerprint := Fingerprint(req)

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Journal{}, fmt.Errorf("begin: %w", err)
	}
	// Deferred rollback. After a successful Commit this is a no-op. The
	// rollback must still be attempted if the request context is already
	// cancelled, so it uses a context detached from cancellation with a short
	// deadline of its own.
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	// For a reversal, lock and verify the original before touching balances.
	if req.Kind == KindReversal {
		if err := r.verifyReversal(ctx, tx, req); err != nil {
			return Journal{}, err
		}
	}

	// Resolve every account, creating it and its zero balance if missing.
	accounts, err := r.resolveAccounts(ctx, tx, req.Legs)
	if err != nil {
		return Journal{}, err
	}

	// Lock the affected balances in ascending account id order in one query,
	// returning their values.
	locked, err := r.lockBalances(ctx, tx, accounts)
	if err != nil {
		return Journal{}, err
	}

	// Compute and apply new balances from the locked values.
	balances, err := r.applyBalances(ctx, tx, req.Legs, accounts, locked)
	if err != nil {
		return Journal{}, err
	}

	journalID, err := r.insertJournal(ctx, tx, req, fingerprint)
	if err != nil {
		return Journal{}, err
	}

	entries, err := r.insertEntries(ctx, tx, journalID, req.Legs, accounts)
	if err != nil {
		return Journal{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		// LD001 and LD002 surface here.
		return Journal{}, fmt.Errorf("commit: %w", MapError(err))
	}

	return Journal{
		ID:                journalID,
		Kind:              req.Kind,
		IdempotencyKey:    req.IdempotencyKey,
		Fingerprint:       fingerprint,
		Actor:             req.Actor,
		Reason:            req.Reason,
		Metadata:          nonNilMetadata(req.Metadata),
		ReversesJournalID: req.ReversesJournalID,
		OccurredAt:        req.OccurredAt,
		Entries:           entries,
		Balances:          balances,
	}, nil
}

// nonNilMetadata returns an empty map for nil so callers never see a nil map.
func nonNilMetadata(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// resolveAccounts returns a map from account reference key to resolved account
// for every leg. Missing accounts are created with a zero balance.
//
// References are resolved in sorted refKey order so two concurrent postings
// that create the same new accounts in opposite request order still insert them
// in one global order. That prevents the two transactions from waiting on each
// other's uncommitted rows.
func (r *Repository) resolveAccounts(ctx context.Context, tx pgx.Tx, legs []Leg) (map[string]Account, error) {
	// Collect distinct references and sort by key.
	seen := make(map[string]AccountRef, len(legs))
	for _, leg := range legs {
		seen[refKey(leg.Account)] = leg.Account
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make(map[string]Account, len(keys))
	for _, key := range keys {
		acct, err := r.resolveAccount(ctx, tx, seen[key])
		if err != nil {
			return nil, err
		}
		if acct.Status != "active" {
			return nil, fmt.Errorf("%w: %s/%s", ErrAccountInactive, acct.Kind, acct.OwnerRef)
		}
		out[key] = acct
	}
	return out, nil
}

// resolveAccount selects an account, inserting it only when it is missing, and
// ensures a zero balance row exists. Reading before writing avoids taking a
// write lock on an existing account.
func (r *Repository) resolveAccount(ctx context.Context, tx pgx.Tx, ref AccountRef) (Account, error) {
	const selectAccount = `
		SELECT id::text, kind, owner_ref, currency, status
		FROM ledger_accounts
		WHERE kind = $1 AND owner_ref = $2 AND currency = $3`

	var acct Account
	err := tx.QueryRow(ctx, selectAccount, string(ref.Kind), ref.OwnerRef, ref.Currency).
		Scan(&acct.ID, &acct.Kind, &acct.OwnerRef, &acct.Currency, &acct.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		const insertAccount = `
			INSERT INTO ledger_accounts (kind, owner_ref, currency)
			VALUES ($1, $2, $3)
			ON CONFLICT (kind, owner_ref, currency) DO NOTHING
			RETURNING id::text, kind, owner_ref, currency, status`
		err = tx.QueryRow(ctx, insertAccount, string(ref.Kind), ref.OwnerRef, ref.Currency).
			Scan(&acct.ID, &acct.Kind, &acct.OwnerRef, &acct.Currency, &acct.Status)
		if errors.Is(err, pgx.ErrNoRows) {
			// Another transaction inserted it between our select and insert.
			err = tx.QueryRow(ctx, selectAccount, string(ref.Kind), ref.OwnerRef, ref.Currency).
				Scan(&acct.ID, &acct.Kind, &acct.OwnerRef, &acct.Currency, &acct.Status)
		}
	}
	if err != nil {
		return Account{}, fmt.Errorf("resolve account: %w", MapError(err))
	}

	// Ensure a balance row exists, selecting first to avoid a write on an
	// existing row.
	var exists bool
	err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ledger_balances WHERE account_id = $1)`, acct.ID).Scan(&exists)
	if err != nil {
		return Account{}, fmt.Errorf("check balance row: %w", MapError(err))
	}
	if !exists {
		const insertBalance = `
			INSERT INTO ledger_balances (account_id, balance)
			VALUES ($1, 0)
			ON CONFLICT (account_id) DO NOTHING`
		if _, err := tx.Exec(ctx, insertBalance, acct.ID); err != nil {
			return Account{}, fmt.Errorf("create balance: %w", MapError(err))
		}
	}

	return acct, nil
}

// lockBalances locks and returns the balance rows for all accounts in
// ascending account id order in a single query. A single ordered
// SELECT ... FOR UPDATE is what makes concurrent posts that touch overlapping
// accounts acquire locks in the same order, preventing deadlock. The returned
// map is keyed by account id.
func (r *Repository) lockBalances(ctx context.Context, tx pgx.Tx, accounts map[string]Account) (map[string]int64, error) {
	ids := accountIDs(accounts)
	const lockSQL = `
		SELECT account_id::text, balance
		FROM ledger_balances
		WHERE account_id = ANY($1::uuid[])
		ORDER BY account_id
		FOR UPDATE`
	rows, err := tx.Query(ctx, lockSQL, ids)
	if err != nil {
		return nil, fmt.Errorf("lock balances: %w", MapError(err))
	}
	defer rows.Close()

	locked := make(map[string]int64, len(accounts))
	for rows.Next() {
		var id string
		var bal int64
		if err := rows.Scan(&id, &bal); err != nil {
			return nil, fmt.Errorf("scan locked balance: %w", err)
		}
		locked[id] = bal
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Every account must have exactly one locked balance row. A mismatch means
	// an account was resolved without its balance row, which is an internal
	// invariant failure.
	if len(locked) != len(accounts) {
		return nil, fmt.Errorf("lock balances: expected %d rows, got %d", len(accounts), len(locked))
	}
	return locked, nil
}

// applyBalances computes the delta for each leg and writes the new values using
// the already-locked balances. It returns the resulting balances.
func (r *Repository) applyBalances(ctx context.Context, tx pgx.Tx, legs []Leg, accounts map[string]Account, locked map[string]int64) ([]Balance, error) {
	// Aggregate the net delta per account reference.
	deltaByKey := map[string]int64{}
	for _, leg := range legs {
		d, err := Delta(leg.Account.Kind, leg.Side, leg.Amount)
		if err != nil {
			return nil, err
		}
		deltaByKey[refKey(leg.Account)] += d
	}

	// Write new values.
	out := make([]Balance, 0, len(accounts))
	const updateSQL = `UPDATE ledger_balances SET balance = $1, updated_at = now() WHERE account_id = $2`
	for key, acct := range accounts {
		newBal := locked[acct.ID] + deltaByKey[key]
		if _, err := tx.Exec(ctx, updateSQL, newBal, acct.ID); err != nil {
			return nil, fmt.Errorf("update balance: %w", MapError(err))
		}
		out = append(out, Balance{
			AccountID: acct.ID,
			Account: AccountRef{
				Kind:     acct.Kind,
				OwnerRef: acct.OwnerRef,
				Currency: acct.Currency,
			},
			Amount:   newBal,
			Currency: acct.Currency,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out, nil
}

// insertJournal writes the journal header and returns its id.
func (r *Repository) insertJournal(ctx context.Context, tx pgx.Tx, req PostRequest, fingerprint string) (string, error) {
	// A nil map must be stored as an empty JSON object, never JSON null.
	meta := req.Metadata
	if meta == nil {
		meta = map[string]string{}
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("marshal metadata: %w", err)
	}

	var reverses *string
	if req.ReversesJournalID != "" {
		reverses = &req.ReversesJournalID
	}

	const insert = `
		INSERT INTO ledger_journals
			(kind, idempotency_key, fingerprint, actor, reason, metadata, reverses_journal_id, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id::text`
	var id string
	err = tx.QueryRow(ctx, insert,
		string(req.Kind), req.IdempotencyKey, fingerprint, req.Actor, req.Reason,
		metaJSON, reverses, req.OccurredAt,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert journal: %w", MapError(err))
	}
	return id, nil
}

// insertEntries writes the legs and returns them with their generated ids.
func (r *Repository) insertEntries(ctx context.Context, tx pgx.Tx, journalID string, legs []Leg, accounts map[string]Account) ([]Entry, error) {
	out := make([]Entry, 0, len(legs))
	const insert = `
		INSERT INTO ledger_entries (journal_id, account_id, side, amount, currency)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id::text, entry_seq`
	for _, leg := range legs {
		acct := accounts[refKey(leg.Account)]
		var e Entry
		e.JournalID = journalID
		e.AccountID = acct.ID
		e.Side = leg.Side
		e.Amount = leg.Amount
		e.Currency = leg.Account.Currency
		if err := tx.QueryRow(ctx, insert, journalID, acct.ID, string(leg.Side), leg.Amount, leg.Account.Currency).
			Scan(&e.ID, &e.EntrySeq); err != nil {
			return nil, fmt.Errorf("insert entry: %w", MapError(err))
		}
		out = append(out, e)
	}
	return out, nil
}

// verifyReversal locks the original journal, rejects reversing a reversal, and
// checks that the request legs mirror the original with opposite sides.
func (r *Repository) verifyReversal(ctx context.Context, tx pgx.Tx, req PostRequest) error {
	const lockOriginal = `SELECT kind FROM ledger_journals WHERE id = $1 FOR UPDATE`
	var originalKind string
	if err := tx.QueryRow(ctx, lockOriginal, req.ReversesJournalID).Scan(&originalKind); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: original %s", ErrInvalidJournal, req.ReversesJournalID)
		}
		return fmt.Errorf("lock original journal: %w", MapError(err))
	}
	if JournalKind(originalKind) == KindReversal {
		return fmt.Errorf("%w: cannot reverse a reversal", ErrInvalidJournal)
	}

	// Reject a journal that already has a reversal before touching balances,
	// so the caller sees ErrAlreadyReversed rather than a balance error. The
	// unique index on reverses_journal_id remains the hard guarantee.
	var alreadyReversed bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM ledger_journals WHERE reverses_journal_id = $1)`,
		req.ReversesJournalID,
	).Scan(&alreadyReversed); err != nil {
		return fmt.Errorf("check reversal: %w", MapError(err))
	}
	if alreadyReversed {
		return fmt.Errorf("%w: %s", ErrAlreadyReversed, req.ReversesJournalID)
	}

	originalLegs, err := r.loadJournalLegs(ctx, tx, req.ReversesJournalID)
	if err != nil {
		return err
	}
	if !MirrorMatches(originalLegs, req.Legs) {
		return fmt.Errorf("%w: reversal legs do not mirror the original", ErrInvalidJournal)
	}
	return nil
}

// loadJournalLegs reads the legs of a journal as domain legs.
func (r *Repository) loadJournalLegs(ctx context.Context, tx pgx.Tx, journalID string) ([]Leg, error) {
	const q = `
		SELECT a.kind, a.owner_ref, a.currency, e.side, e.amount
		FROM ledger_entries e
		JOIN ledger_accounts a ON a.id = e.account_id
		WHERE e.journal_id = $1
		ORDER BY e.entry_seq`
	rows, err := tx.Query(ctx, q, journalID)
	if err != nil {
		return nil, fmt.Errorf("load original legs: %w", MapError(err))
	}
	defer rows.Close()

	var legs []Leg
	for rows.Next() {
		var l Leg
		var kind, side string
		if err := rows.Scan(&kind, &l.Account.OwnerRef, &l.Account.Currency, &side, &l.Amount); err != nil {
			return nil, fmt.Errorf("scan original leg: %w", err)
		}
		l.Account.Kind = AccountKind(kind)
		l.Side = Side(side)
		legs = append(legs, l)
	}
	return legs, rows.Err()
}

// Balance returns the cached balance for an account reference. It returns
// ErrAccountNotFound when no account row exists, and zero for an existing
// account with no activity.
func (r *Repository) Balance(ctx context.Context, ref AccountRef) (int64, error) {
	const q = `
		SELECT b.balance
		FROM ledger_accounts a
		JOIN ledger_balances b ON b.account_id = a.id
		WHERE a.kind = $1 AND a.owner_ref = $2 AND a.currency = $3`
	var bal int64
	err := r.pool.QueryRow(ctx, q, string(ref.Kind), ref.OwnerRef, ref.Currency).Scan(&bal)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("%w: %s/%s/%s", ErrAccountNotFound, ref.Kind, ref.OwnerRef, ref.Currency)
		}
		return 0, fmt.Errorf("read balance: %w", MapError(err))
	}
	return bal, nil
}

// refKey renders an account reference as a map key.
func refKey(ref AccountRef) string {
	return strings.Join([]string{string(ref.Kind), ref.OwnerRef, ref.Currency}, "|")
}

// accountIDs returns the account uuids of a resolved map as a stable slice.
func accountIDs(accounts map[string]Account) []string {
	ids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	sort.Strings(ids)
	return ids
}

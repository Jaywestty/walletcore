package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Jaywestty/walletcore/internal/ledger"
	"github.com/Jaywestty/walletcore/internal/wallet"
)

// handlers bundles the dependencies shared by the route handlers.
type handlers struct {
	svc    WalletService
	logger *slog.Logger
}

// actorFrom returns the principal name, which is the actor for every write.
func actorFrom(r *http.Request) string {
	if p, ok := principalFrom(r.Context()); ok {
		return p.Name
	}
	return ""
}

// fail maps a domain error to an envelope. Unexpected errors are logged with the
// request id and returned as internal_error.
func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	ae := mapError(err)
	if ae.code == codeInternal {
		h.logger.ErrorContext(r.Context(), "request failed",
			slog.String("request_id", RequestID(r.Context())),
			slog.String("code", ae.code),
			slog.String("error", err.Error()),
		)
	}
	writeError(w, r, ae)
}

// writeJournal writes 201 for a new journal or 200 for a replay.
func (h *handlers) writeJournal(w http.ResponseWriter, r *http.Request, j ledger.Journal, err error) {
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if j.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		writeJSON(w, http.StatusOK, toJournalJSON(j))
		return
	}
	writeJSON(w, http.StatusCreated, toJournalJSON(j))
}

// decodeOrFail decodes the body, writing an error and returning false on
// failure.
func (h *handlers) decodeOrFail(w http.ResponseWriter, r *http.Request, dst any) bool {
	if ae := decodeJSONBody(w, r, dst); ae != nil {
		writeError(w, r, ae)
		return false
	}
	return true
}

func (h *handlers) idempotencyOrFail(w http.ResponseWriter, r *http.Request) (string, bool) {
	key, ae := readIdempotencyKey(r)
	if ae != nil {
		writeError(w, r, ae)
		return "", false
	}
	return key, true
}

func (h *handlers) fund(w http.ResponseWriter, r *http.Request) {
	key, ok := h.idempotencyOrFail(w, r)
	if !ok {
		return
	}
	var body fundBody
	if !h.decodeOrFail(w, r, &body) {
		return
	}
	j, err := h.svc.Fund(r.Context(), wallet.FundRequest{
		IdempotencyKey: key, Actor: actorFrom(r), Owner: body.Owner, Amount: body.Amount, Currency: body.Currency,
	})
	h.writeJournal(w, r, j, err)
}

func (h *handlers) transfer(w http.ResponseWriter, r *http.Request) {
	key, ok := h.idempotencyOrFail(w, r)
	if !ok {
		return
	}
	var body transferBody
	if !h.decodeOrFail(w, r, &body) {
		return
	}
	j, err := h.svc.Transfer(r.Context(), wallet.TransferRequest{
		IdempotencyKey: key, Actor: actorFrom(r), From: body.From, To: body.To,
		Amount: body.Amount, Fee: body.Fee, Currency: body.Currency,
	})
	h.writeJournal(w, r, j, err)
}

func (h *handlers) fee(w http.ResponseWriter, r *http.Request) {
	key, ok := h.idempotencyOrFail(w, r)
	if !ok {
		return
	}
	var body feeBody
	if !h.decodeOrFail(w, r, &body) {
		return
	}
	j, err := h.svc.ChargeFee(r.Context(), wallet.ChargeFeeRequest{
		IdempotencyKey: key, Actor: actorFrom(r), Owner: body.Owner, Amount: body.Amount, Currency: body.Currency,
	})
	h.writeJournal(w, r, j, err)
}

func (h *handlers) withdraw(w http.ResponseWriter, r *http.Request) {
	key, ok := h.idempotencyOrFail(w, r)
	if !ok {
		return
	}
	var body withdrawBody
	if !h.decodeOrFail(w, r, &body) {
		return
	}
	j, err := h.svc.RequestWithdrawal(r.Context(), wallet.WithdrawRequest{
		IdempotencyKey: key, Actor: actorFrom(r), Owner: body.Owner, Amount: body.Amount, Currency: body.Currency,
	})
	h.writeJournal(w, r, j, err)
}

func (h *handlers) finalize(w http.ResponseWriter, r *http.Request) {
	key, ok := h.idempotencyOrFail(w, r)
	if !ok {
		return
	}
	j, err := h.svc.FinalizeWithdrawal(r.Context(), wallet.SettleWithdrawalRequest{
		IdempotencyKey: key, Actor: actorFrom(r), RequestID: chi.URLParam(r, "id"),
	})
	h.writeJournal(w, r, j, err)
}

func (h *handlers) failSettlement(w http.ResponseWriter, r *http.Request) {
	key, ok := h.idempotencyOrFail(w, r)
	if !ok {
		return
	}
	j, err := h.svc.FailWithdrawal(r.Context(), wallet.SettleWithdrawalRequest{
		IdempotencyKey: key, Actor: actorFrom(r), RequestID: chi.URLParam(r, "id"),
	})
	h.writeJournal(w, r, j, err)
}

func (h *handlers) reverse(w http.ResponseWriter, r *http.Request) {
	key, ok := h.idempotencyOrFail(w, r)
	if !ok {
		return
	}
	j, err := h.svc.Reverse(r.Context(), wallet.ReverseRequest{
		IdempotencyKey: key, Actor: actorFrom(r), JournalID: chi.URLParam(r, "id"),
	})
	h.writeJournal(w, r, j, err)
}

func (h *handlers) balances(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	currency := chi.URLParam(r, "currency")
	available, held, err := h.svc.Balances(r.Context(), owner, currency)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, balancesJSON{Owner: owner, Currency: currency, Available: available, Held: held})
}

func (h *handlers) statement(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	currency := chi.URLParam(r, "currency")
	limit, ae := parseLimit(r.URL.Query().Get("limit"))
	if ae != nil {
		writeError(w, r, ae)
		return
	}
	after, ae := parseAfterSeq(r.URL.Query().Get("after_seq"))
	if ae != nil {
		writeError(w, r, ae)
		return
	}
	page, err := h.svc.Statement(r.Context(), owner, currency, after, limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	entries := make([]entryJSON, 0, len(page.Entries))
	for _, e := range page.Entries {
		entries = append(entries, toEntryJSON(e))
	}
	writeJSON(w, http.StatusOK, statementJSON{
		Owner: owner, Currency: currency, Entries: entries, NextAfter: page.NextAfterSeq,
	})
}

func (h *handlers) journal(w http.ResponseWriter, r *http.Request) {
	j, err := h.svc.Journal(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toJournalJSON(j))
}

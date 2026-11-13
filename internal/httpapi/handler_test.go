package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Jaywestty/walletcore/internal/ledger"
	"github.com/Jaywestty/walletcore/internal/wallet"
)

// fakeService records calls and returns canned results.
type fakeService struct {
	journal ledger.Journal
	err     error
	page    ledger.StatementPage
	avail   int64
	held    int64

	lastFund     wallet.FundRequest
	lastTransfer wallet.TransferRequest
	lastSettle   wallet.SettleWithdrawalRequest
	lastReverse  wallet.ReverseRequest
}

func (f *fakeService) Fund(_ context.Context, r wallet.FundRequest) (ledger.Journal, error) {
	f.lastFund = r
	return f.journal, f.err
}
func (f *fakeService) Transfer(_ context.Context, r wallet.TransferRequest) (ledger.Journal, error) {
	f.lastTransfer = r
	return f.journal, f.err
}
func (f *fakeService) ChargeFee(_ context.Context, r wallet.ChargeFeeRequest) (ledger.Journal, error) {
	return f.journal, f.err
}
func (f *fakeService) RequestWithdrawal(_ context.Context, r wallet.WithdrawRequest) (ledger.Journal, error) {
	return f.journal, f.err
}
func (f *fakeService) FinalizeWithdrawal(_ context.Context, r wallet.SettleWithdrawalRequest) (ledger.Journal, error) {
	f.lastSettle = r
	return f.journal, f.err
}
func (f *fakeService) FailWithdrawal(_ context.Context, r wallet.SettleWithdrawalRequest) (ledger.Journal, error) {
	f.lastSettle = r
	return f.journal, f.err
}
func (f *fakeService) Reverse(_ context.Context, r wallet.ReverseRequest) (ledger.Journal, error) {
	f.lastReverse = r
	return f.journal, f.err
}
func (f *fakeService) Balances(_ context.Context, owner, currency string) (int64, int64, error) {
	return f.avail, f.held, f.err
}
func (f *fakeService) Statement(_ context.Context, owner, currency string, _ int64, _ int) (ledger.StatementPage, error) {
	return f.page, f.err
}
func (f *fakeService) Journal(_ context.Context, id string) (ledger.Journal, error) {
	return f.journal, f.err
}

const testKey = "test-key-0123456789abcdef0123456789abcdef"

func newTestRouter(t *testing.T, svc WalletService) http.Handler {
	t.Helper()
	return NewRouter(Options{
		Logger:         slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Service:        svc,
		Authenticator:  NewAPIKeyAuthenticator(map[string]string{"svc": testKey}),
		RequestTimeout: 5 * time.Second,
	})
}

func doJSON(t *testing.T, h http.Handler, method, path, body, key string, auth bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+testKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sampleJournal() ledger.Journal {
	return ledger.Journal{
		ID:             "11111111-1111-1111-1111-111111111111",
		Kind:           ledger.KindFund,
		IdempotencyKey: "k",
		Actor:          "svc",
		Reason:         "wallet fund",
		Metadata:       map[string]string{},
		OccurredAt:     time.Now().UTC(),
		PostedAt:       time.Now().UTC(),
		Entries:        []ledger.Entry{{ID: "e1", JournalID: "j", AccountID: "a", Side: ledger.Debit, Amount: 100, Currency: "USD", EntrySeq: 1}},
	}
}

func TestFundSuccess(t *testing.T) {
	svc := &fakeService{journal: sampleJournal()}
	h := newTestRouter(t, svc)
	rec := doJSON(t, h, http.MethodPost, "/v1/funds", `{"owner":"u1","amount":100,"currency":"USD"}`, "k1", true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if svc.lastFund.Actor != "svc" {
		t.Fatalf("actor = %q, want svc (from principal)", svc.lastFund.Actor)
	}
	if !strings.Contains(rec.Body.String(), `"replayed":false`) {
		t.Fatalf("body missing replayed flag: %s", rec.Body.String())
	}
}

func TestReplayReturns200(t *testing.T) {
	j := sampleJournal()
	j.Replayed = true
	svc := &fakeService{journal: j}
	h := newTestRouter(t, svc)
	rec := doJSON(t, h, http.MethodPost, "/v1/funds", `{"owner":"u1","amount":100,"currency":"USD"}`, "k1", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("missing Idempotent-Replayed header")
	}
}

func TestMissingIdempotencyKey(t *testing.T) {
	h := newTestRouter(t, &fakeService{})
	rec := doJSON(t, h, http.MethodPost, "/v1/funds", `{"owner":"u1","amount":1,"currency":"USD"}`, "", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"idempotency_key_required"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	h := newTestRouter(t, &fakeService{})
	rec := doJSON(t, h, http.MethodPost, "/v1/funds", `{"owner":"u1","amount":1,"currency":"USD","actor":"evil"}`, "k", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (unknown field actor): %s", rec.Code, rec.Body.String())
	}
}

func TestFloatAmountRejected(t *testing.T) {
	h := newTestRouter(t, &fakeService{})
	rec := doJSON(t, h, http.MethodPost, "/v1/funds", `{"owner":"u1","amount":1.5,"currency":"USD"}`, "k", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestWrongContentType(t *testing.T) {
	svc := &fakeService{}
	h := newTestRouter(t, svc)
	req := httptest.NewRequest(http.MethodPost, "/v1/funds", strings.NewReader(`{"owner":"u1","amount":1,"currency":"USD"}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Idempotency-Key", "k")
	req.Header.Set("Authorization", "Bearer "+testKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
}

func TestTrailingJSONRejected(t *testing.T) {
	h := newTestRouter(t, &fakeService{})
	rec := doJSON(t, h, http.MethodPost, "/v1/funds", `{"owner":"u1","amount":1,"currency":"USD"}{"x":1}`, "k", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestOversizedBody(t *testing.T) {
	h := newTestRouter(t, &fakeService{})
	big := `{"owner":"` + strings.Repeat("a", 2<<20) + `","amount":1,"currency":"USD"}`
	rec := doJSON(t, h, http.MethodPost, "/v1/funds", big, "k", true)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
}

func TestAuth(t *testing.T) {
	h := newTestRouter(t, &fakeService{})
	body := `{"owner":"u1","amount":1,"currency":"USD"}`

	rec := doJSON(t, h, http.MethodPost, "/v1/funds", body, "k", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing key: status = %d, want 401", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("missing WWW-Authenticate")
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/funds", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "k")
	req.Header.Set("Authorization", "Bearer wrong-key")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: status = %d, want 401", rec.Code)
	}
}

func TestHealthNeedsNoKey(t *testing.T) {
	h := newTestRouter(t, &fakeService{})
	if rec := doJSON(t, h, http.MethodGet, "/healthz", "", "", false); rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		code int
		body string
	}{
		{ledger.ErrInvalidJournal, http.StatusUnprocessableEntity, "invalid_request"},
		{ledger.ErrInsufficientFunds, http.StatusConflict, "insufficient_funds"},
		{ledger.ErrAccountInactive, http.StatusUnprocessableEntity, "account_inactive"},
		{ledger.ErrJournalNotFound, http.StatusNotFound, "not_found"},
		{ledger.ErrIdempotencyConflict, http.StatusConflict, "idempotency_conflict"},
		{ledger.ErrAlreadyReversed, http.StatusConflict, "already_reversed"},
		{ledger.ErrWithdrawalAlreadySettled, http.StatusConflict, "already_settled"},
		{ledger.ErrLockTimeout, http.StatusServiceUnavailable, "busy"},
		{ledger.ErrOutcomeUnknown, http.StatusServiceUnavailable, "outcome_unknown"},
		{context.DeadlineExceeded, http.StatusServiceUnavailable, "timeout"},
		{errors.New("boom"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			svc := &fakeService{err: tc.err}
			h := newTestRouter(t, svc)
			rec := doJSON(t, h, http.MethodPost, "/v1/funds", `{"owner":"u1","amount":1,"currency":"USD"}`, "k", true)
			if rec.Code != tc.code {
				t.Fatalf("status = %d, want %d", rec.Code, tc.code)
			}
			if !strings.Contains(rec.Body.String(), `"code":"`+tc.body+`"`) {
				t.Fatalf("body = %s, want code %s", rec.Body.String(), tc.body)
			}
		})
	}
}

func TestPanicRecovery(t *testing.T) {
	svc := &panicService{}
	h := newTestRouter(t, svc)
	rec := doJSON(t, h, http.MethodGet, "/v1/journals/11111111-1111-1111-1111-111111111111", "", "", true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"code":"internal_error"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

type panicService struct{ fakeService }

func (p *panicService) Journal(context.Context, string) (ledger.Journal, error) { panic("boom") }

func TestBalancesAndStatement(t *testing.T) {
	svc := &fakeService{avail: 500, held: 40, page: ledger.StatementPage{NextAfterSeq: 7,
		Entries: []ledger.Entry{{ID: "e1", JournalID: "j", JournalKind: ledger.KindFund, AccountID: "a", Side: ledger.Credit, Amount: 100, Currency: "USD", EntrySeq: 1}}}}
	h := newTestRouter(t, svc)

	rec := doJSON(t, h, http.MethodGet, "/v1/wallets/u1/USD", "", "", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"available":500`) {
		t.Fatalf("balances: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodGet, "/v1/wallets/u1/USD/entries?limit=1&after_seq=3", "", "", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"next_after_seq":7`) {
		t.Fatalf("statement: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, h, http.MethodGet, "/v1/wallets/u1/USD/entries?limit=0", "", "", true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad limit: status = %d, want 422", rec.Code)
	}
}

func TestLogCaptureHidesSecrets(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := NewRouter(Options{
		Logger:         logger,
		Service:        &fakeService{journal: sampleJournal()},
		Authenticator:  NewAPIKeyAuthenticator(map[string]string{"svc": testKey}),
		RequestTimeout: 5 * time.Second,
	})
	rec := doJSON(t, h, http.MethodPost, "/v1/funds", `{"owner":"u1","amount":100,"currency":"USD"}`, "k1", true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	logs := buf.String()
	if strings.Contains(logs, testKey) {
		t.Fatal("logs contain the API key")
	}
	if strings.Contains(logs, "Authorization") || strings.Contains(logs, "Bearer") {
		t.Fatal("logs contain the Authorization header")
	}
	if !strings.Contains(logs, `"principal":"svc"`) {
		t.Fatalf("logs missing principal: %s", logs)
	}
}

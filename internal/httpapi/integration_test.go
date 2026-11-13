//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/Jaywestty/walletcore/internal/httpapi"
	"github.com/Jaywestty/walletcore/internal/ledger"
	"github.com/Jaywestty/walletcore/internal/testdb"
	"github.com/Jaywestty/walletcore/internal/wallet"
)

const apiKey = "integration-key-0123456789abcdef0123456789abcd"

func TestMain(m *testing.M) {
	testdb.Main(m)
}

func newServer(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	h := testdb.New(t)
	cfg, err := pgxpool.ParseConfig(h.DSN)
	require.NoError(t, err)
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	repo := ledger.NewRepository(pool, ledger.Options{LockTimeout: 5 * time.Second})
	svc := wallet.New(repo)
	handler := httpapi.NewRouter(httpapi.Options{
		Logger:          slog.New(slog.NewJSONHandler(io.Discard, nil)),
		ReadinessPinger: pool,
		Service:         svc,
		Authenticator:   httpapi.NewAPIKeyAuthenticator(map[string]string{"svc": apiKey}),
		RequestTimeout:  10 * time.Second,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, pool
}

func post(t *testing.T, srv *httptest.Server, path, body, key string) (int, map[string]any, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, resp.Header
}

func get(t *testing.T, srv *httptest.Server, path string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

func TestFullFlow(t *testing.T) {
	if os.Getenv("INTEGRATION") != "1" {
		t.Skip("integration")
	}
	srv, pool := newServer(t)

	// Fund.
	code, body, _ := post(t, srv, "/v1/funds", `{"owner":"u1","amount":1000,"currency":"USD"}`, "fund-1")
	require.Equal(t, http.StatusCreated, code, "%v", body)

	// Transfer with fee.
	code, body, _ = post(t, srv, "/v1/transfers", `{"from":"u1","to":"u2","amount":200,"fee":5,"currency":"USD"}`, "tf-1")
	require.Equal(t, http.StatusCreated, code, "%v", body)

	// Request withdrawal and finalize.
	code, body, _ = post(t, srv, "/v1/withdrawals", `{"owner":"u1","amount":100,"currency":"USD"}`, "wd-1")
	require.Equal(t, http.StatusCreated, code, "%v", body)
	reqID, _ := body["id"].(string)
	require.NotEmpty(t, reqID)
	code, body, _ = post(t, srv, "/v1/withdrawals/"+reqID+"/finalize", "", "fin-1")
	require.Equal(t, http.StatusCreated, code, "%v", body)

	// Balances.
	code, body = get(t, srv, "/v1/wallets/u1/USD")
	require.Equal(t, http.StatusOK, code)
	require.EqualValues(t, 695, body["available"]) // 1000 - 205 - 100
	require.EqualValues(t, 0, body["held"])

	// Statement paginated with limit 1.
	code, body = get(t, srv, "/v1/wallets/u1/USD/entries?limit=1")
	require.Equal(t, http.StatusOK, code)
	entries, _ := body["entries"].([]any)
	require.Len(t, entries, 1)
	next, _ := body["next_after_seq"].(float64)
	require.NotZero(t, next)

	// Replay the fund: 200, same body, one journal.
	code, body2, hdr := post(t, srv, "/v1/funds", `{"owner":"u1","amount":1000,"currency":"USD"}`, "fund-1")
	require.Equal(t, http.StatusOK, code)
	require.Equal(t, "true", hdr.Get("Idempotent-Replayed"))
	require.Equal(t, true, body2["replayed"])

	// Conflict: same key, different amount.
	code, body, _ = post(t, srv, "/v1/funds", `{"owner":"u1","amount":999,"currency":"USD"}`, "fund-1")
	require.Equal(t, http.StatusConflict, code)
	require.Equal(t, "idempotency_conflict", errCode(body))

	// Reverse a fund that has been spent.
	fundID := ""
	code, body, _ = post(t, srv, "/v1/funds", `{"owner":"u3","amount":50,"currency":"USD"}`, "fund-u3")
	require.Equal(t, http.StatusCreated, code)
	fundID, _ = body["id"].(string)
	require.NotEmpty(t, fundID)
	code, _, _ = post(t, srv, "/v1/transfers", `{"from":"u3","to":"u1","amount":30,"currency":"USD"}`, "spend-u3")
	require.Equal(t, http.StatusCreated, code)
	code, body, _ = post(t, srv, "/v1/journals/"+fundID+"/reversal", "", "rev-u3")
	require.Equal(t, http.StatusConflict, code)
	require.Equal(t, "insufficient_funds", errCode(body))

	// Invalid uuid.
	code, body, _ = post(t, srv, "/v1/journals/not-a-uuid/reversal", "", "bad-uuid")
	require.Equal(t, http.StatusUnprocessableEntity, code, "%v", body)
	require.Equal(t, "invalid_request", errCode(body))

	testdb.AssertInvariants(t, pool)
}

func errCode(m map[string]any) string {
	if e, ok := m["error"].(map[string]any); ok {
		s, _ := e["code"].(string)
		return s
	}
	return ""
}

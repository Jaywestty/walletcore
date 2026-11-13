// Package httpapi holds the chi router, middleware and HTTP handlers.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// Pinger is the only database capability the readiness check needs. It is
// defined here, at the consumer, so handlers depend on a small interface
// rather than on a concrete pool.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Options configure the router.
type Options struct {
	// Logger receives one structured line per request. A nil logger disables
	// request logging.
	Logger *slog.Logger
	// ReadinessPinger is a nil-safe dependency: when nil, readyz reports the
	// database as unavailable rather than claiming readiness.
	ReadinessPinger Pinger
	// ReadyTimeout bounds the database probe so readyz answers promptly even
	// when the database hangs.
	ReadyTimeout time.Duration
	// TrustProxyHeaders enables using X-Forwarded-For and similar headers to
	// determine the client IP. Only enable behind a trusted reverse proxy.
	TrustProxyHeaders bool
	// RequestTimeout bounds the total handler time for one request.
	RequestTimeout time.Duration
	// Service backs the /v1 wallet routes. Required for /v1 to be mounted.
	Service WalletService
	// Authenticator guards /v1 routes. Required for /v1 to be mounted.
	Authenticator Authenticator
}

// NewRouter builds the application router. Routes live here so main only wires
// dependencies.
func NewRouter(opts Options) http.Handler {
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 2 * time.Second
	}

	r := newRouter(opts.TrustProxyHeaders, opts.RequestTimeout)
	if opts.Logger != nil {
		r.Use(LoggingMiddleware(opts.Logger))
	}

	// Health routes are unauthenticated.
	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", handleReadyz(opts.ReadinessPinger, opts.ReadyTimeout))

	if opts.Service != nil && opts.Authenticator != nil {
		h := &handlers{svc: opts.Service, logger: opts.Logger}
		r.Route("/v1", func(r chiRouter) {
			r.Use(authenticateMiddleware(opts.Authenticator))

			r.Post("/funds", h.fund)
			r.Post("/transfers", h.transfer)
			r.Post("/fees", h.fee)
			r.Post("/withdrawals", h.withdraw)
			r.Post("/withdrawals/{id}/finalize", h.finalize)
			r.Post("/withdrawals/{id}/fail", h.failSettlement)
			r.Post("/journals/{id}/reversal", h.reverse)

			r.Get("/wallets/{owner}/{currency}", h.balances)
			r.Get("/wallets/{owner}/{currency}/entries", h.statement)
			r.Get("/journals/{id}", h.journal)
		})
	}
	return r
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleReadyz reports whether the process can serve traffic. A nil pinger or
// a failed ping yields 503 with no internal detail.
func handleReadyz(p Pinger, timeout time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if p == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]bool{"ok": false})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		if err := p.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]bool{"ok": false})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

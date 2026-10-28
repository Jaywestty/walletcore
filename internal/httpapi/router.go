// Package httpapi holds the chi router, middleware and HTTP handlers.
package httpapi

import (
	"context"
	"encoding/json"
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
}

// NewRouter builds the application router. Routes live here so main only wires
// dependencies.
func NewRouter(opts Options) http.Handler {
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = 2 * time.Second
	}

	r := newRouter()
	if opts.Logger != nil {
		r.Use(LoggingMiddleware(opts.Logger))
	}
	r.Get("/healthz", handleHealthz)
	r.Get("/readyz", handleReadyz(opts.ReadinessPinger, opts.ReadyTimeout))
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

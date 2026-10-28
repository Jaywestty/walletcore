package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// RequestIDHeader carries a request identifier across the hop and back to the
// caller.
const RequestIDHeader = "X-Request-Id"

// newRouter creates a chi router with the base middleware stack.
//
// Middleware order matters: the request id is assigned first so every later
// log line and recovered panic can reference it.
func newRouter() *chi.Mux {
	r := chi.NewRouter()
	r.Use(requestIDMiddleware)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(15 * time.Second))
	return r
}

// requestIDMiddleware reuses an incoming request id or generates one, stores it
// in the context, and echoes it on the response. It avoids blocking on the
// client and is deterministic for handlers and logs.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		ctx := withRequestID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// A random failure must not take down the request; fall back to a
		// timestamp so logs still correlate roughly.
		return "ts-" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b[:])
}

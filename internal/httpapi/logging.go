package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

// principalHolder lets the auth middleware record the principal where the outer
// logging middleware can read it after the handler runs.
type principalHolder struct {
	name string
}

type holderKey struct{}

func withHolder(ctx context.Context, h *principalHolder) context.Context {
	return context.WithValue(ctx, holderKey{}, h)
}

func holderFrom(ctx context.Context) *principalHolder {
	h, _ := ctx.Value(holderKey{}).(*principalHolder)
	return h
}

// statusRecorder captures the response status for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// LoggingMiddleware logs one structured line per request with the method,
// path, status, duration, request id and principal name. It never logs the
// Authorization header, API keys or request bodies.
func LoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			holder := &principalHolder{}
			ctx := withHolder(r.Context(), holder)
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r.WithContext(ctx))
			logger.InfoContext(ctx, "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000.0),
				slog.String("request_id", RequestID(ctx)),
				slog.String("principal", holder.name),
			)
		})
	}
}

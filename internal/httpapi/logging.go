package httpapi

import (
	"log/slog"
	"net/http"
	"time"
)

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
// path, status, duration and request id.
func LoggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			logger.InfoContext(r.Context(), "http request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000.0),
				slog.String("request_id", RequestID(r.Context())),
			)
		})
	}
}

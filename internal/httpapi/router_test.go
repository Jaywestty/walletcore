package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakePinger struct {
	err error
}

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestHealthzAlwaysOK(t *testing.T) {
	// A nil pinger must not affect liveness.
	srv := NewRouter(Options{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "{\"ok\":true}\n" {
		t.Fatalf("body = %q", got)
	}
	if rec.Header().Get(RequestIDHeader) == "" {
		t.Fatal("expected a request id header")
	}
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name       string
		pinger     Pinger
		wantStatus int
		wantBody   string
	}{
		{
			name:       "database reachable",
			pinger:     fakePinger{},
			wantStatus: http.StatusOK,
			wantBody:   "{\"ok\":true}\n",
		},
		{
			name:       "database unreachable",
			pinger:     fakePinger{err: errors.New("connection refused")},
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "{\"ok\":false}\n",
		},
		{
			name:       "no pinger configured",
			pinger:     nil,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "{\"ok\":false}\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewRouter(Options{ReadinessPinger: tc.pinger, ReadyTimeout: time.Second})
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if rec.Body.String() != tc.wantBody {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}

func TestRequestIDIsPreserved(t *testing.T) {
	srv := NewRouter(Options{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set(RequestIDHeader, "abc-123")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if got := rec.Header().Get(RequestIDHeader); got != "abc-123" {
		t.Fatalf("request id = %q, want abc-123", got)
	}
}

func TestRouterUnknownRoute(t *testing.T) {
	srv := NewRouter(Options{})
	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

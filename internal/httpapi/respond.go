package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
)

type principalKey struct{}

// withPrincipal stores the authenticated principal in the context.
func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// principalFrom returns the principal from the context.
func principalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// writeJSON writes a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError writes the error envelope for an apiError, including the request
// id from the context.
func writeError(w http.ResponseWriter, r *http.Request, ae *apiError) {
	env := errorEnvelope{Error: errorBody{
		Code:      ae.code,
		Message:   safeMessages[ae.code],
		RequestID: RequestID(r.Context()),
	}}
	writeJSON(w, ae.status, env)
}

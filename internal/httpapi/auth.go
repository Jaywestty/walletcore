package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
)

// Principal is the authenticated caller.
type Principal struct {
	Name string
}

// Authenticator resolves the caller identity from a request.
type Authenticator interface {
	Authenticate(r *http.Request) (Principal, error)
}

// ErrUnauthenticated is returned when no valid credential is presented.
var ErrUnauthenticated = errors.New("httpapi: unauthenticated")

// APIKeyAuthenticator authenticates via Authorization: Bearer <key>.
type APIKeyAuthenticator struct {
	// keys maps principal name to the SHA-256 digest of the key.
	keys map[string][32]byte
}

// NewAPIKeyAuthenticator builds an authenticator from name to key pairs.
func NewAPIKeyAuthenticator(keys map[string]string) *APIKeyAuthenticator {
	digests := make(map[string][32]byte, len(keys))
	for name, key := range keys {
		digests[name] = sha256.Sum256([]byte(key))
	}
	return &APIKeyAuthenticator{keys: digests}
}

// Authenticate checks the bearer token against every configured key using a
// constant-time comparison and no early exit, so timing does not reveal which
// key matched or whether a prefix was correct.
func (a *APIKeyAuthenticator) Authenticate(r *http.Request) (Principal, error) {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return Principal{}, ErrUnauthenticated
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return Principal{}, ErrUnauthenticated
	}
	got := sha256.Sum256([]byte(token))

	var matched string
	for name, want := range a.keys {
		if subtle.ConstantTimeCompare(got[:], want[:]) == 1 {
			matched = name
		}
	}
	if matched == "" {
		return Principal{}, ErrUnauthenticated
	}
	return Principal{Name: matched}, nil
}

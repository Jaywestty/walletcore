package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxBodyBytes = 1 << 20 // 1 MiB
const maxIdempotencyRunes = 200

// decodeError describes why a body could not be decoded.
type decodeError struct {
	api *apiError
}

// readIdempotencyKey returns the Idempotency-Key header, validated to 1..200
// runes with no control characters.
func readIdempotencyKey(r *http.Request) (string, *apiError) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return "", newAPIError(http.StatusBadRequest, codeIdempotencyRequired)
	}
	if utf8.RuneCountInString(key) > maxIdempotencyRunes {
		return "", newAPIError(http.StatusBadRequest, codeIdempotencyRequired)
	}
	for _, c := range key {
		if c < 0x20 || c == 0x7f {
			return "", newAPIError(http.StatusBadRequest, codeIdempotencyRequired)
		}
	}
	return key, nil
}

// decodeJSONBody enforces Content-Type, size limit, unknown-field rejection and
// exactly one JSON value. It returns an apiError for any violation.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) *apiError {
	ct := r.Header.Get("Content-Type")
	if ct != "application/json" && !strings.HasPrefix(ct, "application/json;") {
		return newAPIError(http.StatusUnsupportedMediaType, codeUnsupportedMedia)
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return decodeFailure(err)
	}
	// Exactly one JSON value: nothing but whitespace may follow.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return newAPIError(http.StatusBadRequest, codeInvalidRequest)
	}
	return nil
}

// decodeFailure classifies a decode error. Malformed JSON is 400; type or
// unknown-field problems are 400 as well, since they are structural. Semantic
// checks happen later in the service and map to 422.
func decodeFailure(err error) *apiError {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return newAPIError(http.StatusRequestEntityTooLarge, codePayloadTooLarge)
	}
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		// A float or string where an integer is expected is a semantic problem.
		if typeErr.Field != "" && (typeErr.Value == "number" || typeErr.Value == "string" || typeErr.Value == "bool") {
			return newAPIError(http.StatusBadRequest, codeInvalidRequest)
		}
		return newAPIError(http.StatusBadRequest, codeInvalidRequest)
	}
	if errors.Is(err, io.EOF) {
		return newAPIError(http.StatusBadRequest, codeInvalidRequest)
	}
	// Unknown fields and syntax errors.
	return newAPIError(http.StatusBadRequest, codeInvalidRequest)
}

// parseLimit parses an optional limit query value.
func parseLimit(raw string) (int, *apiError) {
	if raw == "" {
		return 50, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 200 {
		return 0, newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest)
	}
	return n, nil
}

// parseAfterSeq parses an optional after_seq cursor.
func parseAfterSeq(raw string) (int64, *apiError) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest)
	}
	return n, nil
}

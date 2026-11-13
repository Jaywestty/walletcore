package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/Jaywestty/walletcore/internal/ledger"
	"github.com/Jaywestty/walletcore/internal/wallet"
)

// Error codes returned in the envelope.
const (
	codeInvalidRequest      = "invalid_request"
	codeIdempotencyRequired = "idempotency_key_required"
	codeInsufficientFunds   = "insufficient_funds"
	codeAccountInactive     = "account_inactive"
	codeNotFound            = "not_found"
	codeIdempotencyConflict = "idempotency_conflict"
	codeAlreadyReversed     = "already_reversed"
	codeAlreadySettled      = "already_settled"
	codeBusy                = "busy"
	codeOutcomeUnknown      = "outcome_unknown"
	codeTimeout             = "timeout"
	codeUnauthorized        = "unauthorized"
	codeInternal            = "internal_error"
	codeUnsupportedMedia    = "unsupported_media_type"
	codePayloadTooLarge     = "payload_too_large"
)

// errorEnvelope is the single error response shape.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// safeMessages are fixed per code so raw error text never reaches clients.
var safeMessages = map[string]string{
	codeInvalidRequest:      "The request is invalid.",
	codeIdempotencyRequired: "An Idempotency-Key header is required.",
	codeInsufficientFunds:   "There are insufficient funds for this operation.",
	codeAccountInactive:     "The account is not active.",
	codeNotFound:            "The resource was not found.",
	codeIdempotencyConflict: "The idempotency key was used with a different request.",
	codeAlreadyReversed:     "The journal was already reversed.",
	codeAlreadySettled:      "The withdrawal was already settled.",
	codeBusy:                "The service is busy. Retry shortly.",
	codeOutcomeUnknown:      "The outcome is unknown. Retry with the same Idempotency-Key.",
	codeTimeout:             "The request timed out. Retry with the same Idempotency-Key.",
	codeUnauthorized:        "Authentication is required.",
	codeInternal:            "An internal error occurred.",
	codeUnsupportedMedia:    "Content-Type must be application/json.",
	codePayloadTooLarge:     "The request body is too large.",
}

// apiError is an error already shaped for HTTP.
type apiError struct {
	status int
	code   string
}

func (e *apiError) Error() string { return e.code }

func newAPIError(status int, code string) *apiError {
	return &apiError{status: status, code: code}
}

// mapError converts a domain error to an apiError. It returns nil for a nil
// error and a generic internal error for anything unrecognised.
func mapError(err error) *apiError {
	if err == nil {
		return nil
	}
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}

	switch {
	case errors.Is(err, ledger.ErrInvalidJournal),
		errors.Is(err, ledger.ErrInvalidAmount),
		errors.Is(err, ledger.ErrUnbalancedJournal),
		errors.Is(err, ledger.ErrCurrencyMismatch),
		errors.Is(err, wallet.ErrInvalidRequest):
		return newAPIError(http.StatusUnprocessableEntity, codeInvalidRequest)
	case errors.Is(err, ledger.ErrInsufficientFunds):
		return newAPIError(http.StatusConflict, codeInsufficientFunds)
	case errors.Is(err, ledger.ErrAccountInactive):
		return newAPIError(http.StatusUnprocessableEntity, codeAccountInactive)
	case errors.Is(err, ledger.ErrAccountNotFound),
		errors.Is(err, ledger.ErrJournalNotFound):
		return newAPIError(http.StatusNotFound, codeNotFound)
	case errors.Is(err, ledger.ErrIdempotencyConflict):
		return newAPIError(http.StatusConflict, codeIdempotencyConflict)
	case errors.Is(err, ledger.ErrAlreadyReversed):
		return newAPIError(http.StatusConflict, codeAlreadyReversed)
	case errors.Is(err, ledger.ErrWithdrawalAlreadySettled):
		return newAPIError(http.StatusConflict, codeAlreadySettled)
	case errors.Is(err, ledger.ErrLockTimeout),
		errors.Is(err, ledger.ErrDeadlockDetected),
		errors.Is(err, ledger.ErrSerializationFailure):
		return newAPIError(http.StatusServiceUnavailable, codeBusy)
	case errors.Is(err, ledger.ErrOutcomeUnknown):
		return newAPIError(http.StatusServiceUnavailable, codeOutcomeUnknown)
	case errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, context.Canceled):
		return newAPIError(http.StatusServiceUnavailable, codeTimeout)
	default:
		return newAPIError(http.StatusInternalServerError, codeInternal)
	}
}

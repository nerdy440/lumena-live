// Package errors defines the canonical error types for Lumena.
// Every API response error uses this shape.
// No endpoint returns a bare 500 with "Something went wrong."
package errors

import (
	"encoding/json"
	"net/http"
)

// Code is a stable string enum that clients switch on.
type Code string

const (
	// Auth
	CodeUnauthenticated       Code = "UNAUTHENTICATED"
	CodeSessionExpired        Code = "SESSION_EXPIRED"
	CodeForbidden             Code = "FORBIDDEN"
	CodeAgeAssuranceRequired  Code = "AGE_ASSURANCE_REQUIRED"
	CodeUnderAgeMinimum       Code = "UNDER_AGE_MINIMUM"
	CodeAccountRestricted     Code = "ACCOUNT_RESTRICTED"
	CodeRegionUnavailable     Code = "REGION_UNAVAILABLE"
	CodeLegallyUnavailable    Code = "LEGALLY_UNAVAILABLE"

	// Resource
	CodeNotFound       Code = "NOT_FOUND"
	CodeAlreadyExists  Code = "ALREADY_EXISTS"
	CodeConflict       Code = "CONFLICT"
	CodeStateInvalid   Code = "STATE_INVALID"
	CodeValidation     Code = "VALIDATION_ERROR"
	CodeUnprocessable  Code = "UNPROCESSABLE"

	// Economy — these always include Details with shortfall/required/available
	CodeInsufficientBalance Code = "INSUFFICIENT_BALANCE"
	CodeGiftUnavailable     Code = "GIFT_UNAVAILABLE_IN_REGION"
	CodeRecipientBlocked    Code = "RECIPIENT_BLOCKED"
	CodeRoomNotLive         Code = "ROOM_NOT_LIVE"
	CodeUserMuted           Code = "USER_MUTED_IN_ROOM"

	// Private video — see doc 10 §4 for why these collapse
	CodeRecipientUnavailable Code = "RECIPIENT_UNAVAILABLE"

	// Platform
	CodeRateLimited    Code = "RATE_LIMITED"
	CodeServiceDegraded Code = "SERVICE_DEGRADED"
	CodeInternal       Code = "INTERNAL_ERROR"
)

// APIError is the canonical error returned to clients.
type APIError struct {
	Code      Code           `json:"code"`
	Message   string         `json:"message"` // localized, human-readable
	Details   map[string]any `json:"details,omitempty"`
	TraceID   string         `json:"trace_id,omitempty"`
	Retryable bool           `json:"retryable"`
}

// Envelope wraps APIError in the wire format.
type Envelope struct {
	Err APIError `json:"error"`
}

// New constructs an APIError.
func New(code Code, message string, opts ...Option) *APIError {
	e := &APIError{Code: code, Message: message}
	for _, o := range opts {
		o(e)
	}
	return e
}

type Option func(*APIError)

func WithDetails(details map[string]any) Option {
	return func(e *APIError) { e.Details = details }
}
func WithTraceID(id string) Option {
	return func(e *APIError) { e.TraceID = id }
}
func Retryable() Option {
	return func(e *APIError) { e.Retryable = true }
}

// HTTPStatus maps code to HTTP status.
func (e *APIError) HTTPStatus() int {
	switch e.Code {
	case CodeUnauthenticated, CodeSessionExpired:
		return http.StatusUnauthorized
	case CodeForbidden, CodeAgeAssuranceRequired, CodeUnderAgeMinimum,
		CodeAccountRestricted, CodeRegionUnavailable:
		return http.StatusForbidden
	case CodeLegallyUnavailable:
		return 451
	case CodeNotFound:
		return http.StatusNotFound
	case CodeAlreadyExists, CodeConflict, CodeStateInvalid:
		return http.StatusConflict
	case CodeValidation, CodeUnprocessable:
		return http.StatusUnprocessableEntity
	case CodeInsufficientBalance:
		return http.StatusPaymentRequired
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeServiceDegraded:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// Write serializes the error to the response writer.
func (e *APIError) Write(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.HTTPStatus())
	_ = json.NewEncoder(w).Encode(Envelope{Err: *e})
}

// Common constructors for frequently-used errors.

func Unauthenticated(traceID string) *APIError {
	return New(CodeUnauthenticated, "Authentication required.",
		WithTraceID(traceID))
}

func Forbidden(traceID string) *APIError {
	return New(CodeForbidden, "You don't have permission to do this.",
		WithTraceID(traceID))
}

func NotFound(resource, traceID string) *APIError {
	return New(CodeNotFound, resource+" not found.", WithTraceID(traceID))
}

func InsufficientBalance(required, available int64, traceID string) *APIError {
	shortfall := required - available
	return New(CodeInsufficientBalance,
		"Insufficient coin balance.",
		WithDetails(map[string]any{
			"required":  required,
			"available": available,
			"shortfall": shortfall,
		}),
		WithTraceID(traceID))
}

func RateLimited(retryAfterS int, traceID string) *APIError {
	return New(CodeRateLimited, "Too many requests. Please slow down.",
		WithDetails(map[string]any{"retry_after_s": retryAfterS}),
		WithTraceID(traceID),
		Retryable())
}

func AgeAssuranceRequired(traceID string) *APIError {
	return New(CodeAgeAssuranceRequired,
		"Age verification is required before using this feature.",
		WithTraceID(traceID))
}

// RecipientUnavailable is the collapsed response for gates 2–6 in the PV chain.
// See doc 10 §4 and doc 04 §3 for why these collapse to one code.
// IMPORTANT: Do NOT add a sub-code or reason field to this error.
// Distinguishing block/restricted/privacy is a privacy and safety leak.
func RecipientUnavailable(traceID string) *APIError {
	return New(CodeRecipientUnavailable,
		"This person isn't available for a video call right now.",
		WithTraceID(traceID))
}

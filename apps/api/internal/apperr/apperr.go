// Package apperr defines transport-agnostic application errors that the HTTP
// layer renders as RFC 7807 problem documents.
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

// FieldError describes a validation failure for a single input field.
type FieldError struct {
	Field   string
	Message string
}

// Error is an expected, client-facing failure.
type Error struct {
	Status int
	Title  string
	Detail string
	Fields []FieldError
	cause  error
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s", e.Title, e.Detail)
	}
	return e.Title
}

func (e *Error) Unwrap() error { return e.cause }

// WithCause attaches an underlying error for logging; it is never shown to clients.
func (e *Error) WithCause(err error) *Error {
	e.cause = err
	return e
}

func newErr(status int, detail string) *Error {
	return &Error{Status: status, Title: http.StatusText(status), Detail: detail}
}

func BadRequest(detail string) *Error   { return newErr(http.StatusBadRequest, detail) }
func Unauthorized(detail string) *Error { return newErr(http.StatusUnauthorized, detail) }
func Forbidden(detail string) *Error    { return newErr(http.StatusForbidden, detail) }
func NotFound(detail string) *Error     { return newErr(http.StatusNotFound, detail) }
func Conflict(detail string) *Error     { return newErr(http.StatusConflict, detail) }
func Unprocessable(detail string) *Error {
	return newErr(http.StatusUnprocessableEntity, detail)
}
func TooManyRequests(detail string) *Error { return newErr(http.StatusTooManyRequests, detail) }
func Unavailable(detail string) *Error     { return newErr(http.StatusServiceUnavailable, detail) }

// Validation builds a 400 error carrying per-field messages.
func Validation(fields ...FieldError) *Error {
	e := newErr(http.StatusBadRequest, "request validation failed")
	e.Fields = fields
	return e
}

// As extracts an *Error from err, if any.
func As(err error) (*Error, bool) {
	var ae *Error
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}

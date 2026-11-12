// Package httpx holds the JSON response and error model shared by handlers
// and middleware (API.md "Error model").
package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
)

type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// Error is an API error that handlers can return.
type Error struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func NewError(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

func (e *Error) WithDetails(d any) *Error {
	c := *e
	c.Details = d
	return &c
}

var (
	ErrUnauthenticated = NewError(http.StatusUnauthorized, "unauthenticated", "authentication required")
	ErrNotFound        = NewError(http.StatusNotFound, "not_found", "not found")
	ErrForbidden       = NewError(http.StatusForbidden, "forbidden", "forbidden")
	ErrCSRF            = NewError(http.StatusForbidden, "csrf", "missing or invalid CSRF token or origin")
	ErrTooLarge        = NewError(http.StatusRequestEntityTooLarge, "too_large", "request body too large")
	ErrInternal        = NewError(http.StatusInternalServerError, "internal", "internal error")
)

func BadRequest(msg string) *Error { return NewError(http.StatusBadRequest, "bad_request", msg) }

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// WriteError writes err as the API error model. Non-*Error values become a
// generic 500 (the cause is never exposed).
func WriteError(w http.ResponseWriter, err error) {
	var e *Error
	if !errors.As(err, &e) {
		e = ErrInternal
	}
	WriteJSON(w, e.Status, ErrorBody{Error: ErrorDetail{Code: e.Code, Message: e.Message, Details: e.Details}})
}

// DecodeJSON decodes a JSON body strictly (unknown fields rejected).
func DecodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return ErrTooLarge
		}
		return BadRequest("invalid JSON body: " + err.Error())
	}
	return nil
}

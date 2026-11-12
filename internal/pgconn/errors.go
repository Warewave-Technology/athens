package pgconn

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrUnavailable wraps connect/authentication failures. Its message is
// generic; the cause is logged server-side only.
type ErrUnavailable struct{ Cause error }

func (e *ErrUnavailable) Error() string { return "target unavailable" }
func (e *ErrUnavailable) Unwrap() error { return e.Cause }

// ErrTimeout means statement_timeout fired (SQLSTATE 57014 not caused by a
// cancel of our own context).
var ErrTimeout = errors.New("statement timeout")

// ErrCancelled means the caller's context was cancelled (user cancel,
// client disconnect, target removed).
var ErrCancelled = errors.New("cancelled")

func isAuthError(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (pe.Code == "28P01" || pe.Code == "28000")
}

// classify maps a query error given the caller's context.
func classify(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ErrCancelled
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "57014" {
		return ErrTimeout
	}
	return err
}

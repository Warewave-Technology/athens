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

// ErrReadLoginRequired: a read user's SQL would run on a privileged RW login
// (superuser, replication, bypassrls) without a read-only login (ADR-027).
var ErrReadLoginRequired = errors.New("read-only login required")

// ErrNoDatabase: an explicitly requested database does not exist (3D000).
var ErrNoDatabase = errors.New("database does not exist")

// isMissingDatabase reports SQLSTATE 3D000 (invalid_catalog_name).
func isMissingDatabase(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "3D000"
}

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

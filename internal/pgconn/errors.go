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

// ErrReadLoginRequired: read access needs the target's read-only login
// (readonly-secret), and the target has none (ADR-028).
var ErrReadLoginRequired = errors.New("read-only login required")

// ErrReadLoginPrivileged: the read-only login is itself privileged
// (superuser, replication, bypassrls or similar memberships, ADR-028).
var ErrReadLoginPrivileged = errors.New("read-only login is privileged")

// ErrNoDatabase: an explicitly requested database does not exist (3D000).
var ErrNoDatabase = errors.New("database does not exist")

// ErrDatabaseDenied: the login may not connect to that database (42501).
var ErrDatabaseDenied = errors.New("no permission to connect to this database")

// isNoConnectPrivilege reports SQLSTATE 42501 at connect time (no CONNECT).
func isNoConnectPrivilege(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "42501"
}

// isMissingDatabase reports SQLSTATE 3D000 (invalid_catalog_name).
func isMissingDatabase(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "3D000"
}

// ErrReadLoginUnavailable: the read-only login could not be set up
// (readonly-provision) or the server rejected it. Read users cannot connect;
// the target itself is not marked unreachable (ADR-028).
var ErrReadLoginUnavailable = errors.New("read-only login unavailable")

// errReadLogin: the read-only login cannot be used (the server rejected it,
// or its Secret cannot be read); the cause is logged, never returned.
type errReadLogin struct{ cause error }

func (e *errReadLogin) Error() string        { return "read-only login unavailable" }
func (e *errReadLogin) Unwrap() error        { return e.cause }
func (e *errReadLogin) Is(target error) bool { return target == ErrReadLoginUnavailable }

// ErrNoAccess: the request carries no access level (handlers return 404
// before this; pgconn fails closed).
var ErrNoAccess = errors.New("no access")

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

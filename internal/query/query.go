// Package query executes SQL editor statements with the guardrails of SPEC §7:
// one statement, classification, level enforcement, confirmation of
// destructive (and, on production targets, all write) statements, per-user
// concurrency, cancellation, history and audit.
package query

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	pgxconn "github.com/jackc/pgx/v5/pgconn"
	"github.com/oklog/ulid/v2"

	"github.com/Warewave-Technology/athens/internal/audit"
	"github.com/Warewave-Technology/athens/internal/authz"
	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/httpx"
	"github.com/Warewave-Technology/athens/internal/metrics"
	"github.com/Warewave-Technology/athens/internal/pgconn"
	"github.com/Warewave-Technology/athens/internal/reqctx"
	"github.com/Warewave-Technology/athens/internal/sqlclass"
	"github.com/Warewave-Technology/athens/internal/store"
)

const MaxStatementBytes = 256 << 10

type Service struct {
	Pools    *pgconn.Manager
	Store    store.Store
	Audit    *audit.Writer
	Limits   config.Limits
	Registry *Registry
	Log      *slog.Logger
}

type Input struct {
	User          *reqctx.User
	Target        discovery.Target
	Database      string
	Level         authz.Level
	SQL           string
	Limit         int
	Confirm       bool
	ClientQueryID string
}

type Output struct {
	QueryID        string          `json:"queryId"`
	Class          sqlclass.Class  `json:"class"`
	Destructive    bool            `json:"destructive"`
	Columns        []pgconn.Column `json:"columns"`
	Rows           [][]*string     `json:"rows"`
	Truncated      bool            `json:"truncated"`
	TruncatedCells [][2]int        `json:"truncatedCells"`
	RowsAffected   *int64          `json:"rowsAffected"`
	CommandTag     string          `json:"commandTag"`
	Notices        []pgconn.Notice `json:"notices"`
	DurationMs     int64           `json:"durationMs"`
}

var clientID = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)

func apiErr(status int, code, msg string) *httpx.Error { return httpx.NewError(status, code, msg) }

type SyntaxDetails struct {
	Position int `json:"position"`
	Line     int `json:"line"`
	Column   int `json:"column"`
}

// ConfirmRowsAbove: an unconfirmed data change of more rows than this is
// rolled back and needs confirmation.
const ConfirmRowsAbove = 1000

// xactChanges sums the backend's pending row changes (pg_stat_xact_*).
const xactChanges = `SELECT coalesce(sum(n_tup_ins + n_tup_upd + n_tup_del), 0)::int8 FROM pg_stat_xact_all_tables`

type manyRowsError struct{ n int64 }

func (e *manyRowsError) Error() string { return fmt.Sprintf("statement changes %d rows", e.n) }

// Execute runs in.SQL. Errors are *httpx.Error (API model) or pgconn errors
// for the handler's DB error mapping.
func (s *Service) Execute(ctx context.Context, in Input) (*Output, error) {
	if len(in.SQL) > MaxStatementBytes {
		return nil, apiErr(http.StatusRequestEntityTooLarge, "too_large", "statement exceeds 256 KiB")
	}
	if strings.ContainsRune(in.SQL, 0) {
		// the parser stops at NUL while the server would get the full text
		return nil, httpx.BadRequest("the statement contains a NUL character")
	}
	rec := store.AuditRecord{Event: audit.EventQueryExecute, TargetID: in.Target.ID, Database: in.Database, SQL: in.SQL}
	hist := store.HistoryItem{UserID: in.User.ID, TargetID: in.Target.ID, Database: in.Database, SQL: in.SQL, CreatedAt: time.Now()}

	cls, err := sqlclass.Classify(in.SQL)
	var se *sqlclass.SyntaxError
	switch {
	case errors.As(err, &se):
		line, col := LineCol(in.SQL, se.Position)
		s.history(ctx, hist, "other", "error", nil, nil, se.Message)
		return nil, apiErr(http.StatusBadRequest, "syntax_error", se.Message).WithDetails(SyntaxDetails{Position: se.Position, Line: line, Column: col})
	case errors.Is(err, sqlclass.ErrMultiple):
		return nil, apiErr(http.StatusBadRequest, "multiple_statements", "only one statement per execution is supported")
	case errors.Is(err, sqlclass.ErrEmpty):
		return nil, httpx.BadRequest("empty statement")
	case err != nil:
		return nil, err
	}
	rec.StatementClass, rec.Destructive = string(cls.Class), cls.Destructive

	if cls.Class == sqlclass.Session || cls.Class == sqlclass.NonTx {
		s.history(ctx, hist, string(cls.Class), "denied", nil, nil, cls.Reason)
		return nil, apiErr(http.StatusBadRequest, "unsupported_statement", cls.Reason).WithDetails(map[string]any{"class": cls.Class})
	}
	if in.Level != authz.Write && cls.Class != sqlclass.Read {
		msg := "your access to this database is read-only; " + string(cls.Class) + " statements are not allowed"
		if cls.Reason != "" {
			msg += " (" + cls.Reason + ")"
		}
		rec.Event, rec.Result = audit.EventAccessDenied, audit.ResultDenied
		s.Audit.Write(ctx, rec)
		s.history(ctx, hist, string(cls.Class), "denied", nil, nil, msg)
		metrics.Queries.WithLabelValues(in.Target.ID, string(cls.Class), "denied").Inc()
		return nil, apiErr(http.StatusForbidden, "forbidden_statement", msg).WithDetails(map[string]any{"class": cls.Class})
	}
	production := in.Target.Environment == "production"
	needConfirm := cls.Destructive || (production && cls.Class != sqlclass.Read)
	if needConfirm && !in.Confirm {
		rec.Event, rec.Result = audit.EventConfirmRequired, audit.ResultDenied
		s.Audit.Write(ctx, rec)
		return nil, apiErr(http.StatusConflict, "confirmation_required", "this statement needs confirmation").WithDetails(map[string]any{
			"class": cls.Class, "destructive": cls.Destructive, "summary": cls.Summary,
			"environment": in.Target.Environment, "requireTypedConfirmation": production,
		})
	}

	id := in.ClientQueryID
	if !clientID.MatchString(id) {
		id = ulid.Make().String()
	}
	qctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := s.Registry.Start(in.User.ID, id, s.Limits.ConcurrentQueriesPerUser, cancel); err != nil {
		if errors.Is(err, ErrTooMany) {
			return nil, apiErr(http.StatusTooManyRequests, "too_many_queries", "too many queries running; wait for one to finish or cancel it")
		}
		return nil, httpx.BadRequest("clientQueryId is already in use")
	}
	defer s.Registry.Finish(id)

	limit := in.Limit
	if limit <= 0 || limit > s.Limits.MaxRows {
		limit = s.Limits.MaxRows
	}
	out := &Output{QueryID: id, Class: cls.Class, Destructive: cls.Destructive, Notices: []pgconn.Notice{}}
	start := time.Now()
	// A writer's "read" may still write through a function the classifier
	// cannot see into: run it READ ONLY unless confirmed (ADR-027).
	readOnlyProbe := in.Level == authz.Write && cls.Class == sqlclass.Read && !in.Confirm
	err = s.Pools.Run(qctx, pgconn.Request{
		Target: in.Target, Database: in.Database, Level: in.Level, Email: in.User.Email, Reset: true, UserSQL: true,
		ReadOnly: readOnlyProbe, Notices: &out.Notices,
	}, func(ctx context.Context, tx pgx.Tx) error {
		// Rows changed by this statement, including writable CTEs, EXPLAIN
		// ANALYZE, cascades and triggers: the difference of the backend's
		// pending table counters around it (they may still hold earlier
		// transactions' counts, PostgreSQL 15+).
		countChanges := cls.Class == sqlclass.DML && !in.Confirm
		var before int64
		if countChanges {
			if err := tx.QueryRow(ctx, xactChanges).Scan(&before); err != nil {
				return err
			}
		}
		res, err := pgconn.QueryText(ctx, tx, cls.Statement, nil, pgconn.TextOptions{
			MaxRows: limit, MaxCellBytes: s.Limits.MaxCellBytes, Cursor: cls.Cursorable,
		})
		if err != nil {
			return err
		}
		out.Columns, out.Rows, out.Truncated, out.TruncatedCells, out.CommandTag = res.Columns, res.Rows, res.Truncated, res.TruncatedCells, res.CommandTag
		// Heuristics miss tautologies (WHERE id = id): an unconfirmed change
		// of many rows is rolled back and becomes a confirmation (ADR-027).
		if countChanges {
			var after int64
			if err := tx.QueryRow(ctx, xactChanges).Scan(&after); err != nil {
				return err
			}
			n := after - before
			if res.RowsAffected > n {
				n = res.RowsAffected
			}
			if n > ConfirmRowsAbove {
				return &manyRowsError{n: n}
			}
		}
		if cls.Class != sqlclass.Read {
			n := res.RowsAffected
			out.RowsAffected = &n
		}
		return nil
	})
	var many *manyRowsError
	if errors.As(err, &many) {
		rec.Event, rec.Result = audit.EventConfirmRequired, audit.ResultDenied
		s.Audit.Write(ctx, rec)
		return nil, apiErr(http.StatusConflict, "confirmation_required",
			fmt.Sprintf("this statement changes %d rows", many.n)).WithDetails(map[string]any{
			"class": cls.Class, "destructive": true, "summary": fmt.Sprintf("%s (%d rows)", cls.Summary, many.n),
			"rows": many.n, "environment": in.Target.Environment, "requireTypedConfirmation": in.Target.Environment == "production",
		})
	}
	var pe *pgxconn.PgError
	if readOnlyProbe && errors.As(err, &pe) && pe.Code == "25006" { // read_only_sql_transaction
		rec.Event, rec.Result, rec.StatementClass = audit.EventConfirmRequired, audit.ResultDenied, string(sqlclass.DML)
		s.Audit.Write(ctx, rec)
		return nil, apiErr(http.StatusConflict, "confirmation_required", "this statement writes data through a function").WithDetails(map[string]any{
			"class": sqlclass.DML, "destructive": false, "summary": cls.Summary, "reason": "writes data through a function",
			"environment": in.Target.Environment, "requireTypedConfirmation": in.Target.Environment == "production",
		})
	}
	if errors.As(err, &pe) && pe.Position > 0 {
		// the server saw cls.Statement; report positions in the user's text
		pe.Position += int32(utf8.RuneCountInString(in.SQL[:cls.Offset])) // #nosec G115 -- bounded by MaxStatementBytes
	}
	d := time.Since(start)
	out.DurationMs = d.Milliseconds()
	dms := out.DurationMs
	rec.DurationMs = &dms
	result, code := resultOf(err)
	rec.Result, rec.ErrorCode, rec.RowsAffected = result, code, out.RowsAffected
	s.Audit.Write(ctx, rec)
	metrics.Queries.WithLabelValues(in.Target.ID, string(cls.Class), result).Inc()
	metrics.QueryDuration.WithLabelValues(string(cls.Class)).Observe(d.Seconds())
	var rows *int64
	if out.RowsAffected != nil {
		rows = out.RowsAffected
	} else if err == nil {
		n := int64(len(out.Rows))
		rows = &n
	}
	errMsg := ""
	if err != nil {
		errMsg = errorText(err)
	}
	s.history(ctx, hist, string(cls.Class), result, &dms, rows, errMsg)
	if err != nil {
		return nil, err
	}
	if out.Columns == nil {
		out.Columns = []pgconn.Column{}
	}
	if out.Rows == nil {
		out.Rows = [][]*string{}
	}
	if out.TruncatedCells == nil {
		out.TruncatedCells = [][2]int{}
	}
	return out, nil
}

func resultOf(err error) (string, string) {
	var pe *pgxconn.PgError
	switch {
	case err == nil:
		return audit.ResultOK, ""
	case errors.Is(err, pgconn.ErrCancelled):
		return audit.ResultCancelled, "57014"
	case errors.Is(err, pgconn.ErrTimeout):
		return audit.ResultError, "57014"
	case errors.As(err, &pe):
		return audit.ResultError, pe.Code
	}
	return audit.ResultError, ""
}

func errorText(err error) string {
	var pe *pgxconn.PgError
	var ue *pgconn.ErrUnavailable
	switch {
	case errors.As(err, &pe):
		return pe.Message
	case errors.As(err, &ue):
		return "target unavailable"
	}
	return err.Error()
}

func (s *Service) history(ctx context.Context, h store.HistoryItem, class, result string, dur, rows *int64, errMsg string) {
	h.Class, h.Result, h.DurationMs, h.Rows = class, result, dur, rows
	if errMsg != "" {
		h.Error = &errMsg
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.Store.AddHistory(sctx, h, s.Limits.HistoryPerUser); err != nil {
		s.Log.Error("history write failed", "err", err)
	}
}

// LineCol converts a 1-based character position into line and column.
func LineCol(sql string, pos int) (int, int) {
	line, col, i := 1, 1, 0
	for _, r := range sql {
		i++
		if i >= pos {
			break
		}
		if r == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	return line, col
}

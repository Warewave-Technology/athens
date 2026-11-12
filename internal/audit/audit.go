// Package audit writes audit records to the store and to stdout as JSON
// ("log":"audit") so the cluster log pipeline keeps a copy (SPEC §10).
package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/Warewave-Technology/athens/internal/metrics"
	"github.com/Warewave-Technology/athens/internal/reqctx"
	"github.com/Warewave-Technology/athens/internal/store"
)

const (
	EventLogin           = "login"
	EventLoginDenied     = "login_denied"
	EventLogout          = "logout"
	EventQueryExecute    = "query_execute"
	EventQueryCancel     = "query_cancel"
	EventRowInsert       = "row_insert"
	EventRowUpdate       = "row_update"
	EventRowDelete       = "row_delete"
	EventAccessDenied    = "access_denied"
	EventConfirmRequired = "confirm_required"

	// Reads (audit.logReads) and views of the admin pages.
	EventRowsRead      = "rows_read"
	EventCellRead      = "cell_read"
	EventStructureView = "structure_view"
	EventAdminView     = "admin_view"
	EventAuditExport   = "audit_export"

	// Changes to the user's own stored items. Drafts and UI state are
	// autosaved on every edit and are not audited.
	EventSavedQueryCreate = "saved_query_create"
	EventSavedQueryUpdate = "saved_query_update"
	EventSavedQueryDelete = "saved_query_delete"
	EventHistoryDelete    = "history_delete"

	ResultOK        = "ok"
	ResultError     = "error"
	ResultDenied    = "denied"
	ResultCancelled = "cancelled"

	maxSQLBytes = 8 << 10
)

// Writer is safe for concurrent use.
type Writer struct {
	store  store.Store
	log    *slog.Logger
	logSQL bool
}

func NewWriter(s store.Store, stdout *slog.Logger, logSQL bool) *Writer {
	return &Writer{store: s, log: stdout.With("log", "audit"), logSQL: logSQL}
}

// Write records rec. User and request metadata are filled from ctx when unset.
// A store failure never blocks the caller: it is logged and counted.
func (w *Writer) Write(ctx context.Context, rec store.AuditRecord) {
	if rec.TS.IsZero() {
		rec.TS = time.Now()
	}
	if u := reqctx.UserFrom(ctx); u != nil && rec.UserID == "" {
		rec.UserID, rec.Email = u.ID, u.Email
	}
	if m := reqctx.MetaFrom(ctx); rec.ClientIP == "" {
		rec.ClientIP, rec.UserAgent = m.ClientIP, truncate(m.UserAgent, 512)
	}
	if !w.logSQL {
		rec.SQL = ""
	}
	rec.SQL = truncate(rec.SQL, maxSQLBytes)

	attrs := []any{
		"event", rec.Event, "ts", rec.TS.UTC().Format(time.RFC3339Nano), "result", rec.Result,
		"user_id", rec.UserID, "email", rec.Email, "client_ip", rec.ClientIP,
	}
	add := func(k, v string) {
		if v != "" {
			attrs = append(attrs, k, v)
		}
	}
	add("user_agent", rec.UserAgent)
	add("target_id", rec.TargetID)
	add("database", rec.Database)
	add("object", rec.Object)
	add("statement_class", rec.StatementClass)
	add("sql", rec.SQL)
	add("error_code", rec.ErrorCode)
	add("details", rec.Details)
	if rec.Destructive {
		attrs = append(attrs, "destructive", true)
	}
	if rec.RowsAffected != nil {
		attrs = append(attrs, "rows_affected", *rec.RowsAffected)
	}
	if rec.DurationMs != nil {
		attrs = append(attrs, "duration_ms", *rec.DurationMs)
	}
	w.log.Info("audit", attrs...)

	// Detach from request cancellation: the record must be stored even if the
	// client went away.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := w.store.InsertAudit(sctx, rec); err != nil {
		metrics.AuditWriteErrors.Inc()
		w.log.Error("audit store write failed", "err", err, "event", rec.Event)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// keep valid UTF-8
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

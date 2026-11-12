package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Warewave-Technology/athens/internal/audit"
	"github.com/Warewave-Technology/athens/internal/authz"
	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/httpx"
	"github.com/Warewave-Technology/athens/internal/pgconn"
	"github.com/Warewave-Technology/athens/internal/reqctx"
	"github.com/Warewave-Technology/athens/internal/rows"
	"github.com/Warewave-Technology/athens/internal/store"
)

const maxBulkDelete = 100

var (
	errRowConflict = httpx.NewError(http.StatusConflict, "row_conflict",
		"the row was changed or deleted by someone else; reload it and try again")
	errReadOnlyAccess = httpx.NewError(http.StatusForbidden, "forbidden", "your access to this database is read-only")
)

// errConflict aborts the write transaction (ROLLBACK) and maps to 409.
var errConflict = errors.New("row conflict")

type writeCtx struct {
	target discovery.Target
	db     string
	schema string
	rel    string
}

// writable checks level, environment confirmation and table editability.
func (s *Server) writable(r *http.Request, confirm bool) (*writeCtx, error) {
	if credentialsCatalog(r) {
		return nil, httpx.ErrNotFound
	}
	t, db, lvl, err := s.targetDB(r)
	if err != nil {
		return nil, err
	}
	w := &writeCtx{target: t, db: db, schema: r.PathValue("schema"), rel: r.PathValue("rel")}
	if lvl != authz.Write {
		s.Audit.Write(r.Context(), store.AuditRecord{Event: audit.EventAccessDenied, Result: audit.ResultDenied,
			TargetID: t.ID, Database: db, Object: w.schema + "." + w.rel, Details: `{"operation":"grid_write"}`})
		return nil, errReadOnlyAccess
	}
	if t.Environment == "production" && !confirm {
		s.Audit.Write(r.Context(), store.AuditRecord{Event: audit.EventConfirmRequired, Result: audit.ResultDenied,
			TargetID: t.ID, Database: db, Object: w.schema + "." + w.rel, StatementClass: "dml"})
		return nil, httpx.NewError(http.StatusConflict, "confirmation_required", "writes to production targets need confirmation").
			WithDetails(map[string]any{"class": "dml", "destructive": false, "summary": "change rows of " + w.schema + "." + w.rel,
				"environment": t.Environment, "requireTypedConfirmation": true})
	}
	return w, nil
}

// runWrite loads the table, checks editability and runs fn in a read-write
// transaction.
func (s *Server) runWrite(r *http.Request, w *writeCtx, fn func(ctx context.Context, tx pgx.Tx, tbl rows.Table) error) error {
	return s.Pools.Run(r.Context(), pgconn.Request{
		Target: w.target, Database: w.db, Level: authz.Write, Email: reqctx.UserFrom(r.Context()).Email,
	}, func(ctx context.Context, tx pgx.Tx) error {
		tbl, _, kind, err := loadTable(ctx, tx, w.schema, w.rel)
		if err != nil {
			return err
		}
		if ok, reason := editability(kind, len(tbl.PK) > 0, authz.Write); !ok {
			return httpx.NewError(http.StatusForbidden, "not_editable", "rows of this relation cannot be edited: "+*reason).
				WithDetails(map[string]string{"reason": *reason})
		}
		return fn(ctx, tx, tbl)
	})
}

func (s *Server) writeError(err error) error {
	var he *httpx.Error
	var ie *rows.InvalidError
	switch {
	case errors.As(err, &he):
		return he
	case errors.As(err, &ie):
		return httpx.BadRequest(ie.Msg)
	case errors.Is(err, errConflict):
		return errRowConflict
	}
	return s.mapDBError(err, "")
}

// auditWrite records a grid write. Values are only logged with audit.logValues.
func (s *Server) auditWrite(r *http.Request, w *writeCtx, event string, start time.Time, n int64, err error, details map[string]any) {
	rec := store.AuditRecord{Event: event, TargetID: w.target.ID, Database: w.db, Object: w.schema + "." + w.rel,
		StatementClass: "dml", Result: audit.ResultOK}
	d := time.Since(start).Milliseconds()
	rec.DurationMs = &d
	if err != nil {
		rec.Result, rec.ErrorCode = audit.ResultError, errCode(err)
	} else {
		rec.RowsAffected = &n
	}
	b, _ := json.Marshal(details)
	rec.Details = string(b)
	s.Audit.Write(r.Context(), rec)
}

func errCode(err error) string {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Code
	}
	if errors.Is(err, errConflict) {
		return "row_conflict"
	}
	return "db_error"
}

func (s *Server) values(v map[string]*string) any {
	if s.Config.Audit.LogValues {
		return v
	}
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	return keys
}

func rowOf(ctx context.Context, tx pgx.Tx, st *rows.Statement, maxCell int) ([]*string, int64, error) {
	res, err := pgconn.QueryText(ctx, tx, st.SQL, st.Params, pgconn.TextOptions{MaxRows: 1, MaxCellBytes: maxCell, ParamOIDs: st.OIDs})
	if err != nil {
		return nil, 0, err
	}
	if len(res.Rows) == 0 {
		return nil, res.RowsAffected, nil
	}
	return res.Rows[0], res.RowsAffected, nil
}

type insertRequest struct {
	Values  map[string]*string `json:"values"`
	Confirm bool               `json:"confirm"`
}

func (s *Server) insertRow(w http.ResponseWriter, r *http.Request) error {
	if _, _, _, err := s.targetDB(r); err != nil {
		return err // 404 before anything else (no enumeration)
	}
	var req insertRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	wc, err := s.writable(r, req.Confirm)
	if err != nil {
		return err
	}
	start := time.Now()
	var row []*string
	err = s.runWrite(r, wc, func(ctx context.Context, tx pgx.Tx, tbl rows.Table) error {
		st, err := rows.Insert(tbl, req.Values)
		if err != nil {
			return err
		}
		row, _, err = rowOf(ctx, tx, st, s.Config.Limits.MaxCellBytes)
		return err
	})
	s.auditWrite(r, wc, audit.EventRowInsert, start, 1, err, map[string]any{"columns": s.values(req.Values)})
	if err != nil {
		return s.writeError(err)
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"row": row})
	return nil
}

type updateRequest struct {
	PK       map[string]*string `json:"pk"`
	Set      map[string]*string `json:"set"`
	Original map[string]*string `json:"original"`
	Confirm  bool               `json:"confirm"`
}

func (s *Server) updateRow(w http.ResponseWriter, r *http.Request) error {
	if _, _, _, err := s.targetDB(r); err != nil {
		return err // 404 before anything else (no enumeration)
	}
	var req updateRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	wc, err := s.writable(r, req.Confirm)
	if err != nil {
		return err
	}
	start := time.Now()
	var row []*string
	err = s.runWrite(r, wc, func(ctx context.Context, tx pgx.Tx, tbl rows.Table) error {
		st, err := rows.Update(tbl, req.PK, req.Set, req.Original)
		if err != nil {
			return err
		}
		var n int64
		row, n, err = rowOf(ctx, tx, st, s.Config.Limits.MaxCellBytes)
		if err == nil && n != 1 {
			return errConflict
		}
		return err
	})
	s.auditWrite(r, wc, audit.EventRowUpdate, start, 1, err, map[string]any{"pk": req.PK, "columns": s.values(req.Set)})
	if err != nil {
		return s.writeError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"row": row})
	return nil
}

type deleteRequest struct {
	PKs     []map[string]*string `json:"pks"`
	Confirm bool                 `json:"confirm"`
}

func (s *Server) deleteRows(w http.ResponseWriter, r *http.Request) error {
	if _, _, _, err := s.targetDB(r); err != nil {
		return err // 404 before anything else (no enumeration)
	}
	var req deleteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	if len(req.PKs) == 0 || len(req.PKs) > maxBulkDelete {
		return httpx.BadRequest("pks must contain 1 to 100 primary keys")
	}
	wc, err := s.writable(r, req.Confirm)
	if err != nil {
		return err
	}
	start := time.Now()
	var deleted int64
	err = s.runWrite(r, wc, func(ctx context.Context, tx pgx.Tx, tbl rows.Table) error {
		for _, pk := range req.PKs {
			st, err := rows.Delete(tbl, pk)
			if err != nil {
				return err
			}
			res, err := pgconn.QueryText(ctx, tx, st.SQL, st.Params, pgconn.TextOptions{MaxRows: 0, ParamOIDs: st.OIDs})
			if err != nil {
				return err
			}
			if res.RowsAffected != 1 {
				return errConflict // all or nothing
			}
			deleted++
		}
		return nil
	})
	s.auditWrite(r, wc, audit.EventRowDelete, start, deleted, err, map[string]any{"pks": req.PKs})
	if err != nil {
		return s.writeError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
	return nil
}

package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	pgxconn "github.com/jackc/pgx/v5/pgconn"

	"github.com/Warewave-Technology/athens/internal/audit"
	"github.com/Warewave-Technology/athens/internal/authz"
	"github.com/Warewave-Technology/athens/internal/catalog"
	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/httpx"
	"github.com/Warewave-Technology/athens/internal/pgconn"
	"github.com/Warewave-Technology/athens/internal/query"
	"github.com/Warewave-Technology/athens/internal/reqctx"
)

const maxRelations = 20000

var (
	errTargetUnavailable = httpx.NewError(http.StatusBadGateway, "target_unavailable", "target unavailable")
	errTimeout           = httpx.NewError(http.StatusGatewayTimeout, "timeout", "statement timeout exceeded")
	errCancelled         = httpx.NewError(499, "cancelled", "the operation was cancelled")
)

// DBErrorDetails is the 422 db_error payload (API.md).
type DBErrorDetails struct {
	SQLState string `json:"sqlstate"`
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
	Hint     string `json:"hint,omitempty"`
	Position int32  `json:"position,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
}

// mapDBError converts pgconn/catalog errors into API errors. sql is used to
// turn a character position into line/column.
func (s *Server) mapDBError(err error, sql string) error {
	var ue *pgconn.ErrUnavailable
	var pe *pgxconn.PgError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ue):
		s.Log.Warn("target unavailable", "err", ue.Cause)
		return errTargetUnavailable
	case errors.Is(err, pgconn.ErrTimeout):
		return errTimeout
	case errors.Is(err, pgconn.ErrCancelled), errors.Is(err, context.Canceled):
		return errCancelled
	case errors.Is(err, catalog.ErrNotFound):
		return httpx.ErrNotFound
	case errors.As(err, &pe):
		d := DBErrorDetails{SQLState: pe.Code, Message: pe.Message, Detail: pe.Detail, Hint: pe.Hint, Position: pe.Position}
		if pe.Position > 0 && sql != "" {
			d.Line, d.Column = query.LineCol(sql, int(pe.Position))
		}
		return httpx.NewError(http.StatusUnprocessableEntity, "db_error", pe.Message).WithDetails(d)
	}
	return err
}

// target resolves {ns}/{name} for the current user; unknown or invisible
// targets are 404 (no enumeration).
func (s *Server) target(r *http.Request) (discovery.Target, error) {
	u := reqctx.UserFrom(r.Context())
	t, ok := s.Registry.Get(r.PathValue("ns") + "/" + r.PathValue("name"))
	if !ok || s.Authz.TargetLevel(u.Groups, t) == authz.None {
		return discovery.Target{}, httpx.ErrNotFound
	}
	return t, nil
}

// targetDB resolves target + {db} and the effective level for it.
func (s *Server) targetDB(r *http.Request) (discovery.Target, string, authz.Level, error) {
	t, err := s.target(r)
	if err != nil {
		return t, "", authz.None, err
	}
	db := r.PathValue("db")
	lvl := s.Authz.Effective(reqctx.UserFrom(r.Context()).Groups, t, db)
	if db == "" || lvl == authz.None {
		return t, "", authz.None, httpx.ErrNotFound
	}
	return t, db, lvl, nil
}

func (s *Server) dbReq(r *http.Request, t discovery.Target, db string, lvl authz.Level) pgconn.Request {
	return pgconn.Request{Target: t, Database: db, Level: lvl, Email: reqctx.UserFrom(r.Context()).Email, ReadOnly: true}
}

type targetJSON struct {
	ID          string   `json:"id"`
	Namespace   string   `json:"namespace"`
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName"`
	Source      string   `json:"source"`
	Environment string   `json:"environment"`
	Level       string   `json:"level"`
	Status      string   `json:"status"`
	Warnings    []string `json:"warnings,omitempty"`
}

func (s *Server) listTargets(w http.ResponseWriter, r *http.Request) error {
	u := reqctx.UserFrom(r.Context())
	admin := s.Authz.IsAdmin(u.Groups)
	out := []targetJSON{}
	for _, t := range s.Registry.List() {
		lvl := s.Authz.TargetLevel(u.Groups, t)
		if lvl == authz.None {
			continue
		}
		j := targetJSON{ID: t.ID, Namespace: t.Namespace, Name: t.Name, DisplayName: t.DisplayName, Source: string(t.Source),
			Environment: t.Environment, Level: lvl.String(), Status: s.Pools.Status(t.ID)}
		if admin {
			j.Warnings = t.Warnings
			if s.Pools.ROFallback(t.ID) {
				j.Warnings = append(j.Warnings, "read-only credential was rejected; using rw credential with READ ONLY transactions")
			}
		}
		out = append(out, j)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"targets": out})
	return nil
}

type databaseJSON struct {
	Name      string `json:"name"`
	Level     string `json:"level"`
	SizeBytes *int64 `json:"sizeBytes"`
}

func (s *Server) listDatabases(w http.ResponseWriter, r *http.Request) error {
	t, err := s.target(r)
	if err != nil {
		return err
	}
	u := reqctx.UserFrom(r.Context())
	lvl := s.Authz.TargetLevel(u.Groups, t)
	var dbs []catalog.Database
	err = s.Pools.Run(r.Context(), pgconn.Request{Target: t, Level: lvl, Email: u.Email, ReadOnly: true},
		func(ctx context.Context, tx pgx.Tx) error {
			var err error
			dbs, err = catalog.Databases(ctx, tx)
			return err
		})
	if err != nil {
		return s.mapDBError(err, "")
	}
	out := []databaseJSON{}
	for _, d := range dbs {
		if l := s.Authz.Effective(u.Groups, t, d.Name); l != authz.None {
			out = append(out, databaseJSON{Name: d.Name, Level: l.String(), SizeBytes: d.SizeBytes})
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"databases": out})
	return nil
}

func (s *Server) listSchemas(w http.ResponseWriter, r *http.Request) error {
	t, db, lvl, err := s.targetDB(r)
	if err != nil {
		return err
	}
	var out []catalog.Schema
	err = s.Pools.Run(r.Context(), s.dbReq(r, t, db, lvl), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = catalog.Schemas(ctx, tx, r.URL.Query().Get("system") == "true")
		return err
	})
	if err != nil {
		return s.mapDBError(err, "")
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"schemas": out})
	return nil
}

type relationJSON struct {
	catalog.Relation
	Editable       bool    `json:"editable"`
	EditableReason *string `json:"editableReason"`
}

// editability returns whether rows of rel may be edited at level lvl.
func editability(kind catalog.Kind, hasPK bool, lvl authz.Level) (bool, *string) {
	reason := ""
	switch {
	case kind != catalog.KindTable && kind != catalog.KindPartitioned:
		reason = "not_a_table"
	case !hasPK:
		reason = "no_primary_key"
	case lvl != authz.Write:
		reason = "read_only_access"
	}
	if reason == "" {
		return true, nil
	}
	return false, &reason
}

func (s *Server) listRelations(w http.ResponseWriter, r *http.Request) error {
	t, db, lvl, err := s.targetDB(r)
	if err != nil {
		return err
	}
	var rels []catalog.Relation
	var capped bool
	err = s.Pools.Run(r.Context(), s.dbReq(r, t, db, lvl), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		rels, capped, err = catalog.Relations(ctx, tx, r.URL.Query().Get("system") == "true", maxRelations)
		return err
	})
	if err != nil {
		return s.mapDBError(err, "")
	}
	out := make([]relationJSON, len(rels))
	for i, rel := range rels {
		ok, reason := editability(rel.Kind, rel.HasPK, lvl)
		out[i] = relationJSON{Relation: rel, Editable: ok, EditableReason: reason}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"relations": out, "capped": capped})
	return nil
}

func (s *Server) structure(w http.ResponseWriter, r *http.Request) error {
	t, db, lvl, err := s.targetDB(r)
	if err != nil {
		return err
	}
	var st *catalog.Structure
	err = s.Pools.Run(r.Context(), s.dbReq(r, t, db, lvl), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		st, err = catalog.GetStructure(ctx, tx, r.PathValue("schema"), r.PathValue("rel"))
		return err
	})
	if err != nil {
		return s.mapDBError(err, "")
	}
	s.auditRead(r, audit.EventStructureView, t, db, -1, -1, nil)
	httpx.WriteJSON(w, http.StatusOK, st)
	return nil
}

func (s *Server) completion(w http.ResponseWriter, r *http.Request) error {
	t, db, lvl, err := s.targetDB(r)
	if err != nil {
		return err
	}
	var cols map[string][]string
	err = s.Pools.Run(r.Context(), s.dbReq(r, t, db, lvl), func(ctx context.Context, tx pgx.Tx) error {
		var err error
		cols, err = catalog.CompletionColumns(ctx, tx, 100000)
		return err
	})
	if err != nil {
		return s.mapDBError(err, "")
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tables": cols})
	return nil
}

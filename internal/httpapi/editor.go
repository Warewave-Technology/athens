package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Warewave-Technology/athens/internal/audit"
	"github.com/Warewave-Technology/athens/internal/httpx"
	"github.com/Warewave-Technology/athens/internal/query"
	"github.com/Warewave-Technology/athens/internal/reqctx"
	"github.com/Warewave-Technology/athens/internal/store"
)

type executeRequest struct {
	SQL           string `json:"sql"`
	Limit         int    `json:"limit"`
	Confirm       bool   `json:"confirm"`
	ClientQueryID string `json:"clientQueryId"`
}

func (s *Server) executeQuery(w http.ResponseWriter, r *http.Request) error {
	t, db, lvl, err := s.targetDB(r)
	if err != nil {
		return err
	}
	var req executeRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	out, err := s.Query.Execute(r.Context(), query.Input{
		User: reqctx.UserFrom(r.Context()), Target: t, Database: db, Level: lvl,
		SQL: req.SQL, Limit: req.Limit, Confirm: req.Confirm, ClientQueryID: req.ClientQueryID,
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return he
		}
		return s.mapDBError(err, req.SQL)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) cancelQuery(w http.ResponseWriter, r *http.Request) error {
	u := reqctx.UserFrom(r.Context())
	id := r.PathValue("id")
	owner, err := s.Query.Registry.Cancel(id, u.ID, s.Authz.IsAdmin(u.Groups))
	if err != nil {
		return httpx.ErrNotFound
	}
	details, _ := json.Marshal(map[string]string{"queryId": id, "owner": owner})
	s.Audit.Write(r.Context(), store.AuditRecord{Event: audit.EventQueryCancel, Result: audit.ResultOK, Details: string(details)})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) listHistory(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	items, err := s.Store.ListHistory(r.Context(), reqctx.UserFrom(r.Context()).ID, store.HistoryFilter{
		TargetID: q.Get("targetId"), Database: q.Get("database"), Limit: limit, Before: before,
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (s *Server) deleteHistory(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return httpx.ErrNotFound
	}
	if err := s.Store.DeleteHistory(r.Context(), reqctx.UserFrom(r.Context()).ID, id); err != nil {
		return mapStoreErr(err)
	}
	s.auditUserItem(r, audit.EventHistoryDelete, id, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type savedQueryRequest struct {
	Name     string  `json:"name"`
	SQL      string  `json:"sql"`
	TargetID *string `json:"targetId"`
	Database *string `json:"database"`
}

func (q savedQueryRequest) validate() error {
	if strings.TrimSpace(q.Name) == "" || len(q.Name) > 200 {
		return httpx.BadRequest("name must be 1-200 characters")
	}
	if q.SQL == "" || len(q.SQL) > query.MaxStatementBytes {
		return httpx.BadRequest("sql must be 1 byte to 256 KiB")
	}
	return nil
}

func (s *Server) listSavedQueries(w http.ResponseWriter, r *http.Request) error {
	items, err := s.Store.ListSavedQueries(r.Context(), reqctx.UserFrom(r.Context()).ID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (s *Server) createSavedQuery(w http.ResponseWriter, r *http.Request) error {
	var req savedQueryRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	if err := req.validate(); err != nil {
		return err
	}
	q, err := s.Store.CreateSavedQuery(r.Context(), store.SavedQuery{
		UserID: reqctx.UserFrom(r.Context()).ID, Name: strings.TrimSpace(req.Name), SQL: req.SQL, TargetID: req.TargetID, Database: req.Database,
	})
	if err != nil {
		return mapStoreErr(err)
	}
	s.auditUserItem(r, audit.EventSavedQueryCreate, q.ID, q)
	httpx.WriteJSON(w, http.StatusCreated, q)
	return nil
}

func (s *Server) updateSavedQuery(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return httpx.ErrNotFound
	}
	var req savedQueryRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	if err := req.validate(); err != nil {
		return err
	}
	q, err := s.Store.UpdateSavedQuery(r.Context(), store.SavedQuery{
		ID: id, UserID: reqctx.UserFrom(r.Context()).ID, Name: strings.TrimSpace(req.Name), SQL: req.SQL, TargetID: req.TargetID, Database: req.Database,
	})
	if err != nil {
		return mapStoreErr(err)
	}
	s.auditUserItem(r, audit.EventSavedQueryUpdate, q.ID, q)
	httpx.WriteJSON(w, http.StatusOK, q)
	return nil
}

func (s *Server) deleteSavedQuery(w http.ResponseWriter, r *http.Request) error {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return httpx.ErrNotFound
	}
	if err := s.Store.DeleteSavedQuery(r.Context(), reqctx.UserFrom(r.Context()).ID, id); err != nil {
		return mapStoreErr(err)
	}
	s.auditUserItem(r, audit.EventSavedQueryDelete, id, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// auditUserItem records a change to the user's saved queries or history.
// The saved SQL goes into the sql field (subject to audit.logSQL).
func (s *Server) auditUserItem(r *http.Request, event string, id int64, q *store.SavedQuery) {
	rec := store.AuditRecord{Event: event, Result: audit.ResultOK}
	details := map[string]any{"id": id}
	if q != nil {
		details["name"] = q.Name
		rec.SQL = q.SQL
		if q.TargetID != nil {
			rec.TargetID = *q.TargetID
		}
		if q.Database != nil {
			rec.Database = *q.Database
		}
	}
	b, _ := json.Marshal(details)
	rec.Details = string(b)
	s.Audit.Write(r.Context(), rec)
}

func draftTarget(r *http.Request) string { return r.PathValue("ns") + "/" + r.PathValue("name") }

func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) error {
	d, err := s.Store.GetDraft(r.Context(), reqctx.UserFrom(r.Context()).ID, draftTarget(r), r.PathValue("db"))
	if err != nil {
		return mapStoreErr(err)
	}
	httpx.WriteJSON(w, http.StatusOK, d)
	return nil
}

func (s *Server) putDraft(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		SQL string `json:"sql"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	if len(req.SQL) > query.MaxStatementBytes {
		return httpx.NewError(http.StatusRequestEntityTooLarge, "too_large", "draft exceeds 256 KiB")
	}
	d, err := s.Store.PutDraft(r.Context(), reqctx.UserFrom(r.Context()).ID, draftTarget(r), r.PathValue("db"), req.SQL)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, d)
	return nil
}

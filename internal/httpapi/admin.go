package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/httpx"
	"github.com/Warewave-Technology/athens/internal/reqctx"
	"github.com/Warewave-Technology/athens/internal/store"
)

// requireAdmin returns 404 for non-admins (no enumeration, SPEC §2).
func (s *Server) requireAdmin(r *http.Request) error {
	u := reqctx.UserFrom(r.Context())
	if u == nil || !s.Authz.IsAdmin(u.Groups) {
		return httpx.ErrNotFound
	}
	return nil
}

type adminDiscoveryResponse struct {
	Targets    []discovery.Target          `json:"targets"`
	Namespaces []discovery.NamespaceStatus `json:"namespaces"`
	Synced     bool                        `json:"synced"`
}

func (s *Server) adminDiscovery(w http.ResponseWriter, r *http.Request) error {
	if err := s.requireAdmin(r); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, adminDiscoveryResponse{
		Targets: s.Registry.List(), Namespaces: s.Registry.Status(), Synced: s.Ready == nil || s.Ready(),
	})
	return nil
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) error {
	if err := s.requireAdmin(r); err != nil {
		return err
	}
	q := r.URL.Query()
	f := store.AuditFilter{User: q.Get("user"), TargetID: q.Get("targetId"), Event: q.Get("event")}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Before, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	for name, dst := range map[string]*time.Time{"from": &f.From, "to": &f.To} {
		if v := q.Get(name); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return httpx.BadRequest(name + " must be RFC3339")
			}
			*dst = t
		}
	}
	recs, err := s.Store.ListAudit(r.Context(), f)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": recs})
	return nil
}

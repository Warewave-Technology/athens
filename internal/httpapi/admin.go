package httpapi

import (
	"net/http"

	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/httpx"
	"github.com/Warewave-Technology/athens/internal/reqctx"
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

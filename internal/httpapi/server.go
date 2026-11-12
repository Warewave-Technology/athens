// Package httpapi wires routes, middleware and handlers.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Warewave-Technology/athens/internal/audit"
	"github.com/Warewave-Technology/athens/internal/auth"
	"github.com/Warewave-Technology/athens/internal/authz"
	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/httpx"
	"github.com/Warewave-Technology/athens/internal/pgconn"
	"github.com/Warewave-Technology/athens/internal/query"
	"github.com/Warewave-Technology/athens/internal/reqctx"
	"github.com/Warewave-Technology/athens/internal/store"
	"github.com/Warewave-Technology/athens/internal/version"
)

const maxBodyBytes = 1 << 20

// Deps are the collaborators of the HTTP layer.
type Deps struct {
	Config   *config.Config
	Log      *slog.Logger
	Store    store.Store
	Auth     *auth.Authenticator
	Authz    *authz.Authorizer
	Audit    *audit.Writer
	Registry *discovery.Registry
	Pools    *pgconn.Manager
	Query    *query.Service
	SPA      http.Handler
	// Dev relaxes the CSP for the Vite dev server (--dev).
	Dev bool
	// Ready reports readiness of components beyond the store and OIDC
	// (discovery cache sync). nil means ready.
	Ready func() bool
}

type Server struct {
	Deps
	api *http.ServeMux
}

func New(d Deps) *Server {
	return &Server{Deps: d, api: http.NewServeMux()}
}

// Handler builds the full handler tree.
func (s *Server) Handler() http.Handler {
	root := http.NewServeMux()
	root.Handle("GET /healthz", route("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))
	root.Handle("GET /readyz", route("GET /readyz", http.HandlerFunc(s.readyz)))

	session := func(h http.Handler) http.Handler { return s.Auth.LoadSession(publishUser(h)) }
	root.Handle("GET /auth/login", route("GET /auth/login", http.HandlerFunc(s.Auth.HandleLogin)))
	cb := "GET " + s.Config.OIDC.RedirectPath
	root.Handle(cb, route(cb, http.HandlerFunc(s.Auth.HandleCallback)))
	root.Handle("POST /auth/logout", route("POST /auth/logout", session(http.HandlerFunc(s.Auth.HandleLogout))))

	s.routes()
	root.Handle("/api/", session(auth.RequireUser(s.Auth.CSRF(limitBody(maxBodyBytes, s.api)))))

	spa := s.SPA
	root.Handle("/", route("GET /{spa...}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static assets are public; navigations need a session so the app
		// shell never renders for anonymous users.
		if strings.HasPrefix(r.URL.Path, "/assets/") || r.URL.Path == "/favicon.svg" {
			spa.ServeHTTP(w, r)
			return
		}
		session(auth.RequireUser(spa)).ServeHTTP(w, r)
	})))

	return securityHeaders(!s.Config.Server.InsecureDev, s.Dev, withUserHolder(observe(s.Log, root)))
}

// handle registers an API route; pattern is relative to /api/v1.
func (s *Server) handle(pattern string, h func(http.ResponseWriter, *http.Request) error) {
	method, p, _ := strings.Cut(pattern, " ")
	full := method + " /api/v1" + p
	s.api.Handle(full, route(full, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			if _, ok := err.(*httpx.Error); !ok {
				s.Log.Error("handler error", "route", full, "err", err)
			}
			httpx.WriteError(w, err)
		}
	})))
}

func (s *Server) routes() {
	s.handle("GET /me", s.me)
	s.handle("GET /admin/discovery", s.adminDiscovery)
	s.handle("GET /admin/pools", s.adminPools)
	s.handle("GET /targets", s.listTargets)
	s.handle("GET /targets/{ns}/{name}/databases", s.listDatabases)
	const db = "/targets/{ns}/{name}/db/{db}"
	s.handle("GET "+db+"/schemas", s.listSchemas)
	s.handle("GET "+db+"/relations", s.listRelations)
	s.handle("GET "+db+"/relations/{schema}/{rel}/structure", s.structure)
	s.handle("POST "+db+"/relations/{schema}/{rel}/rows/query", s.rowsQuery)
	s.handle("GET "+db+"/relations/{schema}/{rel}/rows/cell", s.rowsCell)
	s.handle("GET "+db+"/completion", s.completion)
	s.handle("POST "+db+"/query", s.executeQuery)
	s.handle("POST /queries/{id}/cancel", s.cancelQuery)
	s.handle("GET /history", s.listHistory)
	s.handle("DELETE /history/{id}", s.deleteHistory)
	s.handle("GET /saved-queries", s.listSavedQueries)
	s.handle("POST /saved-queries", s.createSavedQuery)
	s.handle("PUT /saved-queries/{id}", s.updateSavedQuery)
	s.handle("DELETE /saved-queries/{id}", s.deleteSavedQuery)
	s.handle("GET /drafts/{ns}/{name}/{db}", s.getDraft)
	s.handle("PUT /drafts/{ns}/{name}/{db}", s.putDraft)
	s.handle("GET /ui-state/{key}", s.getUIState)
	s.handle("PUT /ui-state/{key}", s.putUIState)
	s.handle("GET /version", func(w http.ResponseWriter, _ *http.Request) error {
		httpx.WriteJSON(w, http.StatusOK, version.Get())
		return nil
	})
	s.api.Handle("/api/", route("/api/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteError(w, httpx.ErrNotFound)
	})))
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	checks := map[string]bool{
		"store":     s.Store.Ping(ctx) == nil,
		"oidc":      s.Auth.Ready(),
		"discovery": s.Ready == nil || s.Ready(),
	}
	status := http.StatusOK
	for _, ok := range checks {
		if !ok {
			status = http.StatusServiceUnavailable
		}
	}
	httpx.WriteJSON(w, status, checks)
}

type meResponse struct {
	ID             string `json:"id"`
	Email          string `json:"email"`
	Name           string `json:"name"`
	IsAdmin        bool   `json:"isAdmin"`
	ReadOnlyGlobal bool   `json:"readOnlyGlobal"`
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) error {
	u := reqctx.UserFrom(r.Context())
	httpx.WriteJSON(w, http.StatusOK, meResponse{
		ID: u.ID, Email: u.Email, Name: u.Name, IsAdmin: s.Authz.IsAdmin(u.Groups), ReadOnlyGlobal: s.Authz.ReadOnlyGlobal(),
	})
	return nil
}

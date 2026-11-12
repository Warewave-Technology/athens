package auth

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/Warewave-Technology/athens/internal/httpx"
	"github.com/Warewave-Technology/athens/internal/reqctx"
	"github.com/Warewave-Technology/athens/internal/store"
	"github.com/Warewave-Technology/athens/internal/version"
)

// LoadSession resolves the session cookie and stores the user in the request
// context. Invalid, expired or idle sessions are deleted and ignored.
func (a *Authenticator) LoadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := reqctx.WithMeta(r.Context(), a.meta(r))
		if c, err := r.Cookie(version.SessionCookie); err == nil && c.Value != "" {
			id := hashToken(c.Value)
			s, err := a.store.GetSession(ctx, id)
			switch {
			case errors.Is(err, store.ErrNotFound):
			case err != nil:
				a.log.Error("load session", "err", err)
				httpx.WriteError(w, httpx.ErrInternal)
				return
			default:
				now := a.now()
				if !now.Before(s.ExpiresAt) || now.Sub(s.LastSeenAt) >= a.cfg.Auth.IdleTTL.D() {
					_ = a.store.DeleteSession(ctx, id)
					a.clearSessionCookies(w)
					break
				}
				if now.Sub(s.LastSeenAt) > touchInterval {
					if err := a.store.TouchSession(ctx, id, now); err != nil {
						a.log.Warn("touch session", "err", err)
					}
				}
				ctx = reqctx.WithUser(ctx, &reqctx.User{
					ID: s.UserID, Email: s.Email, Name: s.Name, Groups: s.Groups, SessionID: s.ID, CSRFToken: s.CSRFToken,
				})
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireUser rejects anonymous requests: 401 JSON under /api, otherwise a
// redirect to the login page that returns to the current path.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reqctx.UserFrom(r.Context()) == nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				httpx.WriteError(w, httpx.ErrUnauthenticated)
				return
			}
			http.Redirect(w, r, "/auth/login?"+url.Values{"returnTo": {SafeReturnTo(r.URL.RequestURI())}}.Encode(), http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// CSRF enforces SPEC §5.4 on unsafe methods: the X-Athens-CSRF header must
// equal the session token and Origin (or Referer) must match baseURL.
func (a *Authenticator) CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		u := reqctx.UserFrom(r.Context())
		if u == nil || !constantEqual(r.Header.Get(version.CSRFHeader), u.CSRFToken) || !a.originOK(r) {
			httpx.WriteError(w, httpx.ErrCSRF)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originOK compares Origin (preferred) or Referer with baseURL. A request
// with neither header is rejected.
func (a *Authenticator) originOK(r *http.Request) bool {
	want := a.baseURL.Scheme + "://" + a.baseURL.Host
	if o := r.Header.Get("Origin"); o != "" {
		return strings.EqualFold(o, want)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		return err == nil && strings.EqualFold(u.Scheme+"://"+u.Host, want)
	}
	return false
}

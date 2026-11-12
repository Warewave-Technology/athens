// Package auth implements Entra ID (OIDC) login, server-side sessions, CSRF
// protection and the session middleware (SPEC §5).
package auth

import (
	"context"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/Warewave-Technology/athens/internal/audit"
	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/metrics"
	"github.com/Warewave-Technology/athens/internal/reqctx"
	"github.com/Warewave-Technology/athens/internal/store"
	"github.com/Warewave-Technology/athens/internal/version"
)

// LoginPath is the sign-in page (rendered by the SPA, served without a session).
const LoginPath = "/login"

const (
	loginStateTTL = 10 * time.Minute
	touchInterval = 30 * time.Second
)

type Options struct {
	Config *config.Config
	Store  store.Store
	Audit  *audit.Writer
	Log    *slog.Logger
	// HTTPClient is used for OIDC discovery, JWKS and token calls (tests).
	HTTPClient *http.Client
	Now        func() time.Time
	// ExpectedTenant overrides the tenant derived from an Entra issuer
	// (tests and mock IdPs whose issuer is not login.microsoftonline.com).
	ExpectedTenant string
}

type Authenticator struct {
	cfg     *config.Config
	store   store.Store
	audit   *audit.Writer
	log     *slog.Logger
	client  *http.Client
	now     func() time.Time
	aead    cipher.AEAD
	baseURL *url.URL
	secure  bool
	tenant  string
	trusted []netip.Prefix

	mu         sync.RWMutex
	verifier   *oidc.IDTokenVerifier
	oauth      *oauth2.Config
	endSession string
}

func New(o Options) (*Authenticator, error) {
	key, err := o.Config.SessionKeyBytes()
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(deriveKey(key, "athens auth cookie v1"))
	if err != nil {
		return nil, err
	}
	a := &Authenticator{
		cfg: o.Config, store: o.Store, audit: o.Audit, log: o.Log, client: o.HTTPClient, now: o.Now, aead: aead,
		baseURL: o.Config.BaseURL(), secure: !o.Config.Server.InsecureDev, tenant: o.Config.TenantID(),
		trusted: reqctx.ParsePrefixes(o.Config.Server.TrustedProxies),
	}
	if a.client == nil {
		a.client = &http.Client{Timeout: 15 * time.Second}
	}
	if a.now == nil {
		a.now = time.Now
	}
	if o.ExpectedTenant != "" {
		a.tenant = strings.ToLower(o.ExpectedTenant)
	}
	return a, nil
}

func (a *Authenticator) oidcCtx(ctx context.Context) context.Context {
	return oidc.ClientContext(ctx, a.client)
}

// Init fetches the provider discovery document once.
func (a *Authenticator) Init(ctx context.Context) error {
	p, err := oidc.NewProvider(a.oidcCtx(ctx), a.cfg.OIDC.Issuer)
	if err != nil {
		return err
	}
	var extra struct {
		EndSession string `json:"end_session_endpoint"`
	}
	_ = p.Claims(&extra)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.verifier = p.Verifier(&oidc.Config{ClientID: a.cfg.OIDC.ClientID})
	a.oauth = &oauth2.Config{
		ClientID:     a.cfg.OIDC.ClientID,
		ClientSecret: a.cfg.OIDC.ClientSecret,
		Endpoint:     p.Endpoint(),
		RedirectURL:  a.baseURL.String() + a.cfg.OIDC.RedirectPath,
		Scopes:       a.cfg.OIDC.Scopes,
	}
	a.endSession = extra.EndSession
	return nil
}

// InitWithRetry retries Init with backoff until it succeeds or ctx ends.
// It returns early after firstWait so startup is not blocked forever; the
// retries continue in the background and Ready reports the state.
func (a *Authenticator) InitWithRetry(ctx context.Context, firstWait time.Duration) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		backoff := time.Second
		for {
			err := a.Init(ctx)
			if err == nil {
				a.log.Info("oidc provider initialized", "issuer", a.cfg.OIDC.Issuer)
				return
			}
			a.log.Warn("oidc provider init failed, retrying", "err", err, "in", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
		}
	}()
	select {
	case <-done:
	case <-time.After(firstWait):
	case <-ctx.Done():
	}
}

func (a *Authenticator) Ready() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.oauth != nil
}

func (a *Authenticator) provider() (*oauth2.Config, *oidc.IDTokenVerifier, string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.oauth, a.verifier, a.endSession
}

type loginState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	ReturnTo string `json:"r"`
	Expires  int64  `json:"e"`
}

// HandleLogin starts the authorization code + PKCE flow.
func (a *Authenticator) HandleLogin(w http.ResponseWriter, r *http.Request) {
	oc, _, _ := a.provider()
	if oc == nil {
		a.errorPage(w, http.StatusServiceUnavailable, "The identity provider is not reachable yet. Please try again in a moment.")
		return
	}
	st := loginState{
		State: randomToken(32), Nonce: randomToken(32), Verifier: oauth2.GenerateVerifier(),
		ReturnTo: SafeReturnTo(r.URL.Query().Get("returnTo")), Expires: a.now().Add(loginStateTTL).Unix(),
	}
	b, _ := json.Marshal(st)
	// #nosec G124 -- Secure is false only with server.insecureDev
	http.SetCookie(w, &http.Cookie{
		Name: a.authCookie(), Value: seal(a.aead, b, []byte(version.AuthCookie)), Path: "/auth/",
		MaxAge: int(loginStateTTL.Seconds()), HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, oc.AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier)), http.StatusFound)
}

type idClaims struct {
	OID               string            `json:"oid"`
	Sub               string            `json:"sub"`
	TID               string            `json:"tid"`
	PreferredUsername string            `json:"preferred_username"`
	Email             string            `json:"email"`
	Name              string            `json:"name"`
	Groups            []string          `json:"groups"`
	ClaimNames        map[string]string `json:"_claim_names"`
	HasGroups         bool              `json:"hasgroups"`
}

var (
	errTenant   = errors.New("tenant mismatch")
	errOverage  = errors.New("groups overage")
	errNotInApp = errors.New("not in allowed groups")
)

// HandleCallback completes the login.
func (a *Authenticator) HandleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := reqctx.WithMeta(r.Context(), a.meta(r))
	q := r.URL.Query()
	// #nosec G124 -- Secure is false only with server.insecureDev
	clearAuth := &http.Cookie{Name: a.authCookie(), Path: "/auth/", MaxAge: -1, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode}

	// Callbacks without a valid, matching sign-in state are anonymous (anyone
	// can start a sign-in and replay the callback): they are counted and
	// logged, never audited, so they cannot flood the audit log (ADR-027).
	st, err := a.readState(r)
	if err != nil {
		a.rejectState("missing_or_invalid")
		http.SetCookie(w, clearAuth)
		a.errorPage(w, http.StatusBadRequest, "Your sign-in attempt expired or is invalid. Please sign in again.")
		return
	}
	http.SetCookie(w, clearAuth)
	if !constantEqual(q.Get("state"), st.State) {
		a.rejectState("mismatch")
		a.errorPage(w, http.StatusBadRequest, "Your sign-in attempt is invalid (state mismatch). Please sign in again.")
		return
	}
	if e := q.Get("error"); e != "" {
		a.rejectAnonymous("idp_error")
		a.errorPage(w, http.StatusUnauthorized, "Sign-in was not completed: "+e+" "+q.Get("error_description"))
		return
	}
	oc, verifier, _ := a.provider()
	if oc == nil {
		a.errorPage(w, http.StatusServiceUnavailable, "The identity provider is not reachable yet. Please try again in a moment.")
		return
	}
	tok, err := oc.Exchange(a.oidcCtx(ctx), q.Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		a.log.Warn("token exchange failed", "err", err)
		a.rejectAnonymous("exchange_failed")
		a.errorPage(w, http.StatusUnauthorized, "Sign-in failed while exchanging the authorization code. Please try again.")
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := verifier.Verify(a.oidcCtx(ctx), raw)
	if err != nil {
		a.log.Warn("id token verification failed", "err", err)
		a.rejectAnonymous("invalid_id_token")
		a.errorPage(w, http.StatusUnauthorized, "Sign-in failed: the identity token is invalid.")
		return
	}
	if !constantEqual(idt.Nonce, st.Nonce) {
		a.rejectAnonymous("invalid_nonce")
		a.errorPage(w, http.StatusUnauthorized, "Sign-in failed: the identity token does not belong to this sign-in attempt.")
		return
	}
	var c idClaims
	if err := idt.Claims(&c); err != nil {
		a.deny(ctx, "invalid_claims", "")
		a.errorPage(w, http.StatusUnauthorized, "Sign-in failed: unreadable identity token claims.")
		return
	}
	user, err := a.userFromClaims(c)
	if err != nil {
		a.deny(reqctx.WithUser(ctx, user), reasonOf(err), "")
		switch {
		case errors.Is(err, errOverage):
			a.errorPage(w, http.StatusForbidden, "Your account is a member of too many groups for the token to list them. "+
				"An administrator must set groupMembershipClaims to \"ApplicationGroup\" on the Entra app registration "+
				"and assign the relevant groups to the enterprise application.")
		case errors.Is(err, errNotInApp):
			a.errorPage(w, http.StatusForbidden, "You are not authorized to use this application.")
		default:
			a.errorPage(w, http.StatusUnauthorized, "Sign-in failed: "+err.Error()+".")
		}
		return
	}

	now := a.now()
	if err := a.store.UpsertUser(ctx, store.User{ID: user.ID, Email: user.Email, Name: user.Name, LastLoginAt: now}); err != nil {
		a.log.Error("upsert user", "err", err)
		a.errorPage(w, http.StatusInternalServerError, "Sign-in failed due to an internal error.")
		return
	}
	// A new sign-in replaces the browser's previous session.
	if c, err := r.Cookie(a.SessionCookie()); err == nil && c.Value != "" {
		_ = a.store.DeleteSession(ctx, hashToken(c.Value))
	}
	cookie, csrf, err := a.StartSession(ctx, user)
	if err != nil {
		a.log.Error("create session", "err", err)
		a.errorPage(w, http.StatusInternalServerError, "Sign-in failed due to an internal error.")
		return
	}
	a.setSessionCookies(w, cookie, csrf)
	metrics.AuthLogins.WithLabelValues("ok").Inc()
	a.audit.Write(reqctx.WithUser(ctx, user), store.AuditRecord{Event: audit.EventLogin, Result: audit.ResultOK})
	http.Redirect(w, r, st.ReturnTo, http.StatusFound)
}

// StartSession stores a new session for user (who must exist in the store)
// and returns the cookie value and CSRF token.
func (a *Authenticator) StartSession(ctx context.Context, user *reqctx.User) (cookie, csrf string, err error) {
	now := a.now()
	cookie, csrf = randomToken(32), randomToken(32)
	m := reqctx.MetaFrom(ctx)
	err = a.store.CreateSession(ctx, store.Session{
		ID: hashToken(cookie), UserID: user.ID, Groups: user.Groups, CSRFToken: csrf, CreatedAt: now, LastSeenAt: now,
		ExpiresAt: now.Add(a.cfg.Auth.SessionTTL.D()), ClientIP: m.ClientIP, UserAgent: truncate(m.UserAgent, 512),
	})
	return cookie, csrf, err
}

func (a *Authenticator) setSessionCookies(w http.ResponseWriter, cookie, csrf string) {
	maxAge := int(a.cfg.Auth.SessionTTL.D().Seconds())
	// Secure is false only with server.insecureDev (plain-http local development).
	// The CSRF cookie must be readable by the SPA (double submit, SPEC §5.4).
	// #nosec G124
	http.SetCookie(w, &http.Cookie{Name: a.SessionCookie(), Value: cookie, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode})
	// #nosec G124 -- see above
	http.SetCookie(w, &http.Cookie{Name: a.csrfCookie(), Value: csrf, Path: "/", MaxAge: maxAge, HttpOnly: false, Secure: a.secure, SameSite: http.SameSiteLaxMode})
}

func (a *Authenticator) userFromClaims(c idClaims) (*reqctx.User, error) {
	u := &reqctx.User{ID: c.OID, Email: c.Email, Name: c.Name, Groups: c.Groups}
	if u.Email == "" {
		u.Email = c.PreferredUsername
	}
	if u.Groups == nil {
		u.Groups = []string{}
	}
	if a.tenant != "" && !strings.EqualFold(c.TID, a.tenant) {
		return u, errTenant
	}
	if u.ID == "" {
		if !a.cfg.OIDC.SkipIssuerTenantCheck {
			return u, errors.New("the identity token has no oid claim")
		}
		u.ID = c.Sub
	}
	if _, overage := c.ClaimNames["groups"]; overage || c.HasGroups {
		return u, errOverage
	}
	if len(a.cfg.OIDC.AllowedGroups) > 0 && !slices.ContainsFunc(u.Groups, func(g string) bool {
		return slices.Contains(a.cfg.OIDC.AllowedGroups, g)
	}) {
		return u, errNotInApp
	}
	return u, nil
}

func reasonOf(err error) string {
	switch {
	case errors.Is(err, errTenant):
		return "tenant_mismatch"
	case errors.Is(err, errOverage):
		return "groups_overage"
	case errors.Is(err, errNotInApp):
		return "not_allowed"
	default:
		return "invalid_claims"
	}
}

func (a *Authenticator) rejectState(why string) {
	metrics.AuthLogins.WithLabelValues("denied").Inc()
	a.log.Info("sign-in callback rejected", "reason", "invalid_state", "state", why)
}

// rejectAnonymous records a failure before the ID token is verified: the
// caller is unknown and anyone can trigger it (GET /auth/login then replay
// the callback), so it is counted and logged with a fixed reason, never
// audited (ADR-027). Denials of a verified user are audited by deny.
func (a *Authenticator) rejectAnonymous(reason string) {
	metrics.AuthLogins.WithLabelValues("denied").Inc()
	a.log.Info("sign-in callback rejected", "reason", reason)
}

func (a *Authenticator) deny(ctx context.Context, reason, idpError string) {
	metrics.AuthLogins.WithLabelValues("denied").Inc()
	details, _ := json.Marshal(map[string]string{"reason": reason, "idpError": idpError})
	a.audit.Write(ctx, store.AuditRecord{Event: audit.EventLoginDenied, Result: audit.ResultDenied, ErrorCode: reason, Details: string(details)})
}

func (a *Authenticator) readState(r *http.Request) (*loginState, error) {
	c, err := r.Cookie(a.authCookie())
	if err != nil {
		return nil, err
	}
	b, err := open(a.aead, c.Value, []byte(version.AuthCookie))
	if err != nil {
		return nil, err
	}
	var st loginState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	if a.now().Unix() > st.Expires {
		return nil, errors.New("expired")
	}
	return &st, nil
}

// HandleLogout deletes the session (form post with the CSRF token, ADR-015)
// and redirects to the Entra end-session endpoint.
func (a *Authenticator) HandleLogout(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	u := reqctx.UserFrom(r.Context())
	if u == nil || !a.originOK(r) || !constantEqual(r.PostFormValue("csrf"), u.CSRFToken) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	ctx := reqctx.WithMeta(r.Context(), a.meta(r))
	if err := a.store.DeleteSession(ctx, u.SessionID); err != nil {
		a.log.Error("delete session", "err", err)
	}
	a.clearSessionCookies(w)
	a.audit.Write(ctx, store.AuditRecord{Event: audit.EventLogout, Result: audit.ResultOK})
	_, _, end := a.provider()
	target := a.baseURL.String() + "/"
	if end != "" {
		target = end + "?" + url.Values{"post_logout_redirect_uri": {a.baseURL.String() + "/"}, "client_id": {a.cfg.OIDC.ClientID}}.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *Authenticator) clearSessionCookies(w http.ResponseWriter) {
	for _, n := range []string{a.SessionCookie(), a.csrfCookie()} {
		// #nosec G124 -- expiring cookies; flags mirror the ones set at login
		http.SetCookie(w, &http.Cookie{Name: n, Path: "/", MaxAge: -1, HttpOnly: n == a.SessionCookie(), Secure: a.secure, SameSite: http.SameSiteLaxMode})
	}
}

// Cookie names carry the __Host- prefix (Secure, Path=/, no Domain) so a
// sibling subdomain cannot plant them; the sign-in state cookie is scoped to
// /auth/ and uses __Secure-. Plain names only with server.insecureDev.
func (a *Authenticator) SessionCookie() string { return a.prefixed("__Host-", version.SessionCookie) }
func (a *Authenticator) csrfCookie() string    { return a.prefixed("__Host-", version.CSRFCookie) }
func (a *Authenticator) authCookie() string    { return a.prefixed("__Secure-", version.AuthCookie) }

func (a *Authenticator) prefixed(prefix, name string) string {
	if a.secure {
		return prefix + name
	}
	return name
}

func (a *Authenticator) meta(r *http.Request) reqctx.Meta {
	return reqctx.Meta{ClientIP: reqctx.ClientIP(r, a.trusted), UserAgent: r.UserAgent()}
}

var errorTmpl = template.Must(template.New("e").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<title>Sign-in problem · athens</title><style>body{font:14px/1.6 "IBM Plex Sans",system-ui,sans-serif;background:#f9f8f5;
color:#1b1e24;display:grid;place-items:center;min-height:100vh;margin:0}main{max-width:32rem;padding:28px 32px;border:1px solid #e4e1d9;
border-radius:14px;background:#fff}h1{font-size:18px;font-weight:600;margin:0 0 8px}p{color:#55534d}a{display:inline-block;
margin-top:8px;padding:6px 14px;border-radius:7px;background:#1d5fa8;color:#fff;text-decoration:none;font-weight:500}
@media (prefers-color-scheme:dark){body{background:#0d1320;color:#e7ebf2}main{background:#131b2b;border-color:#222d42}
p{color:#b4bccb}a{background:#6fa8e8;color:#0d1320}}</style></head><body><main><h1>Sign-in problem</h1><p>{{.}}</p>
<p><a href="/login">Back to sign in</a></p></main></body></html>`))

func (a *Authenticator) errorPage(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := errorTmpl.Execute(w, msg); err != nil {
		a.log.Error("render error page", "err", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

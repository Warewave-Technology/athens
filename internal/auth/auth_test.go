package auth

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/oauth2-proxy/mockoidc"

	"github.com/Warewave-Technology/athens/internal/audit"
	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/reqctx"
	"github.com/Warewave-Technology/athens/internal/store"
	"github.com/Warewave-Technology/athens/internal/version"
)

const tenant = "11111111-2222-3333-4444-555555555555"

type entraUser struct {
	oid, tid, email, name string
	groups                []string
	overage               bool
}

func (u *entraUser) ID() string { return "sub-" + u.oid }
func (u *entraUser) Userinfo([]string) ([]byte, error) {
	return json.Marshal(map[string]string{"sub": u.ID()})
}

type entraClaims struct {
	*mockoidc.IDTokenClaims
	OID          string            `json:"oid,omitempty"`
	TID          string            `json:"tid,omitempty"`
	Email        string            `json:"email,omitempty"`
	Name         string            `json:"name,omitempty"`
	Groups       []string          `json:"groups,omitempty"`
	ClaimNames   map[string]string `json:"_claim_names,omitempty"`
	ClaimSources map[string]any    `json:"_claim_sources,omitempty"`
}

func (u *entraUser) Claims(_ []string, c *mockoidc.IDTokenClaims) (jwt.Claims, error) {
	ec := &entraClaims{IDTokenClaims: c, OID: u.oid, TID: u.tid, Email: u.email, Name: u.name, Groups: u.groups}
	if u.overage {
		ec.Groups = nil
		ec.ClaimNames = map[string]string{"groups": "src1"}
		ec.ClaimSources = map[string]any{"src1": map[string]string{"endpoint": "https://graph.microsoft.com/v1.0/users/x/getMemberObjects"}}
	}
	return ec, nil
}

func defaultUser() *entraUser {
	return &entraUser{oid: "oid-1", tid: tenant, email: "jane@example.com", name: "Jane", groups: []string{"g-dev"}}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type env struct {
	t     *testing.T
	mock  *mockoidc.MockOIDC
	srv   *httptest.Server
	store *store.SQLite
	clock *clock
	cfg   *config.Config
	jar   map[string]*http.Cookie
}

func newEnv(t *testing.T, mutate func(*config.Config)) *env {
	t.Helper()
	m, err := mockoidc.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown() })
	st, err := store.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	e := &env{t: t, mock: m, store: st, clock: &clock{t: time.Now()}, jar: map[string]*http.Cookie{}}
	mux := http.NewServeMux()
	e.srv = httptest.NewServer(mux)
	t.Cleanup(e.srv.Close)

	mc := m.Config()
	cfg := config.Default()
	cfg.Server.BaseURL = e.srv.URL
	cfg.Server.InsecureDev = true
	cfg.OIDC.Issuer = mc.Issuer
	cfg.OIDC.ClientID = mc.ClientID
	cfg.OIDC.ClientSecret = mc.ClientSecret
	cfg.OIDC.SkipIssuerTenantCheck = true
	cfg.Auth.SessionKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	cfg.Discovery.Namespaces = []string{"x"}
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	e.cfg = &cfg
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(Options{Config: &cfg, Store: st, Audit: audit.NewWriter(st, log, true), Log: log, Now: e.clock.now, ExpectedTenant: tenant})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	mux.HandleFunc("GET /auth/login", a.HandleLogin)
	mux.HandleFunc("GET /auth/callback", a.HandleCallback)
	mux.Handle("POST /auth/logout", a.LoadSession(http.HandlerFunc(a.HandleLogout)))
	mux.Handle("/api/", a.LoadSession(RequireUser(a.CSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := reqctx.UserFrom(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]any{"id": u.ID, "email": u.Email, "groups": u.Groups})
	})))))
	mux.Handle("/", a.LoadSession(RequireUser(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "app")
	}))))
	return e
}

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (e *env) do(req *http.Request) *http.Response {
	e.t.Helper()
	for _, c := range e.jar {
		req.AddCookie(c)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, c := range resp.Cookies() {
		if c.MaxAge < 0 {
			delete(e.jar, c.Name)
		} else {
			e.jar[c.Name] = c
		}
	}
	return resp
}

func (e *env) get(u string) *http.Response {
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	return e.do(req)
}

// startLogin returns the IdP authorize URL.
func (e *env) startLogin(returnTo string) *url.URL {
	e.t.Helper()
	resp := e.get(e.srv.URL + "/auth/login?returnTo=" + url.QueryEscape(returnTo))
	if resp.StatusCode != http.StatusFound {
		e.t.Fatalf("login status %d", resp.StatusCode)
	}
	u, _ := url.Parse(resp.Header.Get("Location"))
	q := u.Query()
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" {
		e.t.Fatalf("authorize URL lacks PKCE/nonce: %s", u)
	}
	return u
}

// authorize hits the mock IdP and returns the callback URL it redirects to.
func (e *env) authorize(u *url.URL, user *entraUser) *url.URL {
	e.t.Helper()
	e.mock.QueueUser(user)
	resp, err := noRedirect.Get(u.String())
	if err != nil {
		e.t.Fatal(err)
	}
	cb, _ := url.Parse(resp.Header.Get("Location"))
	return cb
}

func (e *env) login(user *entraUser, returnTo string) *http.Response {
	return e.get(e.authorize(e.startLogin(returnTo), user).String())
}

func body(r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	return string(b)
}

func (e *env) lastAudit(event string) *store.AuditRecord {
	recs, err := e.store.ListAudit(context.Background(), store.AuditFilter{Event: event, Limit: 1})
	if err != nil || len(recs) == 0 {
		return nil
	}
	return &recs[0]
}

func TestLoginFlow(t *testing.T) {
	e := newEnv(t, nil)
	resp := e.login(defaultUser(), "/targets/payments/db?x=1")
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/targets/payments/db?x=1" {
		t.Fatalf("callback: %d %s %s", resp.StatusCode, resp.Header.Get("Location"), body(resp))
	}
	sc := e.jar[version.SessionCookie]
	if sc == nil || !sc.HttpOnly || sc.SameSite != http.SameSiteLaxMode || e.jar[version.CSRFCookie] == nil || e.jar[version.CSRFCookie].HttpOnly {
		t.Fatalf("cookies: %+v", e.jar)
	}
	if _, ok := e.jar[version.AuthCookie]; ok {
		t.Fatal("auth cookie must be cleared after callback")
	}
	me := e.get(e.srv.URL + "/api/v1/me")
	if b := body(me); me.StatusCode != 200 || !strings.Contains(b, "jane@example.com") || !strings.Contains(b, "g-dev") {
		t.Fatalf("me: %d %s", me.StatusCode, b)
	}
	// raw token is never stored
	if _, err := e.store.GetSession(context.Background(), sc.Value); err == nil {
		t.Fatal("session stored under raw cookie value")
	}
	if a := e.lastAudit(audit.EventLogin); a == nil || a.Email != "jane@example.com" {
		t.Fatalf("login audit: %+v", a)
	}
}

func TestUnauthenticated(t *testing.T) {
	e := newEnv(t, nil)
	if r := e.get(e.srv.URL + "/api/v1/me"); r.StatusCode != 401 || !strings.Contains(body(r), "unauthenticated") {
		t.Fatalf("api: %d", r.StatusCode)
	}
	r := e.get(e.srv.URL + "/some/page?q=1")
	if r.StatusCode != 302 || r.Header.Get("Location") != "/login?returnTo=%2Fsome%2Fpage%3Fq%3D1" {
		t.Fatalf("page: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
}

func TestTamperedState(t *testing.T) {
	e := newEnv(t, nil)
	cb := e.authorize(e.startLogin("/"), defaultUser())
	q := cb.Query()
	q.Set("state", "forged")
	cb.RawQuery = q.Encode()
	if r := e.get(cb.String()); r.StatusCode != 400 {
		t.Fatalf("status %d", r.StatusCode)
	}
	if e.jar[version.SessionCookie] != nil {
		t.Fatal("session created")
	}
	// anonymous: counted and logged, never audited (ADR-027)
	if a := e.lastAudit(audit.EventLoginDenied); a != nil {
		t.Fatalf("state mismatch was audited: %+v", a)
	}
}

func TestMissingStateCookie(t *testing.T) {
	e := newEnv(t, nil)
	cb := e.authorize(e.startLogin("/"), defaultUser())
	delete(e.jar, version.AuthCookie)
	if r := e.get(cb.String()); r.StatusCode != 400 {
		t.Fatalf("status %d", r.StatusCode)
	}
	// no sign-in in progress: anonymous requests must not write audit rows
	if a := e.lastAudit(audit.EventLoginDenied); a != nil {
		t.Fatalf("callback without state cookie was audited: %+v", a)
	}
}

func TestSecureCookieNames(t *testing.T) {
	cfg := config.Default()
	a := &Authenticator{secure: true, cfg: &cfg}
	if a.SessionCookie() != "__Host-athens_session" || a.csrfCookie() != "__Host-athens_csrf" || a.authCookie() != "__Secure-athens_auth" {
		t.Fatalf("names %s %s %s", a.SessionCookie(), a.csrfCookie(), a.authCookie())
	}
	rec := httptest.NewRecorder()
	a.setSessionCookies(rec, "s", "c")
	for _, c := range rec.Result().Cookies() {
		// __Host- requires Secure, Path=/ and no Domain, or browsers drop it
		if !c.Secure || c.Path != "/" || c.Domain != "" {
			t.Fatalf("cookie %s: %+v", c.Name, c)
		}
	}
	if (&Authenticator{}).SessionCookie() != version.SessionCookie {
		t.Fatal("insecureDev keeps the plain name")
	}
}

func TestExpiredStateCookie(t *testing.T) {
	e := newEnv(t, nil)
	cb := e.authorize(e.startLogin("/"), defaultUser())
	e.clock.add(11 * time.Minute)
	if r := e.get(cb.String()); r.StatusCode != 400 {
		t.Fatalf("status %d", r.StatusCode)
	}
}

func TestTamperedNonce(t *testing.T) {
	e := newEnv(t, nil)
	u := e.startLogin("/")
	q := u.Query()
	q.Set("nonce", "attacker-nonce")
	u.RawQuery = q.Encode()
	r := e.get(e.authorize(u, defaultUser()).String())
	if r.StatusCode != 401 || !strings.Contains(body(r), "does not belong") {
		t.Fatalf("status %d", r.StatusCode)
	}
}

func TestIdPError(t *testing.T) {
	e := newEnv(t, nil)
	u := e.startLogin("/")
	cb := e.srv.URL + "/auth/callback?" + url.Values{"state": {u.Query().Get("state")}, "error": {"access_denied"}}.Encode()
	if r := e.get(cb); r.StatusCode != 401 || !strings.Contains(body(r), "access_denied") {
		t.Fatalf("status %d", r.StatusCode)
	}
}

func TestWrongTenant(t *testing.T) {
	e := newEnv(t, nil)
	u := defaultUser()
	u.tid = "99999999-2222-3333-4444-555555555555"
	if r := e.login(u, "/"); r.StatusCode != 401 {
		t.Fatalf("status %d", r.StatusCode)
	}
	if a := e.lastAudit(audit.EventLoginDenied); a == nil || a.ErrorCode != "tenant_mismatch" || a.Email != "jane@example.com" {
		t.Fatalf("audit %+v", a)
	}
}

func TestGroupsOverage(t *testing.T) {
	e := newEnv(t, nil)
	u := defaultUser()
	u.overage = true
	r := e.login(u, "/")
	if b := body(r); r.StatusCode != 403 || !strings.Contains(b, "ApplicationGroup") {
		t.Fatalf("status %d %s", r.StatusCode, b)
	}
}

func TestAllowedGroups(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.OIDC.AllowedGroups = []string{"g-ops"} })
	if r := e.login(defaultUser(), "/"); r.StatusCode != 403 || !strings.Contains(body(r), "not authorized") {
		t.Fatalf("status %d", r.StatusCode)
	}
	u := defaultUser()
	u.groups = []string{"g-ops"}
	if r := e.login(u, "/"); r.StatusCode != 302 {
		t.Fatalf("member status %d", r.StatusCode)
	}
}

func TestMissingOID(t *testing.T) {
	cfg := config.Default()
	a := &Authenticator{cfg: &cfg, tenant: tenant}
	if _, err := a.userFromClaims(idClaims{TID: tenant, Sub: "s"}); err == nil || !strings.Contains(err.Error(), "oid") {
		t.Fatalf("err = %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	e := newEnv(t, func(c *config.Config) {
		c.Auth.SessionTTL = config.Duration(2 * time.Hour)
		c.Auth.IdleTTL = config.Duration(time.Hour)
	})
	e.login(defaultUser(), "/")
	// activity keeps the idle timer fresh
	for range 2 {
		e.clock.add(50 * time.Minute)
		if r := e.get(e.srv.URL + "/api/v1/me"); r.StatusCode != 200 {
			t.Fatalf("active session rejected: %d", r.StatusCode)
		}
	}
	// absolute TTL (150 min > 120) even though the session is active
	e.clock.add(50 * time.Minute)
	if r := e.get(e.srv.URL + "/api/v1/me"); r.StatusCode != 401 {
		t.Fatalf("expired session status %d", r.StatusCode)
	}
}

func TestSessionIdle(t *testing.T) {
	e := newEnv(t, nil)
	e.login(defaultUser(), "/")
	e.clock.add(61 * time.Minute)
	if r := e.get(e.srv.URL + "/api/v1/me"); r.StatusCode != 401 {
		t.Fatalf("idle session status %d", r.StatusCode)
	}
}

func TestCSRF(t *testing.T) {
	e := newEnv(t, nil)
	e.login(defaultUser(), "/")
	csrf := e.jar[version.CSRFCookie].Value
	post := func(token, origin, referer string) int {
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/api/v1/x", strings.NewReader("{}"))
		if token != "" {
			req.Header.Set(version.CSRFHeader, token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		r := e.do(req)
		_ = body(r)
		return r.StatusCode
	}
	cases := []struct {
		name, token, origin, referer string
		want                         int
	}{
		{"no header", "", e.srv.URL, "", 403},
		{"wrong token", "nope", e.srv.URL, "", 403},
		{"wrong origin", csrf, "https://evil.example", "", 403},
		{"no origin or referer", csrf, "", "", 403},
		{"wrong referer", csrf, "", "https://evil.example/x", 403},
		{"ok origin", csrf, e.srv.URL, "", 200},
		{"ok referer", csrf, "", e.srv.URL + "/page", 200},
	}
	for _, tc := range cases {
		if got := post(tc.token, tc.origin, tc.referer); got != tc.want {
			t.Errorf("%s: status %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestOpenRedirect(t *testing.T) {
	e := newEnv(t, nil)
	for _, rt := range []string{"//evil.com", "https://evil", "/\\evil.com", "evil", "/auth/logout"} {
		r := e.login(defaultUser(), rt)
		if r.StatusCode != 302 || r.Header.Get("Location") != "/" {
			t.Errorf("returnTo %q -> %s", rt, r.Header.Get("Location"))
		}
	}
}

func TestSafeReturnTo(t *testing.T) {
	cases := map[string]string{
		"":                  "/",
		"/":                 "/",
		"/a/b?c=d#e":        "/a/b?c=d#e",
		"//evil.com":        "/",
		"///evil.com":       "/",
		"/\\evil.com":       "/",
		"https://evil":      "/",
		"javascript:alert":  "/",
		"/ok\r\nX: y":       "/",
		"/auth/login":       "/",
		"/login":            "/",
		"/login?returnTo=/": "/",
		"/logins":           "/logins",
	}
	for in, want := range cases {
		if got := SafeReturnTo(in); got != want {
			t.Errorf("SafeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t, nil)
	e.login(defaultUser(), "/")
	csrf := e.jar[version.CSRFCookie].Value
	session := e.jar[version.SessionCookie]
	logout := func(token string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/auth/logout", strings.NewReader(url.Values{"csrf": {token}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", e.srv.URL)
		return e.do(req)
	}
	if r := logout("forged"); r.StatusCode != 303 || r.Header.Get("Location") != "/" {
		t.Fatalf("forged logout: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r := e.get(e.srv.URL + "/api/v1/me"); r.StatusCode != 200 {
		t.Fatal("forged logout must keep the session")
	}
	r := logout(csrf)
	if r.StatusCode != 303 || !strings.HasPrefix(r.Header.Get("Location"), e.srv.URL) && !strings.Contains(r.Header.Get("Location"), "post_logout_redirect_uri") {
		t.Fatalf("logout: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	e.jar[version.SessionCookie] = session // replay old cookie
	if r := e.get(e.srv.URL + "/api/v1/me"); r.StatusCode != 401 {
		t.Fatalf("old session after logout: %d", r.StatusCode)
	}
	if a := e.lastAudit(audit.EventLogout); a == nil {
		t.Fatal("logout not audited")
	}
}

func TestProviderNotReady(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.SessionKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	cfg.Server.BaseURL = "http://x"
	cfg.OIDC.Issuer = "http://127.0.0.1:1/none"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(Options{Config: &cfg, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	a.InitWithRetry(ctx, 20*time.Millisecond)
	if a.Ready() {
		t.Fatal("must not be ready")
	}
	w := httptest.NewRecorder()
	a.HandleLogin(w, httptest.NewRequest(http.MethodGet, "/auth/login", nil))
	if w.Code != 503 {
		t.Fatalf("status %d", w.Code)
	}
}

func TestPreAuthFailuresNotAudited(t *testing.T) {
	e := newEnv(t, nil)
	u := e.startLogin("/")
	q := u.Query()
	cbURL := e.srv.URL + "/auth/callback?state=" + url.QueryEscape(q.Get("state")) + "&error=access_denied&error_description=x"
	if r := e.get(cbURL); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", r.StatusCode)
	}
	if a := e.lastAudit(audit.EventLoginDenied); a != nil {
		t.Fatalf("an anonymous IdP error was audited: %+v", a)
	}
}

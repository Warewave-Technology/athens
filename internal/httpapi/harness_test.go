package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Warewave-Technology/athens/internal/audit"
	"github.com/Warewave-Technology/athens/internal/auth"
	"github.com/Warewave-Technology/athens/internal/authz"
	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/credentials"
	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/pgconn"
	"github.com/Warewave-Technology/athens/internal/reqctx"
	"github.com/Warewave-Technology/athens/internal/store"
	"github.com/Warewave-Technology/athens/internal/version"
)

type harness struct {
	t        *testing.T
	srv      *httptest.Server
	store    *store.SQLite
	registry *discovery.Registry
	pools    *pgconn.Manager
	authn    *auth.Authenticator
	cfg      *config.Config
	server   *Server
}

type harnessOpts struct {
	targets []discovery.Target
	client  kubernetes.Interface
	mutate  func(*config.Config)
}

// testRules: readers read everything in itest/demo, writers write, bi reads
// only database "app" of targets named "bi-*", admins are app admins.
func testConfig() config.Config {
	cfg := config.Default()
	cfg.Server.BaseURL = "http://athens.test"
	cfg.Server.InsecureDev = true
	cfg.OIDC.Issuer = "http://idp.test/oidc"
	cfg.OIDC.SkipIssuerTenantCheck = true
	cfg.OIDC.ClientID, cfg.OIDC.ClientSecret = "c", "s"
	cfg.Auth.SessionKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	cfg.Discovery.Namespaces = []string{"itest", "demo"}
	cfg.Authz.AdminGroups = []string{"admins"}
	cfg.Authz.Rules = []config.Rule{
		{Name: "readers", Groups: []string{"readers"}, Namespaces: []string{"itest", "demo"}, Level: "read"},
		{Name: "writers", Groups: []string{"writers"}, Namespaces: []string{"itest", "demo"}, Level: "write"},
		{Name: "bi", Groups: []string{"bi"}, Namespaces: []string{"*"}, Targets: []string{"bi-*"}, Databases: []string{"app"}, Level: "read"},
	}
	return cfg
}

func newHarness(t *testing.T, o harnessOpts) *harness {
	t.Helper()
	cfg := testConfig()
	if o.mutate != nil {
		o.mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	aw := audit.NewWriter(st, log, cfg.Audit.LogSQL)
	authn, err := auth.New(auth.Options{Config: &cfg, Store: st, Audit: aw, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	reg := discovery.NewRegistry()
	byNS := map[string][]discovery.Target{}
	for _, tg := range o.targets {
		byNS[tg.Namespace] = append(byNS[tg.Namespace], tg)
	}
	for ns, ts := range byNS {
		reg.SetNamespace(ns, ts, nil, time.Now())
	}
	client := o.client
	if client == nil {
		client = fake.NewClientset()
	}
	pools := pgconn.NewManager(pgconn.Options{Targets: reg, Resolver: credentials.NewResolver(client, nil), Limits: cfg.Limits, Log: log})
	reg.OnChange(pools.TargetChanged)
	t.Cleanup(pools.CloseAll)
	s := New(Deps{Config: &cfg, Log: log, Store: st, Auth: authn, Authz: authz.New(cfg.Authz), Audit: aw, Registry: reg, Pools: pools,
		SPA: http.NotFoundHandler()})
	h := &harness{t: t, store: st, registry: reg, pools: pools, authn: authn, cfg: &cfg, server: s}
	h.srv = httptest.NewServer(s.Handler())
	t.Cleanup(h.srv.Close)
	return h
}

type client struct {
	h      *harness
	cookie string
	csrf   string
	email  string
}

func (h *harness) login(email string, groups ...string) *client {
	h.t.Helper()
	ctx := context.Background()
	u := &reqctx.User{ID: "oid-" + email, Email: email, Name: email, Groups: groups}
	if err := h.store.UpsertUser(ctx, store.User{ID: u.ID, Email: email, Name: email, LastLoginAt: time.Now()}); err != nil {
		h.t.Fatal(err)
	}
	cookie, csrf, err := h.authn.StartSession(ctx, u)
	if err != nil {
		h.t.Fatal(err)
	}
	return &client{h: h, cookie: cookie, csrf: csrf, email: email}
}

type response struct {
	status int
	body   []byte
	header http.Header
}

func (r response) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
}

func (r response) errCode() string {
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	_ = json.Unmarshal(r.body, &e)
	return e.Error.Code
}

func (c *client) do(method, path string, body any) response {
	c.h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.h.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://athens.test")
	req.Header.Set(version.CSRFHeader, c.csrf)
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: version.SessionCookie, Value: c.cookie})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, body: b, header: resp.Header}
}

func (c *client) get(path string) response { return c.do(http.MethodGet, path, nil) }

//nolint:unused // used by integration tests (build tag)
func (h *harness) audits(event string) []store.AuditRecord {
	recs, err := h.store.ListAudit(context.Background(), store.AuditFilter{Event: event, Limit: 100})
	if err != nil {
		h.t.Fatal(err)
	}
	return recs
}

package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/query"
)

func TestSecurityHeadersAndHealth(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	anon := &client{h: h}
	r := anon.get("/healthz")
	if r.status != 200 {
		t.Fatalf("healthz %d", r.status)
	}
	for _, hdr := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy"} {
		if r.header.Get(hdr) == "" {
			t.Errorf("missing %s", hdr)
		}
	}
	if !strings.Contains(r.header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Error("CSP lacks frame-ancestors")
	}
	if r := anon.get("/api/v1/me"); r.status != 401 || r.errCode() != "unauthenticated" {
		t.Fatalf("anon me %d", r.status)
	}
	if r := anon.get("/readyz"); r.status != 503 { // OIDC not initialized in tests
		t.Fatalf("readyz %d", r.status)
	}
}

func TestMeAndUnknownRoute(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	c := h.login("admin@example.com", "admins")
	var me meResponse
	r := c.get("/api/v1/me")
	r.json(t, &me)
	if !me.IsAdmin || me.Email != "admin@example.com" {
		t.Fatalf("me %+v", me)
	}
	if r := c.get("/api/v1/nope"); r.status != 404 || r.errCode() != "not_found" {
		t.Fatalf("unknown route %d %s", r.status, r.body)
	}
}

func TestBodyLimit(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	c := h.login("w@example.com", "writers")
	big := map[string]string{"sql": strings.Repeat("x", 2<<20)}
	if r := c.do(http.MethodPut, "/api/v1/ui-state/k", big); r.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d", r.status)
	}
}

func TestTargetsVisibility(t *testing.T) {
	targets := []discovery.Target{
		{ID: "demo/orders", Namespace: "demo", Name: "orders", DisplayName: "Orders", Source: "generic", Warnings: []string{"w1"}},
		{ID: "demo/bi-warehouse", Namespace: "demo", Name: "bi-warehouse", Source: "cnpg", Environment: "production"},
		{ID: "other/secret", Namespace: "other", Name: "secret", Source: "generic"},
	}
	h := newHarness(t, harnessOpts{targets: targets})
	list := func(c *client) []targetJSON {
		var out struct{ Targets []targetJSON }
		c.get("/api/v1/targets").json(t, &out)
		return out.Targets
	}
	if got := list(h.login("nobody@example.com")); len(got) != 0 {
		t.Fatalf("no-rule user sees %+v", got)
	}
	bi := h.login("bi@example.com", "bi")
	if got := list(bi); len(got) != 1 || got[0].ID != "demo/bi-warehouse" || got[0].Level != "read" || got[0].Warnings != nil {
		t.Fatalf("bi sees %+v", got)
	}
	if r := bi.get("/api/v1/targets/demo/orders/databases"); r.status != 404 {
		t.Fatalf("invisible target: %d", r.status)
	}
	if r := bi.get("/api/v1/targets/demo/bi-warehouse/db/other/schemas"); r.status != 404 {
		t.Fatalf("invisible database: %d", r.status)
	}
	if r := bi.get("/api/v1/targets/other/secret/databases"); r.status != 404 {
		t.Fatalf("other ns: %d", r.status)
	}
	w := h.login("w@example.com", "writers", "admins")
	got := list(w)
	if len(got) != 2 || got[1].Level != "write" || len(got[1].Warnings) != 1 || got[1].Status != "unknown" {
		t.Fatalf("writer/admin sees %+v", got)
	}
	// admin endpoints are 404 for non-admins
	if r := bi.get("/api/v1/admin/discovery"); r.status != 404 {
		t.Fatalf("admin discovery for non-admin: %d", r.status)
	}
	if r := w.get("/api/v1/admin/discovery"); r.status != 200 || !strings.Contains(string(r.body), "demo/orders") {
		t.Fatalf("admin discovery: %d", r.status)
	}
}

func TestLineCol(t *testing.T) {
	sql := "select 1,\n  é bad"
	if l, c := query.LineCol(sql, 13); l != 2 || c != 3 { // position 13 is "é"
		t.Fatalf("got %d:%d", l, c)
	}
}

func TestLoginPage(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(c *client, path string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, h.srv.URL+path, nil)
		if c != nil {
			req.AddCookie(&http.Cookie{Name: "athens_session", Value: c.cookie})
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	// anonymous: app pages redirect to the sign-in page, which itself renders
	if r := get(nil, "/some/page"); r.StatusCode != 302 || r.Header.Get("Location") != "/login?returnTo=%2Fsome%2Fpage" {
		t.Fatalf("anonymous page: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r := get(nil, "/login?returnTo=%2Fx"); r.StatusCode == 302 {
		t.Fatalf("login page redirected anonymous user to %s", r.Header.Get("Location"))
	}
	// signed in: the sign-in page forwards to returnTo (validated)
	c := h.login("w@example.com", "writers")
	if r := get(c, "/login?returnTo=%2Fx"); r.StatusCode != 302 || r.Header.Get("Location") != "/x" {
		t.Fatalf("signed-in login: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	if r := get(c, "/login?returnTo=%2F%2Fevil.com"); r.Header.Get("Location") != "/" {
		t.Fatalf("open redirect: %s", r.Header.Get("Location"))
	}
}

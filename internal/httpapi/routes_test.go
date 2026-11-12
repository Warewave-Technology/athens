package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/version"
)

var params = strings.NewReplacer("{ns}", "demo", "{name}", "orders", "{db}", "app", "{schema}", "public", "{rel}", "t",
	"{id}", "1", "{key}", "k")

func routeList(t *testing.T, h *harness) [][2]string {
	t.Helper()
	_ = h.server.Handler() // registers routes
	var out [][2]string
	for _, r := range h.server.Routes {
		m, p, _ := strings.Cut(r, " ")
		out = append(out, [2]string{m, params.Replace(p)})
	}
	if len(out) < 25 {
		t.Fatalf("only %d routes registered", len(out))
	}
	return out
}

// Security checklist (TASKS M6): authentication, CSRF and authorization are
// enforced on every API route, not just the ones with dedicated tests.
func TestRouteTableSecurity(t *testing.T) {
	h := newHarness(t, harnessOpts{targets: []discovery.Target{{ID: "demo/orders", Namespace: "demo", Name: "orders", Source: "generic"}}})
	routes := routeList(t, h)
	anon := &client{h: h}
	nobody := h.login("nobody@example.com") // no rules match
	reader := h.login("r@example.com", "readers")

	for _, rt := range routes {
		method, path := rt[0], rt[1]
		t.Run(method+" "+path, func(t *testing.T) {
			if r := anon.do(method, path, map[string]any{}); r.status != 401 {
				t.Errorf("anonymous: %d", r.status)
			}
			if method != http.MethodGet {
				nc := *reader
				nc.csrf = ""
				if r := nc.do(method, path, map[string]any{}); r.status != 403 || r.errCode() != "csrf" {
					t.Errorf("without CSRF: %d %s", r.status, r.body)
				}
				req, _ := http.NewRequest(method, h.srv.URL+path, strings.NewReader("{}"))
				req.Header.Set(version.CSRFHeader, reader.csrf)
				req.Header.Set("Origin", "https://evil.example")
				req.AddCookie(&http.Cookie{Name: version.SessionCookie, Value: reader.cookie})
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				if resp.StatusCode != 403 {
					t.Errorf("foreign origin: %d", resp.StatusCode)
				}
			}
			if strings.Contains(path, "/targets/demo/orders") || strings.HasPrefix(path, "/api/v1/admin/") {
				if r := nobody.do(method, path, map[string]any{}); r.status != 404 {
					t.Errorf("unauthorized user: %d %s", r.status, r.body)
				}
			}
			if strings.HasPrefix(path, "/api/v1/admin/") {
				if r := reader.do(method, path, nil); r.status != 404 {
					t.Errorf("non-admin: %d", r.status)
				}
			}
		})
	}
}

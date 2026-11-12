package httpapi

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/store"
)

// Regression tests for the security review (ADR-027).

func TestUnknownMethodRejected(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	for _, m := range []string{"X" + strings.Repeat("A", 5000), "PROPFIND", "TRACE"} {
		req, _ := http.NewRequest(m, h.srv.URL+"/api/v1/me", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%.10s: %d", m, resp.StatusCode)
		}
	}
	if got := methodLabel("GET"); got != "GET" {
		t.Fatal(got)
	}
}

func TestUserInputBounds(t *testing.T) {
	visible := discovery.Target{ID: "demo/orders", Namespace: "demo", Name: "orders", Access: testAccess}
	hidden := discovery.Target{ID: "demo/secret", Namespace: "demo", Name: "secret"}
	h := newHarness(t, harnessOpts{targets: []discovery.Target{visible, hidden}})
	w := h.login("w@example.com", "writers")
	str := func(s string) *string { return &s }
	cases := []struct {
		name string
		body map[string]any
	}{
		{"huge target id", map[string]any{"name": "n", "sql": "select 1", "targetId": strings.Repeat("x", 100<<10)}},
		{"invisible target", map[string]any{"name": "n", "sql": "select 1", "targetId": *str("demo/secret")}},
		{"long database", map[string]any{"name": "n", "sql": "select 1", "targetId": "demo/orders", "database": strings.Repeat("d", 64)}},
	}
	for _, tc := range cases {
		if r := w.do(http.MethodPost, "/api/v1/saved-queries", tc.body); r.status != 400 {
			t.Errorf("%s: %d %s", tc.name, r.status, r.body)
		}
	}
	if r := w.do(http.MethodPost, "/api/v1/saved-queries", map[string]any{"name": "ok", "sql": "select 1", "targetId": "demo/orders", "database": "app"}); r.status != 201 {
		t.Fatalf("valid saved query: %d %s", r.status, r.body)
	}
	// drafts only for visible targets and real identifier lengths
	if r := w.do(http.MethodPut, "/api/v1/drafts/demo/secret/app", map[string]any{"sql": "x"}); r.status != 404 {
		t.Fatalf("draft for an invisible target: %d", r.status)
	}
	if r := w.do(http.MethodPut, "/api/v1/drafts/demo/orders/"+strings.Repeat("d", 64), map[string]any{"sql": "x"}); r.status != 404 {
		t.Fatalf("draft for an impossible database name: %d", r.status)
	}
	// path names longer than PostgreSQL identifiers never reach a pool
	if r := w.get("/api/v1/targets/demo/orders/db/" + strings.Repeat("d", 64) + "/schemas"); r.status != 404 {
		t.Fatalf("long db name: %d", r.status)
	}
	// UI state: capped per user, updates of existing keys still work
	for i := range maxUIStateKeys {
		if r := w.do(http.MethodPut, fmt.Sprintf("/api/v1/ui-state/k%d", i), map[string]any{"value": 1}); r.status != 204 {
			t.Fatalf("ui state %d: %d", i, r.status)
		}
	}
	if r := w.do(http.MethodPut, "/api/v1/ui-state/one-too-many", map[string]any{"value": 1}); r.status != 409 || r.errCode() != "limit_reached" {
		t.Fatalf("ui state cap: %d %s", r.status, r.body)
	}
	if r := w.do(http.MethodPut, "/api/v1/ui-state/k0", map[string]any{"value": 2}); r.status != 204 {
		t.Fatalf("update existing key: %d", r.status)
	}
}

func TestAuditBoundsAndExportDefused(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	admin := h.login("admin@example.com", "admins")
	h.server.Audit.Write(context.Background(), store.AuditRecord{Event: "row_update", Result: "denied",
		TargetID: strings.Repeat("t", 1<<20), Database: "=1+1", Object: "@SUM(A1)", Details: strings.Repeat("d", 1<<20)})
	recs := h.audits("row_update")
	if len(recs) != 1 || len(recs[0].TargetID) > 512 || len(recs[0].Details) > 8<<10 {
		t.Fatalf("audit fields not bounded: target %d details %d", len(recs[0].TargetID), len(recs[0].Details))
	}
	r := admin.get("/api/v1/admin/audit/export?format=csv&event=row_update")
	rows, err := csv.NewReader(strings.NewReader(string(r.body))).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatalf("csv: %v %d", err, len(rows))
	}
	if rows[1][9] != "'=1+1" || rows[1][10] != "'@SUM(A1)" {
		t.Fatalf("formula cells not defused: %q %q", rows[1][9], rows[1][10])
	}
}

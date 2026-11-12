//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/testutil/pgtest"
)

const rowsPath = dbPath + "/relations/shop/customers/rows"

func TestGridWrites(t *testing.T) {
	each(t, func(t *testing.T, in *pgtest.Instance) {
		h, _ := dbHarness(t, in, nil)
		w := h.login("w@example.com", "writers")

		var ins struct{ Row []*string }
		r := w.do(http.MethodPost, rowsPath, map[string]any{"values": map[string]any{"email": "new@" + in.Image + ".test", "name": nil}})
		r.json(t, &ins)
		if r.status != 201 || ins.Row[0] == nil || *ins.Row[1] != "new@"+in.Image+".test" || ins.Row[2] != nil || ins.Row[3] == nil {
			t.Fatalf("insert: %d %s", r.status, r.body)
		}
		id := *ins.Row[0]

		var upd struct{ Row []*string }
		r = w.do(http.MethodPatch, rowsPath, map[string]any{"pk": map[string]any{"id": id}, "set": map[string]any{"name": "Renamed"}, "original": map[string]any{"name": nil}})
		r.json(t, &upd)
		if r.status != 200 || *upd.Row[2] != "Renamed" {
			t.Fatalf("update: %d %s", r.status, r.body)
		}

		// concurrent modification between load and save -> 409, nothing changed
		ctx := context.Background()
		admin, err := in.Connect(ctx, "app")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = admin.Close(ctx) }()
		if _, err := admin.Exec(ctx, "UPDATE shop.customers SET name = 'changed in psql' WHERE id = $1", id); err != nil {
			t.Fatal(err)
		}
		r = w.do(http.MethodPatch, rowsPath, map[string]any{"pk": map[string]any{"id": id}, "set": map[string]any{"name": "mine"}, "original": map[string]any{"name": "Renamed"}})
		if r.status != 409 || r.errCode() != "row_conflict" {
			t.Fatalf("conflict: %d %s", r.status, r.body)
		}
		var name string
		_ = admin.QueryRow(ctx, "SELECT name FROM shop.customers WHERE id = $1", id).Scan(&name)
		if name != "changed in psql" {
			t.Fatalf("conflicting update was applied: %q", name)
		}

		// json column optimistic check works (no equality operator on json)
		r = w.do(http.MethodPatch, dbPath+"/relations/shop/orders/rows", map[string]any{"pk": map[string]any{"id": "1"},
			"set": map[string]any{"meta": `{"edited": true}`}, "original": map[string]any{"meta": `{"n": 1}`}})
		if r.status != 200 {
			t.Fatalf("json update: %d %s", r.status, r.body)
		}

		// bulk delete is all-or-nothing
		r = w.do(http.MethodDelete, rowsPath, map[string]any{"pks": []map[string]any{{"id": id}, {"id": "999999"}}})
		if r.status != 409 {
			t.Fatalf("bulk delete with missing pk: %d", r.status)
		}
		var n int
		_ = admin.QueryRow(ctx, "SELECT count(*) FROM shop.customers WHERE id = $1", id).Scan(&n)
		if n != 1 {
			t.Fatal("partial bulk delete was committed")
		}
		r = w.do(http.MethodDelete, rowsPath, map[string]any{"pks": []map[string]any{{"id": id}}})
		if r.status != 200 || !strings.Contains(string(r.body), `"deleted":1`) {
			t.Fatalf("delete: %d %s", r.status, r.body)
		}

		// not editable relations
		for rel, reason := range map[string]string{"shop/nopk": "no_primary_key", "shop/paid_orders": "not_a_table"} {
			r = w.do(http.MethodPost, dbPath+"/relations/"+rel+"/rows", map[string]any{"values": map[string]any{}})
			if r.status != 403 || r.errCode() != "not_editable" || !strings.Contains(string(r.body), reason) {
				t.Errorf("%s: %d %s", rel, r.status, r.body)
			}
		}

		// bad input
		if r = w.do(http.MethodPost, rowsPath, map[string]any{"values": map[string]any{"nope": "x"}}); r.status != 400 {
			t.Errorf("unknown column: %d", r.status)
		}
		if r = w.do(http.MethodPatch, rowsPath, map[string]any{"pk": map[string]any{"id": "1"}, "set": map[string]any{"name": "x"}}); r.status != 400 {
			t.Errorf("update without original: %d", r.status)
		}

		// audit: pk and column names, never values (audit.logValues=false)
		recs := h.audits("row_update")
		if len(recs) == 0 || !strings.Contains(recs[0].Details, `"pk"`) || strings.Contains(recs[0].Details, "Renamed") || strings.Contains(recs[0].Details, "mine") {
			t.Fatalf("row_update audit: %+v", recs)
		}
		count := func(event, result string) int {
			n := 0
			for _, a := range h.audits(event) {
				if a.Result == result {
					n++
				}
			}
			return n
		}
		// failed attempts are audited too (not editable, unknown column, conflicts)
		if count("row_insert", "ok") != 1 || count("row_insert", "error") != 3 || count("row_delete", "ok") != 1 || count("row_delete", "error") != 1 {
			t.Fatalf("audits: insert ok=%d err=%d delete ok=%d err=%d", count("row_insert", "ok"), count("row_insert", "error"),
				count("row_delete", "ok"), count("row_delete", "error"))
		}
	})
}

func TestGridWritesForbiddenForReaders(t *testing.T) {
	in := instances[0]
	h, _ := dbHarness(t, in, nil)
	rd := h.login("r@example.com", "readers")
	for _, req := range []struct {
		method string
		body   map[string]any
	}{
		{http.MethodPost, map[string]any{"values": map[string]any{"email": "x@y"}}},
		{http.MethodPatch, map[string]any{"pk": map[string]any{"id": "1"}, "set": map[string]any{"name": "x"}, "original": map[string]any{"name": nil}}},
		{http.MethodDelete, map[string]any{"pks": []map[string]any{{"id": "1"}}}},
	} {
		if r := rd.do(req.method, rowsPath, req.body); r.status != 403 || r.errCode() != "forbidden" {
			t.Errorf("%s: %d %s", req.method, r.status, r.body)
		}
	}
	if len(h.audits("access_denied")) != 3 {
		t.Fatalf("access_denied audits: %d", len(h.audits("access_denied")))
	}
}

func TestGridWritesProductionConfirm(t *testing.T) {
	in := instances[0]
	h, _ := dbHarness(t, in, func(tg *discovery.Target) { tg.Environment = "production" })
	w := h.login("w@example.com", "writers")
	body := map[string]any{"pk": map[string]any{"id": "2"}, "set": map[string]any{"name": "prod"}, "original": map[string]any{"name": "Customer 2"}}
	if r := w.do(http.MethodPatch, rowsPath, body); r.status != 409 || r.errCode() != "confirmation_required" {
		t.Fatalf("no confirm: %d %s", r.status, r.body)
	}
	body["confirm"] = true
	if r := w.do(http.MethodPatch, rowsPath, body); r.status != 200 {
		t.Fatalf("confirmed: %d %s", r.status, r.body)
	}
}

// TestTargetAccessAnnotations: the readonly and access annotations are
// enforced server-side for grid writes and the editor (ADR-022).
func TestTargetAccessAnnotations(t *testing.T) {
	in := instances[0]
	h, _ := dbHarness(t, in, func(tg *discovery.Target) {
		tg.ReadOnly = true
		tg.Access = &discovery.AccessPolicy{Grants: []discovery.AccessGrant{
			{Group: "system-admins", Level: "write"}, {Group: "developers", Level: "read"},
		}}
	})
	// writers had write through the config rules; the access annotation
	// replaces them, so they lose the target entirely
	if r := h.login("w@example.com", "writers").get(dbPath + "/schemas"); r.status != 404 {
		t.Fatalf("writers via rules: %d", r.status)
	}
	// system-admins get rw from the annotation, capped to read by readonly
	sa := h.login("sa@example.com", "system-admins")
	var out struct{ Targets []targetJSON }
	sa.get("/api/v1/targets").json(t, &out)
	if len(out.Targets) != 1 || out.Targets[0].Level != "read" {
		t.Fatalf("targets %+v", out.Targets)
	}
	if r := sa.do(http.MethodPost, rowsPath, map[string]any{"values": map[string]any{"email": "x@y"}}); r.status != 403 {
		t.Fatalf("grid insert on readonly target: %d %s", r.status, r.body)
	}
	if r := run(sa, "update shop.orders set status = 'paid' where id = 1", nil); r.status != 403 || r.errCode() != "forbidden_statement" {
		t.Fatalf("editor dml on readonly target: %d %s", r.status, r.body)
	}
	if r := run(h.login("d@example.com", "developers"), "select count(*) from shop.orders", nil); r.status != 200 {
		t.Fatalf("developers read: %d %s", r.status, r.body)
	}
}

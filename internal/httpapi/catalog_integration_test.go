//go:build integration

package httpapi

import (
	"net/url"
	"slices"
	"testing"

	"github.com/Warewave-Technology/athens/internal/catalog"
	"github.com/Warewave-Technology/athens/internal/testutil/pgtest"
)

func TestCatalogEndpoints(t *testing.T) {
	each(t, func(t *testing.T, in *pgtest.Instance) {
		h, _ := dbHarness(t, in, nil)
		w := h.login("w@example.com", "writers")
		r := h.login("r@example.com", "readers")

		var dbs struct{ Databases []databaseJSON }
		w.get("/api/v1/targets/itest/db/databases").json(t, &dbs)
		if len(dbs.Databases) == 0 || !slices.ContainsFunc(dbs.Databases, func(d databaseJSON) bool { return d.Name == "app" && d.Level == "write" }) {
			t.Fatalf("databases %+v", dbs)
		}

		var schemas struct{ Schemas []catalog.Schema }
		w.get(dbPath+"/schemas").json(t, &schemas)
		names := []string{}
		for _, s := range schemas.Schemas {
			names = append(names, s.Name)
		}
		if !slices.Equal(names, []string{"public", "shop"}) {
			t.Fatalf("schemas %v", names)
		}
		w.get(dbPath+"/schemas?system=true").json(t, &schemas)
		if len(schemas.Schemas) < 4 {
			t.Fatalf("system schemas %d", len(schemas.Schemas))
		}

		var rels struct {
			Relations []relationJSON
			Capped    bool
		}
		w.get(dbPath+"/relations").json(t, &rels)
		byName := map[string]relationJSON{}
		for _, rel := range rels.Relations {
			byName[rel.Schema+"."+rel.Name] = rel
		}
		check := func(name string, kind catalog.Kind, editable bool, reason string) {
			t.Helper()
			rel, ok := byName[name]
			if !ok {
				t.Fatalf("%s missing", name)
			}
			got := ""
			if rel.EditableReason != nil {
				got = *rel.EditableReason
			}
			if rel.Kind != kind || rel.Editable != editable || got != reason {
				t.Errorf("%s: kind=%s editable=%v reason=%q", name, rel.Kind, rel.Editable, got)
			}
		}
		check("shop.orders", catalog.KindTable, true, "")
		check("shop.events", catalog.KindPartitioned, true, "")
		check("shop.nopk", catalog.KindTable, false, "no_primary_key")
		check("shop.paid_orders", catalog.KindView, false, "not_a_table")
		check("shop.order_totals", catalog.KindMatview, false, "not_a_table")
		check("shop.invoice_seq", catalog.KindSequence, false, "not_a_table")
		check(`public.Weird "Name"`, catalog.KindTable, true, "")
		if p := byName["shop.events_2024"].Parent; p == nil || p.Name != "events" {
			t.Errorf("partition parent %+v", p)
		}
		if est := byName["shop.orders"].RowEstimate; est == nil || *est != 300 {
			t.Errorf("row estimate %v", est)
		}
		if byName["shop.customers"].Comment == nil || byName["shop.orders"].SizeBytes == nil {
			t.Error("comment/size missing")
		}
		var rrels struct{ Relations []relationJSON }
		r.get(dbPath+"/relations").json(t, &rrels)
		for _, rel := range rrels.Relations {
			if rel.Schema == "shop" && rel.Name == "orders" && (rel.Editable || *rel.EditableReason != "read_only_access") {
				t.Errorf("reader editability %+v", rel)
			}
		}

		var st catalog.Structure
		w.get(dbPath+"/relations/shop/orders/structure").json(t, &st)
		if len(st.Columns) != 8 || st.PrimaryKey == nil || st.PrimaryKey.Columns[0] != "id" {
			t.Fatalf("structure %+v", st)
		}
		col := func(n string) catalog.ColumnInfo {
			for _, c := range st.Columns {
				if c.Name == n {
					return c
				}
			}
			t.Fatalf("column %s missing", n)
			return catalog.ColumnInfo{}
		}
		if c := col("amount"); c.Type != "numeric(12,2)" || !c.NotNull {
			t.Errorf("amount %+v", c)
		}
		if c := col("total_with_tax"); c.Generated == nil || *c.Generated != "stored" {
			t.Errorf("generated %+v", c)
		}
		if c := col("status"); c.Default == nil || c.Type != "shop.status" {
			t.Errorf("status %+v", c)
		}
		if len(st.ForeignKeys) != 1 || st.ForeignKeys[0].RefTable != "customers" || st.ForeignKeys[0].OnDelete != "cascade" || st.ForeignKeys[0].Direction != "out" {
			t.Errorf("fks %+v", st.ForeignKeys)
		}
		if len(st.Checks) != 1 || len(st.Indexes) != 2 || len(st.Triggers) != 1 || st.Triggers[0].Enabled != "origin" {
			t.Errorf("checks=%d indexes=%d triggers=%+v", len(st.Checks), len(st.Indexes), st.Triggers)
		}
		var cust catalog.Structure
		w.get(dbPath+"/relations/shop/customers/structure").json(t, &cust)
		if len(cust.ForeignKeys) != 1 || cust.ForeignKeys[0].Direction != "in" || cust.ForeignKeys[0].RefTable != "orders" ||
			len(cust.Uniques) != 1 || *cust.Columns[0].Identity != "always" {
			t.Errorf("customers %+v", cust)
		}
		var weird catalog.Structure
		if resp := w.get(dbPath + "/relations/public/" + url.PathEscape(`Weird "Name"`) + "/structure"); resp.status != 200 {
			t.Fatalf("quoted name: %d %s", resp.status, resp.body)
		} else {
			resp.json(t, &weird)
		}
		if resp := w.get(dbPath + "/relations/shop/missing/structure"); resp.status != 404 {
			t.Errorf("missing relation %d", resp.status)
		}
	})
}

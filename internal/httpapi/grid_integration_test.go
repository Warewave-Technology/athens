//go:build integration

package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Warewave-Technology/athens/internal/testutil/pgtest"
)

func gridQuery(t *testing.T, c *client, rel string, body map[string]any) (gridResponse, response) {
	t.Helper()
	r := c.do(http.MethodPost, dbPath+"/relations/"+rel+"/rows/query", body)
	var g gridResponse
	if r.status == 200 {
		r.json(t, &g)
	}
	return g, r
}

func TestGridPagingSortFilter(t *testing.T) {
	each(t, func(t *testing.T, in *pgtest.Instance) {
		h, _ := dbHarness(t, in, nil)
		c := h.login("r@example.com", "readers")

		g, r := gridQuery(t, c, "shop/orders", map[string]any{"limit": 25, "offset": 50})
		if r.status != 200 || len(g.Rows) != 25 || *g.Rows[0][0] != "51" || g.Total == nil || *g.Total != 300 || g.TotalExact {
			t.Fatalf("page: %d %s total=%v", r.status, r.body[:min(200, len(r.body))], g.Total)
		}
		if len(g.Columns) != 8 || g.Columns[3].Type != "numeric(12,2)" || !slices.Equal(g.PrimaryKey, []string{"id"}) {
			t.Fatalf("columns %+v pk %v", g.Columns, g.PrimaryKey)
		}

		g, _ = gridQuery(t, c, "shop/orders", map[string]any{"limit": 5, "sort": []map[string]string{{"column": "amount", "dir": "desc"}}})
		if *g.Rows[0][3] != "450.00" || *g.Rows[1][3] != "448.50" {
			t.Fatalf("sort: %v %v", *g.Rows[0][3], *g.Rows[1][3])
		}

		g, _ = gridQuery(t, c, "shop/orders", map[string]any{"limit": 500, "exactCount": true, "filters": []map[string]any{
			{"column": "status", "op": "in", "value": []string{"paid", "shipped"}},
			{"column": "amount", "op": "gte", "value": "100"},
			{"column": "meta", "op": "not_null"},
		}})
		if g.Total == nil || !g.TotalExact || *g.Total != int64(len(g.Rows)) || len(g.Rows) == 0 {
			t.Fatalf("filtered exact: total=%v rows=%d", g.Total, len(g.Rows))
		}
		for _, row := range g.Rows {
			if *row[2] == "new" {
				t.Fatal("in filter not applied")
			}
		}

		// estimated count with filters (planner estimate), not exact
		g, _ = gridQuery(t, c, "shop/orders", map[string]any{"limit": 10, "filters": []map[string]any{{"column": "status", "op": "eq", "value": "paid"}}})
		if g.Total == nil || g.TotalExact || *g.Total < 50 || *g.Total > 200 {
			t.Fatalf("estimate: %v exact=%v", g.Total, g.TotalExact)
		}

		g, _ = gridQuery(t, c, "shop/customers", map[string]any{"filters": []map[string]any{{"column": "email", "op": "ilike", "value": "C1_@EXAMPLE.COM"}}})
		if len(g.Rows) != 10 || !g.TotalExact {
			t.Fatalf("ilike rows %d", len(g.Rows))
		}

		// quoted identifiers and views
		g, r = gridQuery(t, c, "public/"+url.PathEscape(`Weird "Name"`), map[string]any{"sort": []map[string]string{{"column": "select"}}})
		if r.status != 200 || *g.Rows[0][1] != "quoted" {
			t.Fatalf("weird: %d %s", r.status, r.body)
		}
		if _, r = gridQuery(t, c, "shop/paid_orders", map[string]any{"limit": 3}); r.status != 200 {
			t.Fatalf("view: %d", r.status)
		}

		// every page read is audited; filter values only with audit.logValues
		reads := h.audits("rows_read")
		if len(reads) != 7 || reads[0].Object != "shop.paid_orders" || reads[0].RowsAffected == nil || *reads[0].RowsAffected != 3 {
			t.Fatalf("rows_read: %d %+v", len(reads), reads[0])
		}
		for _, a := range reads {
			if strings.Contains(a.Details, "C1_@") || strings.Contains(a.Details, "paid") {
				t.Fatalf("filter value in audit: %s", a.Details)
			}
		}
	})
}

func TestGridInjectionRejected(t *testing.T) {
	in := instances[0]
	h, _ := dbHarness(t, in, nil)
	c := h.login("w@example.com", "writers")
	bad := []map[string]any{
		{"filters": []map[string]any{{"column": `status" = 'x' OR 1=1 --`, "op": "eq", "value": "x"}}},
		{"sort": []map[string]string{{"column": "id", "dir": "asc; DROP TABLE shop.orders"}}},
		{"filters": []map[string]any{{"column": "id", "op": "eq); DROP TABLE shop.orders; --", "value": "1"}}},
		{"limit": 100000},
	}
	for _, b := range bad {
		if _, r := gridQuery(t, c, "shop/orders", b); r.status != 400 || r.errCode() != "invalid_filter" {
			t.Errorf("%v: %d %s", b, r.status, r.body)
		}
	}
	// values are bound: a malicious value is just a value that fails to parse or matches nothing
	g, r := gridQuery(t, c, "shop/orders", map[string]any{"filters": []map[string]any{{"column": "meta", "op": "like", "value": "%'; DROP TABLE shop.orders; --"}}})
	if r.status != 200 || len(g.Rows) != 0 {
		t.Fatalf("bound like: %d", r.status)
	}
	if _, r = gridQuery(t, c, "shop/orders", map[string]any{"filters": []map[string]any{{"column": "id", "op": "eq", "value": "1 OR 1=1"}}}); r.status != 422 || r.errCode() != "db_error" {
		t.Fatalf("bad int literal: %d %s", r.status, r.body)
	}
	g, _ = gridQuery(t, c, "shop/orders", map[string]any{"limit": 1})
	if g.Total == nil || *g.Total != 300 {
		t.Fatal("orders table damaged?")
	}
	if _, r = gridQuery(t, c, "shop/nope", nil); r.status != 404 {
		t.Fatalf("missing relation %d", r.status)
	}
}

func TestGridTrickyTypesAndCells(t *testing.T) {
	each(t, func(t *testing.T, in *pgtest.Instance) {
		h, _ := dbHarness(t, in, nil)
		c := h.login("r@example.com", "readers")
		g, r := gridQuery(t, c, "shop/types", nil)
		if r.status != 200 || len(g.Rows) != 2 {
			t.Fatalf("%d %s", r.status, r.body)
		}
		row := g.Rows[0]
		want := map[string]string{
			"n": "12345678901234567890.123456789", "i8": "9223372036854775807", "j": `{"k": [1, "two", null]}`,
			"arr": "{1,NULL,3}", "tarr": `{"a b","c,d","e\"f"}`, "b": `\xdeadbeef`, "ts": "2024-02-29 23:59:59.123456+00",
			"iv": "1 year 2 mons 3 days 04:05:06.789", "u": "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "st": "paid",
			"comp": "(7,seven)", "r": "[1,10)", "f": "NaN",
		}
		for i, col := range g.Columns {
			if w, ok := want[col.Name]; ok && (row[i] == nil || *row[i] != w) {
				t.Errorf("%s = %v want %q", col.Name, deref(row[i]), w)
			}
		}
		for i, v := range g.Rows[1][1:] {
			if v != nil {
				t.Errorf("null row col %d = %q", i+1, *v)
			}
		}
		// the long text column is cut at maxCellBytes and flagged
		tcol := slices.IndexFunc(g.Columns, func(c gridColumn) bool { return c.Name == "t" })
		if len(*row[tcol]) != h.cfg.Limits.MaxCellBytes || !slices.Contains(g.TruncatedCells, [2]int{0, tcol}) {
			t.Fatalf("truncation len=%d cells=%v", len(*row[tcol]), g.TruncatedCells)
		}
		pk := base64.RawURLEncoding.EncodeToString([]byte(`{"id":"1"}`))
		cr := c.get(dbPath + "/relations/shop/types/rows/cell?column=t&pk=" + pk)
		var cell struct{ Value *string }
		cr.json(t, &cell)
		if cr.status != 200 || len(*cell.Value) != 100000 {
			t.Fatalf("cell: %d len=%d", cr.status, len(deref(cell.Value)))
		}
		if cr := c.get(dbPath + "/relations/shop/types/rows/cell?column=t&pk=" + base64.RawURLEncoding.EncodeToString([]byte(`{"id":"99"}`))); cr.status != 404 {
			t.Fatalf("missing row: %d", cr.status)
		}
		if cr := c.get(dbPath + "/relations/shop/nopk/rows/cell?column=a&pk=" + base64.RawURLEncoding.EncodeToString([]byte(`{"a":"1"}`))); cr.status != 400 {
			t.Fatalf("no pk: %d", cr.status)
		}
	})
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestGridExactCountTimeout(t *testing.T) {
	in := instances[0]
	ctx := context.Background()
	admin, err := in.Connect(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	// a view whose count is slow; selecting a page stays fast
	if _, err := admin.Exec(ctx, `CREATE OR REPLACE VIEW public.slow_count AS
		SELECT a.g AS id, b.g AS s FROM generate_series(1, 1000000) AS a(g) CROSS JOIN generate_series(1, 1000) AS b(g)`); err != nil {
		t.Fatal(err)
	}
	h, _ := dbHarness(t, in, nil)
	c := h.login("r@example.com", "readers")
	start := time.Now()
	g, r := gridQuery(t, c, "public/slow_count", map[string]any{"limit": 10, "exactCount": true})
	if r.status != 200 || !g.CountTimedOut || g.TotalExact || len(g.Rows) != 10 {
		t.Fatalf("status=%d timedOut=%v exact=%v rows=%d body=%s", r.status, g.CountTimedOut, g.TotalExact, len(g.Rows), r.body[:min(300, len(r.body))])
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("took %s", d)
	}
}

func TestGridPerformanceMillionRows(t *testing.T) {
	in := instances[0]
	ctx := context.Background()
	admin, err := in.Connect(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	if _, err := admin.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.big AS
		SELECT g AS id, md5(g::text) AS h, now() - (g || ' s')::interval AS at, g % 97 AS bucket FROM generate_series(1, 1000000) g;
		ALTER TABLE public.big ADD PRIMARY KEY (id); CREATE INDEX ON public.big(bucket); ANALYZE public.big`); err != nil {
		t.Fatal(err)
	}
	h, _ := dbHarness(t, in, nil)
	c := h.login("r@example.com", "readers")
	var times []time.Duration
	for i := range 40 {
		body := map[string]any{"limit": 100, "offset": i * 100}
		if i%2 == 1 {
			body["filters"] = []map[string]any{{"column": "bucket", "op": "eq", "value": fmt.Sprint(i)}}
			body["sort"] = []map[string]string{{"column": "id", "dir": "desc"}}
		}
		start := time.Now()
		g, r := gridQuery(t, c, "public/big", body)
		times = append(times, time.Since(start))
		if r.status != 200 || len(g.Rows) != 100 || g.Total == nil {
			t.Fatalf("page %d: %d", i, r.status)
		}
	}
	sort.Slice(times, func(a, b int) bool { return times[a] < times[b] })
	p95 := times[len(times)*95/100]
	t.Logf("1M rows: p50=%s p95=%s", times[len(times)/2], p95)
	if p95 > 300*time.Millisecond {
		t.Fatalf("p95 %s > 300ms", p95)
	}
}

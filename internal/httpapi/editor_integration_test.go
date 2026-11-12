//go:build integration

package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/query"
	"github.com/Warewave-Technology/athens/internal/testutil/pgtest"
)

func run(c *client, sql string, extra map[string]any) response {
	body := map[string]any{"sql": sql}
	for k, v := range extra {
		body[k] = v
	}
	return c.do(http.MethodPost, dbPath+"/query", body)
}

func TestEditorBasics(t *testing.T) {
	each(t, func(t *testing.T, in *pgtest.Instance) {
		h, _ := dbHarness(t, in, nil)
		w := h.login("w@example.com", "writers")

		r := run(w, "select id, status from shop.orders order by id limit 3", nil)
		var out query.Output
		r.json(t, &out)
		if r.status != 200 || len(out.Rows) != 3 || out.Class != "read" || out.CommandTag != "SELECT 3" || out.Columns[1].Type != "shop.status" {
			t.Fatalf("select: %d %s", r.status, r.body)
		}

		r = run(w, "select g from generate_series(1, 5000) g", map[string]any{"limit": 10})
		r.json(t, &out)
		if len(out.Rows) != 10 || !out.Truncated {
			t.Fatalf("cap: rows=%d truncated=%v", len(out.Rows), out.Truncated)
		}

		r = run(w, "update shop.orders set meta = meta where id <= 5", nil)
		r.json(t, &out)
		if r.status != 200 || out.RowsAffected == nil || *out.RowsAffected != 5 || out.Class != "dml" {
			t.Fatalf("update: %d %s", r.status, r.body)
		}

		r = run(w, "do $$ begin raise notice 'hello %', 42; end $$", map[string]any{"confirm": true})
		r.json(t, &out)
		if r.status != 200 || len(out.Notices) != 1 || out.Notices[0].Message != "hello 42" {
			t.Fatalf("notices: %d %s", r.status, r.body)
		}

		for sql, code := range map[string]string{
			"select 1; select 2":        "multiple_statements",
			"begin":                     "unsupported_statement",
			"set statement_timeout = 0": "unsupported_statement",
			"vacuum shop.orders":        "unsupported_statement",
			"select 1,\n  from x":       "syntax_error",
			"":                          "bad_request",
		} {
			if r := run(w, sql, nil); r.status != 400 || r.errCode() != code {
				t.Errorf("%q: %d %s", sql, r.status, r.body)
			}
		}
		r = run(w, "select 1,\n  from x", nil)
		if !strings.Contains(string(r.body), `"line":2`) || !strings.Contains(string(r.body), `"column":3`) {
			t.Errorf("syntax position: %s", r.body)
		}
		r = run(w, "-- comment\n  select nope\nfrom shop.orders", nil)
		if r.status != 422 || r.errCode() != "db_error" || !strings.Contains(string(r.body), `"sqlstate":"42703"`) ||
			!strings.Contains(string(r.body), `"line":2,"column":10`) {
			t.Errorf("db error: %d %s", r.status, r.body)
		}
	})
}

func TestEditorConfirmAndAudit(t *testing.T) {
	in := instances[0]
	h, _ := dbHarness(t, in, nil)
	w := h.login("w@example.com", "writers")
	if r := run(w, "create table public.to_drop(id int)", nil); r.status != 200 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	r := run(w, "DROP TABLE public.to_drop", nil)
	if r.status != 409 || r.errCode() != "confirmation_required" || !strings.Contains(string(r.body), `"summary":"DROP TABLE public.to_drop"`) {
		t.Fatalf("drop without confirm: %d %s", r.status, r.body)
	}
	if n := len(h.audits("confirm_required")); n != 1 {
		t.Fatalf("confirm_required audits: %d", n)
	}
	if r := run(w, "DROP TABLE public.to_drop", map[string]any{"confirm": true}); r.status != 200 {
		t.Fatalf("drop with confirm: %d %s", r.status, r.body)
	}
	recs := h.audits("query_execute")
	found := false
	for _, a := range recs {
		if a.SQL == "DROP TABLE public.to_drop" && a.Destructive && a.Result == "ok" && a.StatementClass == "ddl" && a.Email == "w@example.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("drop not audited: %+v", recs)
	}
}

func TestEditorReadUser(t *testing.T) {
	in := instances[0]
	h, _ := dbHarness(t, in, nil)
	rd := h.login("r@example.com", "readers")
	for _, sql := range []string{
		"update shop.orders set status = 'paid' where id = 1",
		"with d as (delete from shop.orders where id = 1 returning *) select * from d",
		"select pg_terminate_backend(1)",
		"select set_config('role', 'postgres', false)",
		"create table x(id int)",
	} {
		if r := run(rd, sql, nil); r.status != 403 || r.errCode() != "forbidden_statement" {
			t.Errorf("%q: %d %s", sql, r.status, r.body)
		}
	}
	if n := len(h.audits("access_denied")); n != 5 {
		t.Fatalf("access_denied audits: %d", n)
	}
	if r := run(rd, "select count(*) from shop.orders", nil); r.status != 200 {
		t.Fatalf("read: %d %s", r.status, r.body)
	}
	// read users use the ro credential + READ ONLY; a volatile function the
	// denylist misses is still stopped by the transaction mode
	if r := run(rd, "select * from shop.orders limit 1 for update", nil); r.status != 403 {
		t.Fatalf("locking read: %d", r.status)
	}
}

func TestEditorProductionConfirm(t *testing.T) {
	in := instances[0]
	h, _ := dbHarness(t, in, func(tg *discovery.Target) { tg.Environment = "production" })
	w := h.login("w@example.com", "writers")
	r := run(w, "update shop.orders set meta = meta where id = 1", nil)
	if r.status != 409 || !strings.Contains(string(r.body), `"requireTypedConfirmation":true`) {
		t.Fatalf("prod dml: %d %s", r.status, r.body)
	}
	if r := run(w, "select 1", nil); r.status != 200 {
		t.Fatalf("prod read: %d", r.status)
	}
	if r := run(w, "update shop.orders set meta = meta where id = 1", map[string]any{"confirm": true}); r.status != 200 {
		t.Fatalf("prod dml confirmed: %d", r.status)
	}
}

func TestEditorCancelTimeoutConcurrency(t *testing.T) {
	in := instances[0]
	tg, client := in.Target("db")
	h := newHarness(t, harnessOpts{targets: []discovery.Target{tg}, client: client, mutate: func(c *config.Config) {
		c.Limits.ConcurrentQueriesPerUser = 2
		c.Limits.StatementTimeout = config.Duration(2 * time.Second)
	}})
	w := h.login("w@example.com", "writers")
	other := h.login("o@example.com", "writers")

	// cancel within 1s
	done := make(chan response, 1)
	go func() { done <- run(w, "select pg_sleep(60)", map[string]any{"clientQueryId": "sleep-0001"}) }()
	waitRunning(t, h, 1)
	if r := other.do(http.MethodPost, "/api/v1/queries/sleep-0001/cancel", nil); r.status != 404 {
		t.Fatalf("foreign cancel: %d", r.status)
	}
	start := time.Now()
	if r := w.do(http.MethodPost, "/api/v1/queries/sleep-0001/cancel", nil); r.status != 204 {
		t.Fatalf("cancel: %d %s", r.status, r.body)
	}
	r := <-done
	if took := time.Since(start); took > time.Second || r.errCode() != "cancelled" {
		t.Fatalf("cancelled after %s: %d %s", took, r.status, r.body)
	}
	if len(h.audits("query_cancel")) != 1 {
		t.Fatal("cancel not audited")
	}

	// statement_timeout -> 504
	if r := run(w, "select pg_sleep(10)", nil); r.status != 504 || r.errCode() != "timeout" {
		t.Fatalf("timeout: %d %s", r.status, r.body)
	}

	// per-user concurrency limit (2) -> 429
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run(w, "select pg_sleep(1.5)", map[string]any{"clientQueryId": fmt.Sprintf("conc-%04d", i)})
		}()
	}
	waitRunning(t, h, 2)
	if r := run(w, "select 1", nil); r.status != 429 || r.errCode() != "too_many_queries" {
		t.Fatalf("concurrency: %d %s", r.status, r.body)
	}
	if r := run(other, "select 1", nil); r.status != 200 {
		t.Fatalf("other user blocked: %d", r.status)
	}
	wg.Wait()
}

func waitRunning(t *testing.T, h *harness, n int) {
	t.Helper()
	for range 200 {
		if h.server.Query.Registry.Running() >= n {
			time.Sleep(100 * time.Millisecond) // let the statement reach the server
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never saw %d running queries", n)
}

func TestHistorySavedDrafts(t *testing.T) {
	in := instances[0]
	tg, client := in.Target("db")
	h := newHarness(t, harnessOpts{targets: []discovery.Target{tg}, client: client, mutate: func(c *config.Config) { c.Limits.HistoryPerUser = 5 }})
	w := h.login("w@example.com", "writers")
	for i := range 8 {
		run(w, fmt.Sprintf("select %d", i), nil)
	}
	run(w, "select broken from", nil)
	var hist struct {
		Items []struct {
			SQL, Result string
			Error       *string
		}
	}
	w.get("/api/v1/history?targetId=itest/db&database=app").json(t, &hist)
	if len(hist.Items) != 5 || hist.Items[0].Result != "error" || hist.Items[1].SQL != "select 7" {
		t.Fatalf("history: %+v", hist.Items)
	}

	r := w.do(http.MethodPost, "/api/v1/saved-queries", map[string]any{"name": "top orders", "sql": "select 1", "targetId": "itest/db"})
	if r.status != 201 {
		t.Fatalf("save: %d %s", r.status, r.body)
	}
	if r := w.do(http.MethodPost, "/api/v1/saved-queries", map[string]any{"name": "top orders", "sql": "select 2"}); r.status != 409 {
		t.Fatalf("dup: %d", r.status)
	}
	other := h.login("o@example.com", "writers")
	var saved struct{ Items []struct{ ID int64 } }
	other.get("/api/v1/saved-queries").json(t, &saved)
	if len(saved.Items) != 0 {
		t.Fatal("saved queries leak across users")
	}

	if r := w.get("/api/v1/drafts/itest/db/app"); r.status != 404 {
		t.Fatalf("no draft: %d", r.status)
	}
	if r := w.do(http.MethodPut, "/api/v1/drafts/itest/db/app", map[string]any{"sql": "select 42"}); r.status != 200 {
		t.Fatalf("put draft: %d", r.status)
	}
	if r := w.get("/api/v1/drafts/itest/db/app"); !strings.Contains(string(r.body), "select 42") {
		t.Fatalf("draft: %s", r.body)
	}
	_ = context.Background
}

// TestReadLogins (ADR-028): read users only ever use a read-only login. A
// target without one is invisible to them; a provisioned one works; a
// privileged one is refused.
func TestReadLogins(t *testing.T) {
	in := instances[0]

	h, _ := dbHarness(t, in, func(tg *discovery.Target) { tg.RO = nil })
	rd := h.login("r@example.com", "readers")
	var list struct{ Targets []struct{ ID string } }
	rd.get("/api/v1/targets").json(t, &list)
	if len(list.Targets) != 0 {
		t.Fatalf("target without a read-only login listed for read users: %+v", list.Targets)
	}
	if r := run(rd, "select 1", nil); r.status != 404 {
		t.Fatalf("editor without a read-only login: %d %s", r.status, r.body)
	}
	if _, r := gridQuery(t, rd, "shop/customers", map[string]any{"limit": 3}); r.status != 404 {
		t.Fatalf("grid without a read-only login: %d %s", r.status, r.body)
	}
	if r := run(h.login("w@example.com", "writers"), "select 1", nil); r.status != 200 {
		t.Fatalf("writers unaffected: %d %s", r.status, r.body)
	}

	hp, _ := dbHarness(t, in, func(tg *discovery.Target) { tg.RO, tg.ROProvision = nil, true })
	rp := hp.login("r@example.com", "readers")
	r := run(rp, "select session_user", nil)
	if r.status != 200 || !strings.Contains(string(r.body), `"athens_read"`) {
		t.Fatalf("provisioned login: %d %s", r.status, r.body)
	}
	if _, r := gridQuery(t, rp, "shop/customers", map[string]any{"limit": 3}); r.status != 200 {
		t.Fatalf("grid on the provisioned login: %d %s", r.status, r.body)
	}
	if r := run(rp, "update shop.orders set status = 'paid' where id = 1", nil); r.status != 403 {
		t.Fatalf("write on the provisioned login: %d", r.status)
	}
	// pg_read_all_data may read pg_authid; athens never lets it through
	if r := run(rp, "select rolpassword from pg_authid", nil); r.status != 400 || r.errCode() != "unsupported_statement" {
		t.Fatalf("pg_authid on the provisioned login: %d %s", r.status, r.body)
	}
	if _, r := gridQuery(t, rp, "pg_catalog/pg_authid", map[string]any{"limit": 5}); r.status != 404 {
		t.Fatalf("grid pg_authid on the provisioned login: %d %s", r.status, r.body)
	}

	// a read-only login that is a superuser is refused, admins are told
	hs, _ := dbHarness(t, in, func(tg *discovery.Target) { ro := tg.RW; tg.RO = &ro })
	rs := hs.login("r@example.com", "readers")
	if r := run(rs, "select 1", nil); r.status != 403 || r.errCode() != "read_login_privileged" {
		t.Fatalf("privileged read-only login: %d %s", r.status, r.body)
	}
	var adminList struct{ Targets []struct{ Warnings []string } }
	hs.login("a@example.com", "admins", "writers").get("/api/v1/targets").json(t, &adminList)
	if len(adminList.Targets) != 1 || !strings.Contains(strings.Join(adminList.Targets[0].Warnings, "|"), "read-only login is privileged") {
		t.Fatalf("admin warning: %+v", adminList.Targets)
	}
}

// TestEditorSecurityBoundaries (ADR-027).
func TestEditorSecurityBoundaries(t *testing.T) {
	in := instances[0]

	// dynamic SQL and server functions are never reads
	h2, _ := dbHarness(t, in, nil)
	rd2 := h2.login("r@example.com", "readers")
	for _, sql := range []string{
		"select query_to_xml('select set_config(''role'',''none'',true)',true,true,'')",
		"select table_to_xml('pg_authid',true,true,'')",
		"select ts_stat('select to_tsvector(''simple'', rolpassword) from pg_authid')",
		"select pg_read_file('PG_VERSION')",
		"explain select * from shop.orders where id = length(table_to_xml('pg_authid',true,true,'')::text)",
	} {
		if r := run(rd2, sql, nil); r.status != 403 || r.errCode() != "forbidden_statement" {
			t.Errorf("%s: %d %s", sql, r.status, r.body)
		}
	}
	if r := run(rd2, "select 1\u0000; delete from shop.orders", nil); r.status != 400 {
		t.Fatalf("NUL byte: %d", r.status)
	}

	// a writer's SELECT that writes through a function needs confirmation
	w := h2.login("w@example.com", "writers")
	if r := run(w, "create function shop.zz_touch() returns int language sql as $$ update shop.customers set name = name where id = 1 returning 1 $$", nil); r.status != 200 {
		t.Fatalf("create function: %d %s", r.status, r.body)
	}
	if r := run(w, "select shop.zz_touch()", nil); r.status != 409 || r.errCode() != "confirmation_required" {
		t.Fatalf("write through a function: %d %s", r.status, r.body)
	}
	if r := run(w, "select shop.zz_touch()", map[string]any{"confirm": true}); r.status != 200 {
		t.Fatalf("confirmed: %d %s", r.status, r.body)
	}
	if r := run(w, "select count(*) from shop.customers", nil); r.status != 200 {
		t.Fatalf("plain read by a writer: %d %s", r.status, r.body)
	}
}

// TestManyRowsNeedConfirmation (ADR-027): heuristics cannot see that
// "WHERE id = id" matches every row; the row count can.
func TestManyRowsNeedConfirmation(t *testing.T) {
	h, _ := dbHarness(t, instances[0], nil)
	w := h.login("w@example.com", "writers")
	if r := run(w, "create table public.zbig as select g as id from generate_series(1, 2000) g", nil); r.status != 200 {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	r := run(w, "delete from public.zbig where id = id", nil)
	if r.status != 409 || r.errCode() != "confirmation_required" || !strings.Contains(string(r.body), "2000") {
		t.Fatalf("unconfirmed mass delete: %d %s", r.status, r.body)
	}
	if r := run(w, "select count(*)::text from public.zbig", nil); !strings.Contains(string(r.body), "2000") {
		t.Fatalf("rows were deleted without confirmation: %s", r.body)
	}
	for _, sql := range []string{
		"with d as (delete from public.zbig where id = id returning 1) select count(*) from d",
		"explain analyze update public.zbig set id = id where id > 0",
	} {
		if r := run(w, sql, nil); r.status != 409 || r.errCode() != "confirmation_required" {
			t.Fatalf("%s: %d %s", sql, r.status, r.body)
		}
	}
	if r := run(w, "delete from public.zbig where id <= 10", nil); r.status != 200 {
		t.Fatalf("small delete: %d %s", r.status, r.body)
	}
	if r := run(w, "delete from public.zbig where id = id", map[string]any{"confirm": true}); r.status != 200 {
		t.Fatalf("confirmed: %d %s", r.status, r.body)
	}
}

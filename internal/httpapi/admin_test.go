package httpapi

import (
	"encoding/csv"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAuditAdminViewsAndExport(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	admin := h.login("admin@example.com", "admins")
	user := h.login("w@example.com", "writers")

	// viewing admin pages is audited once per page within adminViewEvery
	for range 3 {
		admin.get("/api/v1/admin/pools")
	}
	admin.get("/api/v1/admin/discovery")
	admin.get("/api/v1/admin/audit?event=login")
	if got := len(h.audits("admin_view")); got != 3 {
		t.Fatalf("admin_view records: %d", got)
	}

	// saved query changes are audited with the SQL
	var q struct{ ID int64 }
	user.do("POST", "/api/v1/saved-queries", map[string]string{"name": "n", "sql": "=cmd|' /C calc'!A0"}).json(t, &q)
	user.do("DELETE", "/api/v1/saved-queries/"+strconv.FormatInt(q.ID, 10), nil)
	if c, d := h.audits("saved_query_create"), h.audits("saved_query_delete"); len(c) != 1 || c[0].SQL == "" || len(d) != 1 {
		t.Fatalf("saved query audit: %+v %+v", c, d)
	}

	// export: admins only, every matching record, formula cells defused
	if r := user.get("/api/v1/admin/audit/export"); r.status != 404 {
		t.Fatalf("non-admin export: %d", r.status)
	}
	r := admin.get("/api/v1/admin/audit/export?format=csv&event=saved_query_create")
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Disposition"), "attachment;") {
		t.Fatalf("csv export: %d %s", r.status, r.header)
	}
	rows, err := csv.NewReader(strings.NewReader(string(r.body))).ReadAll()
	if err != nil || len(rows) != 2 || rows[0][2] != "event" || rows[1][2] != "saved_query_create" {
		t.Fatalf("csv rows %v %v", rows, err)
	}
	if sql := rows[1][13]; !strings.HasPrefix(sql, "'=") {
		t.Fatalf("formula not defused: %q", sql)
	}
	r = admin.get("/api/v1/admin/audit/export?format=jsonl")
	if r.status != 200 || strings.Count(string(r.body), "\n") < 5 {
		t.Fatalf("jsonl export: %d %s", r.status, r.body)
	}
	if r := admin.get("/api/v1/admin/audit/export?format=xml"); r.status != 400 {
		t.Fatalf("bad format: %d", r.status)
	}
	if ex := h.audits("audit_export"); len(ex) != 2 || ex[0].RowsAffected == nil {
		t.Fatalf("audit_export records: %+v", ex)
	}
}

func TestViewLogThrottle(t *testing.T) {
	var v viewLog
	now := time.Now()
	if !v.due("k", now) || v.due("k", now.Add(time.Minute)) || !v.due("other", now) {
		t.Fatal("throttle within window")
	}
	if !v.due("k", now.Add(adminViewEvery+time.Second)) {
		t.Fatal("throttle after window")
	}
}

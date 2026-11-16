//go:build integration

package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestActivity: writers see the running queries of the database, their own
// editor queries carry a cancellable query id, password statements are
// hidden and read users are refused (ADR-031).
func TestActivity(t *testing.T) {
	in := instances[0]
	h, _ := dbHarness(t, in, nil)
	w := h.login("w@example.com", "writers")
	other := h.login("o@example.com", "writers")

	done := make(chan response, 2)
	go func() { done <- run(w, "select pg_sleep(30) -- mine", map[string]any{"clientQueryId": "act-mine-01"}) }()
	go func() {
		done <- run(other, "select pg_sleep(30) /* password */", map[string]any{"clientQueryId": "act-other-01"})
	}()

	type item struct {
		PID         uint32 `json:"pid"`
		Application string `json:"application"`
		State       string `json:"state"`
		Query       string `json:"query"`
		QueryID     string `json:"queryId"`
	}
	var out struct {
		Now   string `json:"now"`
		Items []item `json:"items"`
	}
	// wait until both statements run on the server (slow under -race)
	var r response
	var mine, hidden *item
	for range 150 {
		r = w.do(http.MethodGet, dbPath+"/activity", nil)
		r.json(t, &out)
		if r.status != 200 || out.Now == "" {
			t.Fatalf("activity: %d %s", r.status, r.body)
		}
		mine, hidden = nil, nil
		for i, it := range out.Items {
			if strings.Contains(it.Query, "-- mine") {
				mine = &out.Items[i]
			}
			if strings.HasPrefix(it.Query, "<hidden") {
				hidden = &out.Items[i]
			}
		}
		if mine != nil && hidden != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if mine == nil || mine.QueryID != "act-mine-01" || mine.State != "active" || mine.Application != "athens:w@example.com" {
		t.Fatalf("own query: %+v in %s", mine, r.body)
	}
	if hidden == nil || hidden.QueryID != "" {
		t.Fatalf("other user's password statement: %+v in %s", hidden, r.body)
	}
	if strings.Contains(string(r.body), "act-other-01") {
		t.Fatal("another user's query id is exposed")
	}
	if n := len(h.audits("activity_view")); n != 1 {
		t.Fatalf("activity_view audits: %d", n)
	}
	w.do(http.MethodGet, dbPath+"/activity", nil)
	if n := len(h.audits("activity_view")); n != 1 {
		t.Fatalf("activity_view not throttled: %d", n)
	}

	if r := h.login("r@example.com", "readers").do(http.MethodGet, dbPath+"/activity", nil); r.status != 403 {
		t.Fatalf("read user: %d %s", r.status, r.body)
	}
	for _, c := range []*client{w, other} {
		c.do(http.MethodPost, "/api/v1/queries/act-mine-01/cancel", nil)
		c.do(http.MethodPost, "/api/v1/queries/act-other-01/cancel", nil)
	}
	<-done
	<-done
}

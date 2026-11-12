package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) *SQLite {
	t.Helper()
	s, err := OpenSQLite(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedUser(t *testing.T, s *SQLite, id string) {
	t.Helper()
	if err := s.UpsertUser(context.Background(), User{ID: id, Email: id + "@x", Name: id, LastLoginAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	for range 2 {
		s, err := OpenSQLite(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Close()
	}
}

func TestSessions(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	seedUser(t, s, "u1")
	now := time.Now()
	err := s.CreateSession(ctx, Session{ID: "h1", UserID: "u1", Groups: []string{"g1", "g2"}, CSRFToken: "c",
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ctx, "h1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "u1@x" || len(got.Groups) != 2 || got.CSRFToken != "c" || !got.ExpiresAt.After(now) {
		t.Fatalf("got %+v", got)
	}
	if _, err := s.GetSession(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	// idle purge
	n, err := s.PurgeSessions(ctx, now.Add(2*time.Minute), time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("purge n=%d err=%v", n, err)
	}
}

func TestHistoryTrim(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	seedUser(t, s, "u1")
	seedUser(t, s, "u2")
	for i := range 10 {
		if _, err := s.AddHistory(ctx, HistoryItem{UserID: "u1", TargetID: "ns/a", Database: "db", SQL: fmt.Sprint(i), Class: "read", Result: "ok", CreatedAt: time.Now()}, 5); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddHistory(ctx, HistoryItem{UserID: "u2", TargetID: "ns/a", Database: "db", SQL: "x", Class: "read", Result: "ok", CreatedAt: time.Now()}, 5); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListHistory(ctx, "u1", HistoryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 5 || items[0].SQL != "9" || items[4].SQL != "5" {
		t.Fatalf("items = %+v", items)
	}
	page, _ := s.ListHistory(ctx, "u1", HistoryFilter{Limit: 2, Before: items[1].ID})
	if len(page) != 2 || page[0].SQL != "7" {
		t.Fatalf("page = %+v", page)
	}
	if err := s.DeleteHistory(ctx, "u2", items[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user's delete: %v", err)
	}
}

func TestSavedQueries(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	seedUser(t, s, "u1")
	q, err := s.CreateSavedQuery(ctx, SavedQuery{UserID: "u1", Name: "a", SQL: "select 1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSavedQuery(ctx, SavedQuery{UserID: "u1", Name: "a", SQL: "select 2"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup err = %v", err)
	}
	q.SQL = "select 3"
	if u, err := s.UpdateSavedQuery(ctx, *q); err != nil || u.SQL != "select 3" {
		t.Fatalf("update %v %v", u, err)
	}
	if err := s.DeleteSavedQuery(ctx, "u1", q.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSavedQuery(ctx, "u1", q.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestDraftsAndUIState(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	seedUser(t, s, "u1")
	if _, err := s.GetDraft(ctx, "u1", "ns/a", "db"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	_, _ = s.PutDraft(ctx, "u1", "ns/a", "db", "select 1")
	_, _ = s.PutDraft(ctx, "u1", "ns/a", "db", "select 2")
	if d, err := s.GetDraft(ctx, "u1", "ns/a", "db"); err != nil || d.SQL != "select 2" {
		t.Fatalf("%v %v", d, err)
	}
	if err := s.PutUIState(ctx, "u1", "k", `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	if v, err := s.GetUIState(ctx, "u1", "k"); err != nil || v != `{"a":1}` {
		t.Fatalf("%v %v", v, err)
	}
}

func TestAuditAppendOnlyAndFilter(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	old := time.Now().AddDate(0, 0, -100)
	for i, ev := range []string{"login", "query_execute", "query_execute"} {
		at := time.Now()
		if i == 0 {
			at = old
		}
		if err := s.InsertAudit(ctx, AuditRecord{TS: at, Event: ev, UserID: "u1", Email: "U1@x", TargetID: "ns/a", Result: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE audit_log SET event = 'x'`); err == nil {
		t.Fatal("audit_log must reject UPDATE")
	}
	recs, err := s.ListAudit(ctx, AuditFilter{User: "u1@x", Event: "query_execute"})
	if err != nil || len(recs) != 2 {
		t.Fatalf("recs=%d err=%v", len(recs), err)
	}
	if n, err := s.PurgeAudit(ctx, time.Now().AddDate(0, 0, -90)); err != nil || n != 1 {
		t.Fatalf("purge %d %v", n, err)
	}
}

func TestHistoryByteTrimAndUserItems(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.UpsertUser(ctx, User{ID: "u1", Email: "u@x"}); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("x", 7<<20)
	for i := range 3 {
		if _, err := s.AddHistory(ctx, HistoryItem{UserID: "u1", SQL: big, Class: "read", Result: "ok", CreatedAt: time.Now().Add(time.Duration(i) * time.Second)}, 500); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.ListHistory(ctx, "u1", HistoryFilter{Limit: 10})
	if err != nil || len(items) != 2 {
		t.Fatalf("history kept %d items (want 2 within 16 MiB): %v", len(items), err)
	}
	if _, err := s.CreateSavedQuery(ctx, SavedQuery{UserID: "u1", Name: "a", SQL: "select 1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutDraft(ctx, "u1", "ns/t", "db", "select 22"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUIState(ctx, "u1", "k", `{"a":"çş"}`); err != nil {
		t.Fatal(err)
	}
	u, err := s.UserItems(ctx, "u1")
	// bytes, not characters: "çş" is 4 bytes
	if err != nil || u.Saved != 1 || u.Drafts != 1 || u.UIState != 1 || u.Bytes != int64(len("select 1")+len("select 22")+len(`{"a":"çş"}`)) {
		t.Fatalf("user items %+v %v", u, err)
	}
}

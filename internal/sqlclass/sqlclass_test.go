package sqlclass

import (
	"bufio"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestFixtures(t *testing.T) {
	f, err := os.Open("testdata/statements.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		head, sql, ok := strings.Cut(line, " | ")
		if !ok {
			t.Fatalf("bad line %q", line)
		}
		fields := strings.Fields(head)
		sql = strings.ReplaceAll(sql, `\n`, "\n")
		n++
		r, err := Classify(sql)
		if err != nil {
			t.Errorf("%q: %v", sql, err)
			continue
		}
		if string(r.Class) != fields[0] || r.Destructive != (fields[1] == "y") {
			t.Errorf("%q: got %s destructive=%v (%s), want %s %s", sql, r.Class, r.Destructive, r.Reason, fields[0], fields[1])
		}
	}
	if n < 80 {
		t.Fatalf("only %d fixtures, need >= 80", n)
	}
	t.Logf("%d statements classified", n)
}

func TestErrors(t *testing.T) {
	if _, err := Classify("select 1; select 2"); !errors.Is(err, ErrMultiple) {
		t.Errorf("multiple: %v", err)
	}
	if _, err := Classify("select 1; delete from x"); !errors.Is(err, ErrMultiple) {
		t.Errorf("multiple with dml: %v", err)
	}
	if _, err := Classify("  -- just a comment\n"); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty: %v", err)
	}
	_, err := Classify("select 1,\n  from x")
	var se *SyntaxError
	if !errors.As(err, &se) || se.Position != 13 {
		t.Fatalf("syntax: %#v", err)
	}
}

func TestSummaryAndStatement(t *testing.T) {
	r, _ := Classify("  DROP TABLE public.orders, shop.x CASCADE ;  ")
	if r.Summary != "DROP TABLE public.orders, shop.x" || r.Statement != "DROP TABLE public.orders, shop.x CASCADE" {
		t.Fatalf("%q / %q", r.Summary, r.Statement)
	}
	r, _ = Classify("delete from shop.orders")
	if r.Summary != "DELETE FROM shop.orders without WHERE" {
		t.Fatal(r.Summary)
	}
	r, _ = Classify("select 1 -- trailing comment")
	if !r.Cursorable || r.Statement != "select 1 -- trailing comment" {
		t.Fatalf("%+v", r)
	}
	r, _ = Classify("show all")
	if r.Cursorable {
		t.Fatal("SHOW is not cursorable")
	}
}

func TestCredentialCatalogsRefused(t *testing.T) {
	for _, sql := range []string{
		"SELECT rolname, rolpassword FROM pg_authid",
		"select * from PG_CATALOG.PG_SHADOW",
		"TABLE pg_authid",
		"SELECT count(*) FROM pg_user_mappings",
		"WITH x AS (SELECT * FROM pg_catalog.pg_authid) SELECT 1 FROM x",
		"EXPLAIN ANALYZE SELECT * FROM pg_authid",
	} {
		r, err := Classify(sql)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		if r.Class != Session || r.Reason == "" {
			t.Errorf("%s: class %s, want session (refused)", sql, r.Class)
		}
	}
	// EXPLAIN without ANALYZE returns only the plan, no rows
	for _, sql := range []string{"SELECT rolname FROM pg_roles", "SELECT * FROM myapp.pg_authid", "EXPLAIN SELECT * FROM pg_authid"} {
		if r, _ := Classify(sql); r.Class != Read {
			t.Errorf("%s: class %s, want read", sql, r.Class)
		}
	}
}

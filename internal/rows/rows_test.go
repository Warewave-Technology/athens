package rows

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Warewave-Technology/athens/internal/catalog"
)

var update = flag.Bool("update", false, "rewrite golden file")

var orders = Table{
	Schema: "shop", Name: "orders", PK: []string{"id"},
	Columns: []catalog.ColumnInfo{
		{Name: "id", Type: "bigint", TypeOID: 20, ArrayOID: 1016},
		{Name: "status", Type: "shop.status", TypeOID: 90001, ArrayOID: 90002},
		{Name: "amount", Type: "numeric(12,2)", TypeOID: 1700, ArrayOID: 1231},
		{Name: `we"ird col`, Type: "text", TypeOID: 25, ArrayOID: 1009},
	},
}

var weird = Table{
	Schema: `my "schema"`, Name: "Table; DROP TABLE x", PK: []string{"a", "b"},
	Columns: []catalog.ColumnInfo{{Name: "a", TypeOID: 23, ArrayOID: 1007}, {Name: "b", TypeOID: 25, ArrayOID: 1009}, {Name: "select", TypeOID: 25}},
}

func s(v string) *string { return &v }

func TestGolden(t *testing.T) {
	cases := []struct {
		name  string
		build func() (*Statement, error)
	}{
		{"default page", func() (*Statement, error) { return Select(orders, Query{}, 500) }},
		{"sort and paging", func() (*Statement, error) {
			return Select(orders, Query{Limit: 50, Offset: 100, Sort: []Sort{{"amount", "desc"}, {"id", "asc"}}}, 500)
		}},
		{"all filter ops", func() (*Statement, error) {
			return Select(orders, Query{Filters: []Filter{
				{"status", "eq", "paid"}, {"status", "neq", "new"}, {"amount", "lt", "1"}, {"amount", "lte", "2"},
				{"amount", "gt", "3"}, {"amount", "gte", "4"}, {`we"ird col`, "like", "a%"}, {`we"ird col`, "ilike", "%b"},
				{"amount", "is_null", nil}, {"status", "not_null", nil}, {"id", "in", []any{"1", "2", `3"\`}},
			}}, 500)
		}},
		{"injection in value is bound", func() (*Statement, error) {
			return Select(orders, Query{Filters: []Filter{{"status", "eq", "x'; DROP TABLE shop.orders; --"}}}, 500)
		}},
		{"quoted identifiers", func() (*Statement, error) {
			return Select(weird, Query{Sort: []Sort{{"select", "desc"}}}, 500)
		}},
		{"count", func() (*Statement, error) { return Count(orders, []Filter{{"status", "eq", "paid"}}) }},
		{"cell", func() (*Statement, error) { return Cell(weird, "select", map[string]*string{"a": s("1"), "b": s("x")}) }},
		{"insert", func() (*Statement, error) {
			return Insert(orders, map[string]*string{"status": s("new"), "amount": nil, `we"ird col`: s("x'); DROP TABLE t; --")})
		}},
		{"insert defaults", func() (*Statement, error) { return Insert(orders, nil) }},
		{"update optimistic", func() (*Statement, error) {
			return Update(weird, map[string]*string{"a": s("1"), "b": s("k")}, map[string]*string{"select": s("new")}, map[string]*string{"select": nil})
		}},
		{"delete", func() (*Statement, error) { return Delete(orders, map[string]*string{"id": s("42")}) }},
	}
	var out strings.Builder
	for _, tc := range cases {
		st, err := tc.build()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		params := make([]string, len(st.Params))
		for i, p := range st.Params {
			if p == nil {
				params[i] = fmt.Sprintf("NULL::%d", st.OIDs[i])
				continue
			}
			params[i] = fmt.Sprintf("%q::%d", *p, st.OIDs[i])
		}
		fmt.Fprintf(&out, "-- %s\n%s\n-- params: %s\n\n", tc.name, st.SQL, strings.Join(params, ", "))
	}
	const golden = "testdata/golden.sql"
	if *update {
		if err := os.WriteFile(golden, []byte(out.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != out.String() {
		t.Errorf("golden mismatch:\n%s", out.String())
	}
}

func TestRejected(t *testing.T) {
	cases := []struct {
		name string
		q    Query
	}{
		{"unknown filter column", Query{Filters: []Filter{{`status" OR 1=1 --`, "eq", "x"}}}},
		{"unknown sort column", Query{Sort: []Sort{{"id; DROP TABLE x", "asc"}}}},
		{"bad direction", Query{Sort: []Sort{{"id", "asc; DROP"}}}},
		{"bad op", Query{Filters: []Filter{{"id", "= 1 OR", "x"}}}},
		{"non-string value", Query{Filters: []Filter{{"id", "eq", 1.0}}}},
		{"empty in", Query{Filters: []Filter{{"id", "in", []any{}}}}},
		{"in with numbers", Query{Filters: []Filter{{"id", "in", []any{1.0}}}}},
		{"limit too big", Query{Limit: 501}},
		{"negative offset", Query{Offset: -1}},
	}
	for _, tc := range cases {
		_, err := Select(orders, tc.q, 500)
		var ie *InvalidError
		if !errors.As(err, &ie) {
			t.Errorf("%s: want InvalidError, got %v", tc.name, err)
		}
	}
	if _, err := Update(orders, map[string]*string{"id": s("1")}, map[string]*string{"status": s("x")}, nil); err == nil {
		t.Error("update without original accepted")
	}
	if _, err := Update(orders, map[string]*string{"id": s("1")}, map[string]*string{"nope": s("x")}, map[string]*string{"nope": nil}); err == nil {
		t.Error("update of unknown column accepted")
	}
	if _, err := Insert(orders, map[string]*string{"x; drop": s("1")}); err == nil {
		t.Error("insert of unknown column accepted")
	}
	if _, err := Delete(Table{Schema: "a", Name: "b", Columns: orders.Columns}, map[string]*string{"id": s("1")}); err == nil {
		t.Error("delete without pk accepted")
	}
	if _, err := Cell(orders, "status", map[string]*string{"nope": s("1")}); err == nil {
		t.Error("cell with wrong pk accepted")
	}
	if _, err := Cell(Table{Schema: "a", Name: "b", Columns: orders.Columns}, "id", map[string]*string{"id": s("1")}); err == nil {
		t.Error("cell without pk accepted")
	}
}

func TestDecodeFilterJSON(t *testing.T) {
	var q Query
	if err := json.Unmarshal([]byte(`{"filters":[{"column":"id","op":"in","value":["1","2"]},{"column":"status","op":"is_null"}]}`), &q); err != nil {
		t.Fatal(err)
	}
	if _, err := Select(orders, q, 500); err != nil {
		t.Fatal(err)
	}
}

func TestArrayLiteral(t *testing.T) {
	if got := ArrayLiteral([]string{`a`, `b"c`, `d\e`, `,{}`}); got != `{"a","b\"c","d\\e",",{}"}` {
		t.Fatal(got)
	}
}

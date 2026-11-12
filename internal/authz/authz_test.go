package authz

import (
	"testing"

	"github.com/Warewave-Technology/athens/internal/config"
)

func TestEffective(t *testing.T) {
	cfg := config.Authz{
		AdminGroups: []string{"admins"},
		Rules: []config.Rule{
			{Name: "dba", Groups: []string{"dba"}, Namespaces: []string{"*"}, Level: "write"},
			{Name: "dev-read", Groups: []string{"dev"}, Namespaces: []string{"payments", "payments-staging"}, Level: "read"},
			{Name: "dev-write-staging", Groups: []string{"dev"}, Namespaces: []string{"payments-staging"}, Level: "write"},
			{Name: "analytics", Groups: []string{"bi"}, Namespaces: []string{"orders"}, Targets: []string{"orders-*"}, Databases: []string{"reporting", "app_ro*"}, Level: "read"},
			{Name: "overlap-lower", Groups: []string{"dba"}, Namespaces: []string{"payments"}, Level: "read"},
		},
	}
	a := New(cfg)
	cases := []struct {
		name           string
		groups         []string
		ns, target, db string
		want           Level
	}{
		{"no groups", nil, "payments", "db", "app", None},
		{"dba anywhere", []string{"dba"}, "x", "y", "z", Write},
		{"overlap takes max", []string{"dba"}, "payments", "db", "app", Write},
		{"dev read prod", []string{"dev"}, "payments", "db", "app", Read},
		{"dev write staging", []string{"dev"}, "payments-staging", "db", "app", Write},
		{"dev other ns", []string{"dev"}, "orders", "orders-db", "app", None},
		{"bi db glob", []string{"bi"}, "orders", "orders-db", "app_ro_1", Read},
		{"bi db mismatch", []string{"bi"}, "orders", "orders-db", "app", None},
		{"bi target mismatch", []string{"bi"}, "orders", "billing", "reporting", None},
		{"multi group", []string{"bi", "dev"}, "payments-staging", "x", "y", Write},
		{"admin no data", []string{"admins"}, "payments", "db", "app", None},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.Effective(tc.groups, tc.ns, tc.target, tc.db); got != tc.want {
				t.Fatalf("Effective = %v, want %v", got, tc.want)
			}
		})
	}
	if a.TargetLevel([]string{"bi"}, "orders", "orders-db") != Read {
		t.Fatal("bi must see orders-db")
	}
	if !a.IsAdmin([]string{"x", "admins"}) || a.IsAdmin([]string{"dba"}) {
		t.Fatal("IsAdmin")
	}

	cfg.ReadOnly = true
	ro := New(cfg)
	if ro.Effective([]string{"dba"}, "x", "y", "z") != Read || !ro.ReadOnlyGlobal() {
		t.Fatal("global readOnly must cap at read")
	}
}

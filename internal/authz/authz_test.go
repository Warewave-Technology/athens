package authz

import (
	"testing"

	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/discovery"
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
			tg := discovery.Target{Namespace: tc.ns, Name: tc.target}
			if got := a.Effective(tc.groups, tg, tc.db); got != tc.want {
				t.Fatalf("Effective = %v, want %v", got, tc.want)
			}
		})
	}
	if a.TargetLevel([]string{"bi"}, discovery.Target{Namespace: "orders", Name: "orders-db"}) != Read {
		t.Fatal("bi must see orders-db")
	}
	if !a.IsAdmin([]string{"x", "admins"}) || a.IsAdmin([]string{"dba"}) {
		t.Fatal("IsAdmin")
	}

	cfg.ReadOnly = true
	ro := New(cfg)
	if ro.Effective([]string{"dba"}, discovery.Target{Namespace: "x", Name: "y"}, "z") != Read || !ro.ReadOnlyGlobal() {
		t.Fatal("global readOnly must cap at read")
	}
}

func TestTargetAnnotations(t *testing.T) {
	cfg := config.Authz{
		AnnotationAccess: true,
		GroupAliases:     map[string]string{"system-admins": "11111111-aaaa"},
		Rules:            []config.Rule{{Groups: []string{"dba"}, Namespaces: []string{"*"}, Level: "write"}},
	}
	a := New(cfg)
	grants := &discovery.AccessPolicy{Grants: []discovery.AccessGrant{
		{Group: "system-admins", Level: "write"}, {Group: "developers", Level: "read"},
	}}
	plain := discovery.Target{Namespace: "ns", Name: "db"}
	ro := discovery.Target{Namespace: "ns", Name: "db", ReadOnly: true}
	annotated := discovery.Target{Namespace: "ns", Name: "db", Access: grants}
	both := discovery.Target{Namespace: "ns", Name: "db", Access: grants, ReadOnly: true}
	empty := discovery.Target{Namespace: "ns", Name: "db", Access: &discovery.AccessPolicy{}}
	cases := []struct {
		name   string
		groups []string
		t      discovery.Target
		want   Level
	}{
		{"rules apply without annotation", []string{"dba"}, plain, Write},
		{"readonly caps rules", []string{"dba"}, ro, Read},
		{"access replaces rules", []string{"dba"}, annotated, None},
		{"alias resolves to group id", []string{"11111111-aaaa"}, annotated, Write},
		{"group named directly", []string{"developers"}, annotated, Read},
		{"max over grants", []string{"developers", "11111111-aaaa"}, annotated, Write},
		{"unlisted group", []string{"others"}, annotated, None},
		{"readonly caps grants", []string{"11111111-aaaa"}, both, Read},
		{"empty policy grants nobody", []string{"dba"}, empty, None},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.Effective(tc.groups, tc.t, "app"); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}

	cfg.AnnotationAccess = false
	off := New(cfg)
	if off.Effective([]string{"dba"}, annotated, "app") != Write || off.Effective([]string{"dba"}, both, "app") != Read {
		t.Fatal("annotationAccess=false must fall back to the rules but keep readonly")
	}
}

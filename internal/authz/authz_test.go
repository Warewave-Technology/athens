package authz

import (
	"testing"

	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/discovery"
)

func TestLevel(t *testing.T) {
	cfg := config.Authz{
		AdminGroups:  []string{"admins"},
		GroupAliases: map[string]string{"system-admins": "11111111-aaaa"},
	}
	a := New(cfg)
	grants := &discovery.AccessPolicy{Grants: []discovery.AccessGrant{
		{Group: "system-admins", Level: "write"}, {Group: "developers", Level: "read"},
	}}
	none := discovery.Target{Namespace: "ns", Name: "db"}
	annotated := discovery.Target{Namespace: "ns", Name: "db", Access: grants}
	ro := discovery.Target{Namespace: "ns", Name: "db", Access: grants, ReadOnly: true}
	empty := discovery.Target{Namespace: "ns", Name: "db", Access: &discovery.AccessPolicy{}}
	cases := []struct {
		name   string
		groups []string
		t      discovery.Target
		want   Level
	}{
		{"no access annotation: nobody", []string{"system-admins", "11111111-aaaa", "admins"}, none, None},
		{"alias resolves to group id", []string{"11111111-aaaa"}, annotated, Write},
		{"alias name as claim", []string{"system-admins"}, annotated, Write},
		{"group named directly", []string{"developers"}, annotated, Read},
		{"max over grants", []string{"developers", "11111111-aaaa"}, annotated, Write},
		{"unlisted group", []string{"others"}, annotated, None},
		{"no groups", nil, annotated, None},
		{"readonly caps grants", []string{"11111111-aaaa"}, ro, Read},
		{"empty policy grants nobody", []string{"developers"}, empty, None},
		{"admin has no data access", []string{"admins"}, annotated, None},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.Level(tc.groups, tc.t); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	if !a.IsAdmin([]string{"x", "admins"}) || a.IsAdmin([]string{"developers"}) {
		t.Fatal("IsAdmin")
	}

	cfg.ReadOnly = true
	glob := New(cfg)
	if glob.Level([]string{"11111111-aaaa"}, annotated) != Read || !glob.ReadOnlyGlobal() {
		t.Fatal("global readOnly must cap at read")
	}
}

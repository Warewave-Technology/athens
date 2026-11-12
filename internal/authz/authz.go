// Package authz evaluates config rules: Entra groups -> target/database -> level.
package authz

import (
	"path"
	"slices"

	"github.com/Warewave-Technology/athens/internal/config"
)

type Level int

const (
	None Level = iota
	Read
	Write
)

func (l Level) String() string {
	switch l {
	case Read:
		return "read"
	case Write:
		return "write"
	default:
		return "none"
	}
}

func (l Level) MarshalText() ([]byte, error) { return []byte(l.String()), nil }

type rule struct {
	name                           string
	groups                         []string
	namespaces, targets, databases []string
	level                          Level
}

type Authorizer struct {
	rules    []rule
	admins   []string
	readOnly bool
}

func New(c config.Authz) *Authorizer {
	a := &Authorizer{admins: c.AdminGroups, readOnly: c.ReadOnly}
	for _, r := range c.Rules {
		lvl := Read
		if r.Level == "write" {
			lvl = Write
		}
		a.rules = append(a.rules, rule{
			name: r.Name, groups: r.Groups, level: lvl,
			namespaces: orAll(r.Namespaces), targets: orAll(r.Targets), databases: orAll(r.Databases),
		})
	}
	return a
}

func orAll(globs []string) []string {
	if len(globs) == 0 {
		return []string{"*"}
	}
	return globs
}

// ReadOnlyGlobal reports the authz.readOnly kill switch.
func (a *Authorizer) ReadOnlyGlobal() bool { return a.readOnly }

// IsAdmin reports membership in authz.adminGroups. Admins get the admin pages,
// not implicit data access.
func (a *Authorizer) IsAdmin(groups []string) bool {
	return slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(a.admins, g) })
}

// TargetLevel is the highest level any rule grants on the target for any
// database. None means the target is invisible to the user.
func (a *Authorizer) TargetLevel(groups []string, namespace, name string) Level {
	return a.eval(groups, namespace, name, nil)
}

// Effective is the level for one database of a target (max of matching rules,
// capped at Read when the global read-only switch is on).
func (a *Authorizer) Effective(groups []string, namespace, name, database string) Level {
	return a.eval(groups, namespace, name, &database)
}

func (a *Authorizer) eval(groups []string, namespace, name string, database *string) Level {
	best := None
	for _, r := range a.rules {
		if r.level <= best || !slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(r.groups, g) }) {
			continue
		}
		if !matchAny(r.namespaces, namespace) || !matchAny(r.targets, name) {
			continue
		}
		if database != nil && !matchAny(r.databases, *database) {
			continue
		}
		best = r.level
	}
	if a.readOnly && best > Read {
		best = Read
	}
	return best
}

func matchAny(globs []string, s string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, s); ok {
			return true
		}
	}
	return false
}

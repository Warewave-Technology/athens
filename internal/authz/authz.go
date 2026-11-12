// Package authz evaluates config rules: Entra groups -> target/database -> level.
package authz

import (
	"path"
	"slices"

	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/discovery"
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
	rules            []rule
	admins           []string
	readOnly         bool
	annotationAccess bool
	aliases          map[string]string
}

func New(c config.Authz) *Authorizer {
	a := &Authorizer{admins: c.AdminGroups, readOnly: c.ReadOnly, annotationAccess: c.AnnotationAccess, aliases: c.GroupAliases}
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

// TargetLevel is the highest level the user has on the target for any
// database. None means the target is invisible to the user.
func (a *Authorizer) TargetLevel(groups []string, t discovery.Target) Level {
	return a.eval(groups, t, nil)
}

// Effective is the level for one database of a target: the target's access
// annotation when present (and allowed), else the max of matching rules;
// capped at Read by the target's readonly annotation and the global switch.
func (a *Authorizer) Effective(groups []string, t discovery.Target, database string) Level {
	return a.eval(groups, t, &database)
}

// UsesAnnotation reports whether the target's access annotation replaces the
// rules.
func (a *Authorizer) UsesAnnotation(t discovery.Target) bool {
	return t.Access != nil && a.annotationAccess
}

func (a *Authorizer) eval(groups []string, t discovery.Target, database *string) Level {
	var best Level
	if a.UsesAnnotation(t) {
		best = a.grants(groups, t.Access.Grants)
	} else {
		best = a.rulesLevel(groups, t.Namespace, t.Name, database)
	}
	if (a.readOnly || t.ReadOnly) && best > Read {
		best = Read
	}
	return best
}

// grants is the highest level of the grants naming one of the user's groups,
// by ID or by alias.
func (a *Authorizer) grants(groups []string, gs []discovery.AccessGrant) Level {
	best := None
	for _, g := range gs {
		id := g.Group
		if alias, ok := a.aliases[g.Group]; ok {
			id = alias
		}
		if !slices.Contains(groups, id) && !slices.Contains(groups, g.Group) {
			continue
		}
		lvl := Read
		if g.Level == "write" {
			lvl = Write
		}
		best = max(best, lvl)
	}
	return best
}

func (a *Authorizer) rulesLevel(groups []string, namespace, name string, database *string) Level {
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

// Package authz decides a user's level on a target from the target's
// access and readonly annotations (ADR-022, ADR-023).
package authz

import (
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

type Authorizer struct {
	admins   []string
	readOnly bool
	aliases  map[string]string
}

func New(c config.Authz) *Authorizer {
	return &Authorizer{admins: c.AdminGroups, readOnly: c.ReadOnly, aliases: c.GroupAliases}
}

// ReadOnlyGlobal reports the authz.readOnly kill switch.
func (a *Authorizer) ReadOnlyGlobal() bool { return a.readOnly }

// IsAdmin reports membership in authz.adminGroups. Admins get the admin pages,
// not implicit data access.
func (a *Authorizer) IsAdmin(groups []string) bool {
	return slices.ContainsFunc(groups, func(g string) bool { return slices.Contains(a.admins, g) })
}

// Level is the user's level on every database of t: the highest grant of the
// target's access annotation naming one of the user's groups, capped at Read
// by the readonly annotation and the global switch. A target without an
// access annotation is usable by nobody. None means the target is invisible.
func (a *Authorizer) Level(groups []string, t discovery.Target) Level {
	if t.Access == nil {
		return None
	}
	best := None
	for _, g := range t.Access.Grants {
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
	if (a.readOnly || t.ReadOnly) && best > Read {
		best = Read
	}
	return best
}

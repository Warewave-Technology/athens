// Package reqctx carries per-request values (authenticated user, client IP)
// through context.
package reqctx

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// User is the authenticated principal of a request.
type User struct {
	ID        string
	Email     string
	Name      string
	Groups    []string
	SessionID string // sha256 of the session cookie
	CSRFToken string
}

type ctxKey int

const (
	userKey ctxKey = iota
	metaKey
)

// Meta holds request metadata used for audit.
type Meta struct {
	ClientIP  string
	UserAgent string
}

func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// UserFrom returns the authenticated user or nil.
func UserFrom(ctx context.Context) *User {
	u, _ := ctx.Value(userKey).(*User)
	return u
}

func WithMeta(ctx context.Context, m Meta) context.Context { return context.WithValue(ctx, metaKey, m) }

func MetaFrom(ctx context.Context) Meta {
	m, _ := ctx.Value(metaKey).(Meta)
	return m
}

// ClientIP returns the client address. X-Forwarded-For is honoured only when
// the direct peer is inside a trusted proxy CIDR; then the right-most address
// that is not itself a trusted proxy is used.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !inAny(peer, trusted) {
		return host
	}
	xff := r.Header.Values("X-Forwarded-For")
	parts := strings.Split(strings.Join(xff, ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			break
		}
		if !inAny(a, trusted) {
			return a.String()
		}
	}
	return host
}

func inAny(a netip.Addr, prefixes []netip.Prefix) bool {
	a = a.Unmap()
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ParsePrefixes parses CIDRs (already validated by config).
func ParsePrefixes(cidrs []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		if p, err := netip.ParsePrefix(c); err == nil {
			out = append(out, p.Masked())
		}
	}
	return out
}

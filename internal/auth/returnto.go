package auth

import (
	"net/url"
	"strings"
)

// SafeReturnTo returns p if it is a same-origin relative path, else "/".
// It rejects absolute URLs, scheme-relative ("//host") and backslash tricks
// ("/\host") that browsers normalize into another origin.
func SafeReturnTo(p string) string {
	if p == "" || len(p) > 2048 || !strings.HasPrefix(p, "/") {
		return "/"
	}
	if strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") || strings.ContainsAny(p, "\\\r\n\t\x00") {
		return "/"
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	if strings.HasPrefix(p, "/auth/") || p == "/login" || strings.HasPrefix(p, "/login?") {
		return "/" // never bounce back into the sign-in flow
	}
	return p
}

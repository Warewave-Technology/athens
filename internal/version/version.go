// Package version centralizes the product name and build information.
// Version, Commit and BuildDate are set via -ldflags at build time.
package version

import "runtime"

const (
	// Name is the product name used for logs, metrics prefix, cookies and
	// application_name. Change it here only.
	Name = "athens"
	// EnvPrefix prefixes config overrides from the environment.
	EnvPrefix = "ATHENS_"
	// MetricsNamespace is the Prometheus namespace for all collectors.
	MetricsNamespace = "athens"
	// CSRFHeader carries the session CSRF token on mutating API requests.
	CSRFHeader = "X-Athens-CSRF"
	// SessionCookie, CSRFCookie and AuthCookie are the cookie names.
	SessionCookie = "athens_session"
	CSRFCookie    = "athens_csrf"
	AuthCookie    = "athens_auth"
	// DefaultAnnotationPrefix is the default discovery annotation prefix.
	DefaultAnnotationPrefix = "athens.warewave.io"
)

var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

// Info is the build information exposed in logs and /api/v1/version.
type Info struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
	GoVersion string `json:"goVersion"`
}

// Get returns the build information.
func Get() Info {
	return Info{Name: Name, Version: Version, Commit: Commit, BuildDate: BuildDate, GoVersion: runtime.Version()}
}

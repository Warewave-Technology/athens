// Package config loads, overrides (env) and validates the athens configuration.
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Warewave-Technology/athens/internal/version"
)

// Duration is a time.Duration that unmarshals from strings like "30s".
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q", n.Line, n.Value)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

type Config struct {
	Server    Server    `yaml:"server"`
	OIDC      OIDC      `yaml:"oidc"`
	Auth      Auth      `yaml:"auth"`
	Authz     Authz     `yaml:"authz"`
	Discovery Discovery `yaml:"discovery"`
	Limits    Limits    `yaml:"limits"`
	Audit     Audit     `yaml:"audit"`
	Store     Store     `yaml:"store"`
	Log       Log       `yaml:"log"`
}

type Server struct {
	Listen         string   `yaml:"listen"`
	MetricsListen  string   `yaml:"metricsListen"`
	BaseURL        string   `yaml:"baseURL"`
	TrustedProxies []string `yaml:"trustedProxies"`
	InsecureDev    bool     `yaml:"insecureDev"`
	// DevHostOverrides maps "host:port" of discovered targets to another
	// address (e.g. a kubectl port-forward). Only allowed with insecureDev.
	DevHostOverrides map[string]string `yaml:"devHostOverrides"`
}

type OIDC struct {
	Issuer           string           `yaml:"issuer"`
	ClientID         string           `yaml:"clientID"`
	ClientSecret     string           `yaml:"clientSecret"`
	RedirectPath     string           `yaml:"redirectPath"`
	Scopes           []string         `yaml:"scopes"`
	AllowedGroups    []string         `yaml:"allowedGroups"`
	WorkloadIdentity WorkloadIdentity `yaml:"workloadIdentity"`
	// SkipIssuerTenantCheck allows non-Entra issuers (tests and local mock
	// IdPs). Only honoured together with server.insecureDev.
	SkipIssuerTenantCheck bool `yaml:"skipIssuerTenantCheck"`
}

type WorkloadIdentity struct {
	Enabled   bool   `yaml:"enabled"`
	TokenFile string `yaml:"tokenFile"`
}

type Auth struct {
	SessionKey string   `yaml:"sessionKey"`
	SessionTTL Duration `yaml:"sessionTTL"`
	IdleTTL    Duration `yaml:"idleTTL"`
}

type Authz struct {
	ReadOnly    bool     `yaml:"readOnly"`
	AdminGroups []string `yaml:"adminGroups"`
	Rules       []Rule   `yaml:"rules"`
	// AnnotationAccess lets the <prefix>/access annotation replace the rules
	// for its target (ADR-022). The readonly annotation always applies.
	AnnotationAccess bool `yaml:"annotationAccess"`
	// GroupAliases maps names usable in access annotations to Entra group
	// IDs, e.g. {"system-admins": "3f2a..."}.
	GroupAliases map[string]string `yaml:"groupAliases"`
}

type Rule struct {
	Name       string   `yaml:"name"`
	Groups     []string `yaml:"groups"`
	Namespaces []string `yaml:"namespaces"`
	Targets    []string `yaml:"targets"`
	Databases  []string `yaml:"databases"`
	Level      string   `yaml:"level"`
}

type Discovery struct {
	Namespaces       []string `yaml:"namespaces"`
	Resync           Duration `yaml:"resync"`
	UseFQDN          bool     `yaml:"useFQDN"`
	ClusterDomain    string   `yaml:"clusterDomain"`
	DefaultSSLMode   string   `yaml:"defaultSSLMode"`
	AnnotationPrefix string   `yaml:"annotationPrefix"`
	// RequireAnnotation limits discovery to objects carrying
	// <annotationPrefix>/enabled: "true" (ADR-020). When false, unannotated
	// postgres workloads and operator clusters are found automatically.
	RequireAnnotation bool    `yaml:"requireAnnotation"`
	CNPG              CNPG    `yaml:"cnpg"`
	Zalando           Zalando `yaml:"zalando"`
	Generic           Generic `yaml:"generic"`
}

type CNPG struct {
	Enabled      bool `yaml:"enabled"`
	UseSuperuser bool `yaml:"useSuperuser"`
	VerifyTLS    bool `yaml:"verifyTLS"`
}

type Zalando struct {
	Enabled        bool   `yaml:"enabled"`
	UseSuperuser   bool   `yaml:"useSuperuser"`
	SecretTemplate string `yaml:"secretTemplate"`
}

type Generic struct {
	Enabled      bool   `yaml:"enabled"`
	ImagePattern string `yaml:"imagePattern"`
}

type Limits struct {
	StatementTimeout         Duration `yaml:"statementTimeout"`
	LockTimeout              Duration `yaml:"lockTimeout"`
	MaxRows                  int      `yaml:"maxRows"`
	GridMaxPageSize          int      `yaml:"gridMaxPageSize"`
	MaxCellBytes             int      `yaml:"maxCellBytes"`
	PoolMaxConns             int      `yaml:"poolMaxConns"`
	ConcurrentQueriesPerUser int      `yaml:"concurrentQueriesPerUser"`
	HistoryPerUser           int      `yaml:"historyPerUser"`
}

type Audit struct {
	LogSQL    bool `yaml:"logSQL"`
	LogValues bool `yaml:"logValues"`
	// LogReads records grid page reads, cell reads and structure views.
	LogReads      bool `yaml:"logReads"`
	RetentionDays int  `yaml:"retentionDays"`
}

type Store struct {
	Path string `yaml:"path"`
}

type Log struct {
	Level string `yaml:"level"`
}

// DefaultImagePattern matches the repository part of well-known Postgres images.
const DefaultImagePattern = `(^|/)(postgres|postgresql|postgis/postgis|pgvector/pgvector|timescale/timescaledb[^/]*|bitnami/postgresql[^/]*)$`

// Default returns a config with every default from config.example.yaml.
func Default() Config {
	return Config{
		Server: Server{Listen: ":8080", MetricsListen: ":9090"},
		OIDC: OIDC{
			RedirectPath: "/auth/callback",
			Scopes:       []string{"openid", "profile", "email"},
			WorkloadIdentity: WorkloadIdentity{
				TokenFile: "/var/run/secrets/azure/tokens/azure-identity-token", // #nosec G101 -- file path
			},
		},
		Auth:  Auth{SessionTTL: Duration(8 * time.Hour), IdleTTL: Duration(time.Hour)},
		Authz: Authz{AnnotationAccess: true},
		Discovery: Discovery{
			Resync:            Duration(10 * time.Minute),
			ClusterDomain:     "cluster.local",
			DefaultSSLMode:    "prefer",
			AnnotationPrefix:  version.DefaultAnnotationPrefix,
			RequireAnnotation: true,
			CNPG:              CNPG{Enabled: true},
			Zalando: Zalando{
				Enabled:        true,
				SecretTemplate: "{username}.{cluster}.credentials.postgresql.acid.zalan.do", // #nosec G101 -- secret name template, not a credential
			},
			Generic: Generic{Enabled: true, ImagePattern: DefaultImagePattern},
		},
		Limits: Limits{
			StatementTimeout:         Duration(30 * time.Second),
			LockTimeout:              Duration(5 * time.Second),
			MaxRows:                  1000,
			GridMaxPageSize:          500,
			MaxCellBytes:             64 << 10,
			PoolMaxConns:             4,
			ConcurrentQueriesPerUser: 3,
			HistoryPerUser:           500,
		},
		Audit: Audit{LogSQL: true, LogReads: true, RetentionDays: 90},
		Store: Store{Path: "/data/athens.db"},
		Log:   Log{Level: "info"},
	}
}

// Load reads the YAML file (if path is non-empty), applies env overrides and
// validates. All validation problems are returned together.
func Load(file string, environ []string) (*Config, error) {
	cfg := Default()
	if file != "" {
		b, err := os.ReadFile(file) // #nosec G304 -- path comes from the operator's --config flag
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(b)))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parse config %s: %w", file, err)
		}
	}
	if err := applyEnv(&cfg, environ); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyEnv walks the struct and overrides every scalar (and string slice,
// comma separated) from ATHENS_<PATH> where PATH is the upper-cased yaml path
// joined by "_" (e.g. ATHENS_OIDC_CLIENTSECRET).
func applyEnv(cfg *Config, environ []string) error {
	env := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, version.EnvPrefix) {
			env[k] = v
		}
	}
	var errs []error
	walkEnv(reflect.ValueOf(cfg).Elem(), strings.TrimSuffix(version.EnvPrefix, "_"), env, &errs)
	return errors.Join(errs...)
}

var durationType = reflect.TypeFor[Duration]()

func walkEnv(v reflect.Value, prefix string, env map[string]string, errs *[]error) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		name := prefix + "_" + strings.ToUpper(tag)
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct {
			walkEnv(fv, name, env, errs)
			continue
		}
		raw, ok := env[name]
		if !ok {
			continue
		}
		if err := setScalar(fv, raw); err != nil {
			*errs = append(*errs, fmt.Errorf("env %s: %w", name, err))
		}
	}
}

func setScalar(fv reflect.Value, raw string) error {
	if fv.Type() == durationType {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return err
		}
		fv.SetInt(int64(d))
		return nil
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return err
		}
		fv.SetInt(int64(n))
	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported type %s", fv.Type())
		}
		var parts []string
		for p := range strings.SplitSeq(raw, ",") {
			if p = strings.TrimSpace(p); p != "" {
				parts = append(parts, p)
			}
		}
		fv.Set(reflect.ValueOf(parts))
	default:
		return fmt.Errorf("unsupported type %s", fv.Type())
	}
	return nil
}

var (
	entraIssuer = regexp.MustCompile(`^https://login\.microsoftonline\.com/([0-9a-fA-F-]{36})/v2\.0$`)
	dnsLabel    = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	sslModes    = map[string]bool{"disable": true, "allow": true, "prefer": true, "require": true, "verify-ca": true, "verify-full": true}
	logLevels   = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
)

// Validate checks SPEC §4. It returns every problem at once.
func (c *Config) Validate() error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if c.Server.BaseURL == "" {
		add("server.baseURL is required")
	} else if u, err := url.Parse(c.Server.BaseURL); err != nil || u.Host == "" {
		add("server.baseURL must be an absolute URL")
	} else {
		if u.Scheme != "https" && (u.Scheme != "http" || !c.Server.InsecureDev) {
			add("server.baseURL must use https (http only with server.insecureDev=true)")
		}
		if u.Path != "" && u.Path != "/" {
			add("server.baseURL must not contain a path (sub-path deployments are not supported)")
		}
	}
	if c.Server.Listen == "" {
		add("server.listen is required")
	}
	if len(c.Server.DevHostOverrides) > 0 && !c.Server.InsecureDev {
		add("server.devHostOverrides is only allowed with server.insecureDev=true")
	}
	for _, cidr := range c.Server.TrustedProxies {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			add("server.trustedProxies: invalid CIDR %q", cidr)
		}
	}

	if c.OIDC.Issuer == "" {
		add("oidc.issuer is required")
	} else if !entraIssuer.MatchString(c.OIDC.Issuer) {
		if !c.OIDC.SkipIssuerTenantCheck || !c.Server.InsecureDev {
			add("oidc.issuer must be https://login.microsoftonline.com/<tenant-id>/v2.0")
		}
	}
	if c.OIDC.ClientID == "" {
		add("oidc.clientID is required")
	}
	if !strings.HasPrefix(c.OIDC.RedirectPath, "/") {
		add("oidc.redirectPath must start with /")
	}
	hasSecret := c.OIDC.ClientSecret != ""
	switch {
	case c.OIDC.WorkloadIdentity.Enabled:
		add("oidc.workloadIdentity is not implemented yet (milestone M8); use oidc.clientSecret")
	case !hasSecret:
		add("oidc.clientSecret is required (set %sOIDC_CLIENTSECRET)", version.EnvPrefix)
	}

	if c.Auth.SessionKey == "" {
		add("auth.sessionKey is required (32+ random bytes, base64; set %sAUTH_SESSIONKEY)", version.EnvPrefix)
	} else if _, err := c.SessionKeyBytes(); err != nil {
		add("auth.sessionKey: %v", err)
	}
	if c.Auth.SessionTTL <= 0 || c.Auth.IdleTTL <= 0 {
		add("auth.sessionTTL and auth.idleTTL must be positive")
	}

	for i, r := range c.Authz.Rules {
		where := fmt.Sprintf("authz.rules[%d]", i)
		if r.Name != "" {
			where += " (" + r.Name + ")"
		}
		if len(r.Groups) == 0 {
			add("%s: groups must not be empty", where)
		}
		if r.Level != "read" && r.Level != "write" {
			add("%s: level must be read or write, got %q", where, r.Level)
		}
		if len(r.Namespaces) == 0 && len(r.Targets) == 0 && len(r.Databases) == 0 {
			add("%s: at least one of namespaces, targets, databases is required", where)
		}
		for _, g := range append(append(append([]string{}, r.Namespaces...), r.Targets...), r.Databases...) {
			if _, err := path.Match(g, ""); err != nil {
				add("%s: invalid glob %q", where, g)
			}
		}
	}

	for name, id := range c.Authz.GroupAliases {
		if name == "" || id == "" || strings.ContainsAny(name, ":, \t") {
			add("authz.groupAliases: %q -> %q: names must be non-empty without ':', ',' or spaces, IDs non-empty", name, id)
		}
	}

	if len(c.Discovery.Namespaces) == 0 {
		add("discovery.namespaces must not be empty")
	}
	for _, ns := range c.Discovery.Namespaces {
		if ns == "*" || strings.ContainsAny(ns, "*?[") {
			add("discovery.namespaces: wildcards are not allowed (%q), list namespaces explicitly", ns)
		} else if !dnsLabel.MatchString(ns) {
			add("discovery.namespaces: %q is not a valid namespace name", ns)
		}
	}
	if !sslModes[c.Discovery.DefaultSSLMode] {
		add("discovery.defaultSSLMode: invalid value %q", c.Discovery.DefaultSSLMode)
	}
	if c.Discovery.AnnotationPrefix == "" {
		add("discovery.annotationPrefix is required")
	}
	if c.Discovery.Resync <= 0 {
		add("discovery.resync must be positive")
	}
	if c.Discovery.Generic.Enabled {
		if _, err := regexp.Compile(c.Discovery.Generic.ImagePattern); err != nil {
			add("discovery.generic.imagePattern: %v", err)
		}
	}
	if c.Discovery.Zalando.Enabled && !strings.Contains(c.Discovery.Zalando.SecretTemplate, "{username}") {
		add("discovery.zalando.secretTemplate must contain {username}")
	}

	l := c.Limits
	if l.StatementTimeout <= 0 || l.LockTimeout <= 0 || l.MaxRows <= 0 || l.GridMaxPageSize <= 0 ||
		l.MaxCellBytes <= 0 || l.PoolMaxConns <= 0 || l.ConcurrentQueriesPerUser <= 0 || l.HistoryPerUser <= 0 {
		add("limits: all values must be positive")
	}
	if l.MaxRows > 50000 {
		add("limits.maxRows must be <= 50000")
	}
	if l.GridMaxPageSize > 500 {
		add("limits.gridMaxPageSize must be <= 500")
	}
	if c.Audit.RetentionDays <= 0 {
		add("audit.retentionDays must be positive")
	}
	if c.Store.Path == "" {
		add("store.path is required")
	}
	if !logLevels[c.Log.Level] {
		add("log.level must be one of debug, info, warn, error")
	}

	if len(errs) == 0 {
		return nil
	}
	return &ValidationError{Problems: errs}
}

// ValidationError aggregates every configuration problem.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string {
	return "invalid configuration:\n  - " + strings.Join(e.Problems, "\n  - ")
}

// SessionKeyBytes decodes auth.sessionKey (std or URL base64) and requires 32+ bytes.
func (c *Config) SessionKeyBytes() ([]byte, error) {
	s := strings.TrimSpace(c.Auth.SessionKey)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) < 32 {
				return nil, errors.New("must decode to at least 32 bytes")
			}
			return b, nil
		}
	}
	return nil, errors.New("must be base64")
}

// TenantID returns the tenant from an Entra issuer URL ("" for other issuers).
func (c *Config) TenantID() string {
	if m := entraIssuer.FindStringSubmatch(c.OIDC.Issuer); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

// BaseURL returns the parsed server.baseURL (valid after Validate).
func (c *Config) BaseURL() *url.URL {
	u, _ := url.Parse(strings.TrimSuffix(c.Server.BaseURL, "/"))
	return u
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes

func validEnv() []string {
	return []string{
		"ATHENS_SERVER_BASEURL=https://athens.example.com",
		"ATHENS_OIDC_ISSUER=https://login.microsoftonline.com/11111111-2222-3333-4444-555555555555/v2.0",
		"ATHENS_OIDC_CLIENTID=client",
		"ATHENS_OIDC_CLIENTSECRET=secret",
		"ATHENS_AUTH_SESSIONKEY=" + validKey,
		"ATHENS_DISCOVERY_NAMESPACES=payments, orders",
	}
}

func TestExampleConfigLoads(t *testing.T) {
	env := []string{
		"ATHENS_OIDC_ISSUER=https://login.microsoftonline.com/11111111-2222-3333-4444-555555555555/v2.0",
		"ATHENS_OIDC_CLIENTSECRET=s",
		"ATHENS_AUTH_SESSIONKEY=" + validKey,
	}
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"), env)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Authz.Rules) != 3 || cfg.Limits.StatementTimeout.D() != 30*time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.TenantID() != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("tenant = %q", cfg.TenantID())
	}
}

func TestEnvOverride(t *testing.T) {
	env := append(validEnv(), "ATHENS_LIMITS_STATEMENTTIMEOUT=5s", "ATHENS_AUTHZ_READONLY=true", "ATHENS_LIMITS_MAXROWS=10")
	cfg, err := Load("", env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Limits.StatementTimeout.D() != 5*time.Second || !cfg.Authz.ReadOnly || cfg.Limits.MaxRows != 10 {
		t.Fatalf("overrides not applied: %+v", cfg.Limits)
	}
	if got := strings.Join(cfg.Discovery.Namespaces, "|"); got != "payments|orders" {
		t.Fatalf("namespaces = %q", got)
	}
}

func TestEnvOverrideBadValue(t *testing.T) {
	_, err := Load("", append(validEnv(), "ATHENS_LIMITS_MAXROWS=abc"))
	if err == nil || !strings.Contains(err.Error(), "ATHENS_LIMITS_MAXROWS") {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownFieldRejected(t *testing.T) {
	f := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(f, []byte("server:\n  lisen: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(f, validEnv()); err == nil || !strings.Contains(err.Error(), "lisen") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidation(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"http baseURL", []string{"ATHENS_SERVER_BASEURL=http://x"}, "must use https"},
		{"http baseURL insecureDev ok", []string{"ATHENS_SERVER_BASEURL=http://x", "ATHENS_SERVER_INSECUREDEV=true"}, ""},
		{"baseURL path", []string{"ATHENS_SERVER_BASEURL=https://x/sub"}, "must not contain a path"},
		{"issuer not entra", []string{"ATHENS_OIDC_ISSUER=https://accounts.google.com"}, "oidc.issuer must be"},
		{"issuer mock needs insecureDev", []string{"ATHENS_OIDC_ISSUER=http://127.0.0.1/oidc", "ATHENS_OIDC_SKIPISSUERTENANTCHECK=true"}, "oidc.issuer must be"},
		{"issuer mock ok", []string{"ATHENS_OIDC_ISSUER=http://127.0.0.1/oidc", "ATHENS_OIDC_SKIPISSUERTENANTCHECK=true", "ATHENS_SERVER_INSECUREDEV=true"}, ""},
		{"no secret", []string{"ATHENS_OIDC_CLIENTSECRET="}, "oidc.clientSecret is required"},
		{"workload identity", []string{"ATHENS_OIDC_WORKLOADIDENTITY_ENABLED=true"}, "not implemented"},
		{"short session key", []string{"ATHENS_AUTH_SESSIONKEY=YWJj"}, "at least 32 bytes"},
		{"wildcard namespace", []string{"ATHENS_DISCOVERY_NAMESPACES=*"}, "wildcards are not allowed"},
		{"empty namespaces", []string{"ATHENS_DISCOVERY_NAMESPACES="}, "must not be empty"},
		{"maxRows", []string{"ATHENS_LIMITS_MAXROWS=50001"}, "maxRows must be <= 50000"},
		{"negative", []string{"ATHENS_LIMITS_POOLMAXCONNS=0"}, "must be positive"},
		{"sslmode", []string{"ATHENS_DISCOVERY_DEFAULTSSLMODE=maybe"}, "defaultSSLMode"},
		{"bad cidr", []string{"ATHENS_SERVER_TRUSTEDPROXIES=10.0.0.0/33"}, "invalid CIDR"},
		{"bad regex", []string{"ATHENS_DISCOVERY_GENERIC_IMAGEPATTERN=("}, "imagePattern"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load("", append(validEnv(), tc.env...))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRuleValidationAggregates(t *testing.T) {
	f := filepath.Join(t.TempDir(), "c.yaml")
	y := `authz:
  rules:
    - name: a
      level: admin
    - name: b
      groups: [g]
      level: read
`
	if err := os.WriteFile(f, []byte(y), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(f, validEnv())
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"rules[0] (a): groups must not be empty", "rules[0] (a): level must be read or write", "rules[1] (b): at least one of"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

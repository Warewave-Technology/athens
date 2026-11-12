package discovery

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/testutil/k8sfixture"
)

var update = flag.Bool("update", false, "rewrite golden files")

// testOptions uses automatic discovery so the golden files cover every
// source; annotation-only mode (the default) has its own test.
func testOptions(t *testing.T) Options {
	t.Helper()
	opt, err := OptionsFromConfig(config.Default().Discovery)
	if err != nil {
		t.Fatal(err)
	}
	opt.RequireAnnotation = false
	return opt
}

func load(t *testing.T, f string) Objects {
	t.Helper()
	set, err := k8sfixture.Load(f, "demo")
	if err != nil {
		t.Fatal(err)
	}
	return Objects{Namespace: "demo", StatefulSets: set.StatefulSets, Deployments: set.Deployments,
		Services: set.Services, CNPG: set.CNPG, Zalando: set.Zalando}
}

// TestAnnotationOnly runs every fixture with the default configuration:
// only objects annotated enabled=true may become targets.
func TestAnnotationOnly(t *testing.T) {
	opt, err := OptionsFromConfig(config.Default().Discovery)
	if err != nil {
		t.Fatal(err)
	}
	if !opt.RequireAnnotation {
		t.Fatal("annotation-only must be the default")
	}
	files, _ := filepath.Glob("testdata/*.yaml")
	var got []string
	for _, f := range files {
		targets, _ := Build(load(t, f), opt)
		for _, tg := range targets {
			got = append(got, tg.ID+"="+string(tg.Source))
		}
	}
	want := []string{
		"demo/analytics=annotation", // annotated Service
		"demo/legacy=annotation",    // annotated StatefulSet
		"demo/billing=cnpg",         // annotated CNPG Cluster
		"demo/acid-ledger=zalando",  // annotated Zalando postgresql
		"demo/crm=annotation",       // readonly + access
		"demo/locked=annotation",    // empty access
	}
	if strings.Join(sorted(got), ",") != strings.Join(sorted(want), ",") {
		t.Fatalf("annotation-only targets = %v, want %v", got, want)
	}
}

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

type golden struct {
	Targets  []Target  `json:"targets"`
	Problems []Problem `json:"problems"`
}

func TestFixtures(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.yaml")
	if len(files) < 13 {
		t.Fatalf("expected at least 13 fixtures, got %d", len(files))
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".yaml")
		t.Run(name, func(t *testing.T) {
			set, err := k8sfixture.Load(f, "demo")
			if err != nil {
				t.Fatal(err)
			}
			targets, problems := Build(Objects{
				Namespace: "demo", StatefulSets: set.StatefulSets, Deployments: set.Deployments,
				Services: set.Services, CNPG: set.CNPG, Zalando: set.Zalando,
			}, testOptions(t))
			got, err := json.MarshalIndent(golden{Targets: targets, Problems: problems}, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')

			// Golden output must never contain a secret value or literal password.
			for _, s := range set.Secrets {
				for k, v := range s.Data {
					if isPasswordKey(k) && strings.Contains(string(got), string(v)) {
						t.Errorf("secret value of %s/%s leaked into discovery output", s.Name, k)
					}
				}
			}
			for _, cm := range set.ConfigMaps {
				for k, v := range cm.Data {
					if strings.Contains(strings.ToLower(k), "password") && strings.Contains(string(got), v) {
						t.Errorf("configmap password %s leaked", k)
					}
				}
			}
			if strings.Contains(string(got), "literal-value") || strings.Contains(string(got), "literal-pw") || strings.Contains(string(got), "never-used") {
				t.Error("literal env password leaked into discovery output")
			}

			gf := filepath.Join("testdata", name+".golden.json")
			if *update {
				if err := os.WriteFile(gf, got, 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(gf)
			if err != nil {
				t.Fatalf("%v (run go test -update)", err)
			}
			if string(want) != string(got) {
				t.Errorf("output differs from %s:\n%s", gf, got)
			}
		})
	}
}

// isPasswordKey selects the fixture secret keys that hold passwords (user and
// database names are not secret and legitimately appear as literals).
func isPasswordKey(k string) bool {
	k = strings.ToLower(k)
	return strings.Contains(k, "pass") || k == "pw"
}

func TestImageRepo(t *testing.T) {
	cases := map[string]string{
		"postgres":                             "postgres",
		"postgres:16":                          "postgres",
		"docker.io/library/postgres:15-alpine": "library/postgres",
		"docker.io/bitnami/postgresql:16.4.0-debian":  "bitnami/postgresql",
		"localhost:5000/postgres:16":                  "postgres",
		"ghcr.io/x/postgres@sha256:abc":               "x/postgres",
		"registry.local:5000/team/postgis/postgis:16": "team/postgis/postgis",
	}
	opt := testOptions(t)
	for in, want := range cases {
		got := imageRepo(in)
		if got != want {
			t.Errorf("imageRepo(%q) = %q, want %q", in, got, want)
		}
		if !opt.imageRE.MatchString(got) {
			t.Errorf("default pattern must match %q", got)
		}
	}
	for _, no := range []string{"quay.io/prometheuscommunity/postgres-exporter:v0.15.0", "nginx:1", "ghcr.io/cloudnative-pg/postgresql-operator"} {
		if opt.imageRE.MatchString(imageRepo(no)) {
			t.Errorf("pattern must not match %q", no)
		}
	}
}

func TestFQDN(t *testing.T) {
	set, err := k8sfixture.Load("testdata/official-literal.yaml", "demo")
	if err != nil {
		t.Fatal(err)
	}
	opt := testOptions(t)
	opt.UseFQDN = true
	targets, _ := Build(Objects{Namespace: "demo", StatefulSets: set.StatefulSets, Services: set.Services}, opt)
	if len(targets) != 1 || targets[0].Host != "pg.demo.svc.cluster.local" {
		t.Fatalf("targets = %+v", targets)
	}
}

func TestParseAccess(t *testing.T) {
	p, warnings := ParseAccess("system-admins:rw, developers:ro,bad developers:rw qa:admin a:b:ro dba:RO;ops:write ops:ro")
	got, _ := json.Marshal(p.Grants)
	want := `[{"group":"system-admins","level":"write"},{"group":"developers","level":"write"},{"group":"a:b","level":"read"},` +
		`{"group":"dba","level":"read"},{"group":"ops","level":"write"}]`
	if string(got) != want {
		t.Fatalf("grants %s", got)
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0], `"bad"`) || !strings.Contains(warnings[1], `"qa:admin"`) {
		t.Fatalf("warnings %v", warnings)
	}
	if p, _ := ParseAccess(" "); p.Grants == nil || len(p.Grants) != 0 {
		t.Fatal("blank annotation must give an empty, non-nil policy")
	}
}

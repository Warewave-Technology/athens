package discovery

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/testutil/k8sfixture"
)

var update = flag.Bool("update", false, "rewrite golden files")

func testOptions(t *testing.T) Options {
	t.Helper()
	opt, err := OptionsFromConfig(config.Default().Discovery)
	if err != nil {
		t.Fatal(err)
	}
	return opt
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

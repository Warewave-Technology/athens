package credentials

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/Warewave-Technology/athens/internal/config"
	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/testutil/k8sfixture"
)

// fixtureWorkloads serves pod specs straight from the fixture set.
type fixtureWorkloads struct{ set *k8sfixture.Set }

func (f fixtureWorkloads) PodSpec(_ string, ref discovery.ObjectRef) (*corev1.PodSpec, error) {
	for _, s := range f.set.StatefulSets {
		if ref.Kind == "StatefulSet" && s.Name == ref.Name {
			return &s.Spec.Template.Spec, nil
		}
	}
	for _, d := range f.set.Deployments {
		if ref.Kind == "Deployment" && d.Name == ref.Name {
			return &d.Spec.Template.Spec, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

// TestResolveFixtures resolves every discovered target of every fixture
// against the fixture's own Secrets.
func TestResolveFixtures(t *testing.T) {
	want := map[string]Credentials{
		"demo/pg":                    {"app", "s3cr3t-literal-value"},
		"demo/orders-db":             {"orders", "orders-secret-pw"},
		"demo/inventory":             {"inv", "inventory-secret-pw"},
		"demo/files":                 {"filer", "files-secret-pw"},
		"demo/my-release-postgresql": {"app", "bitnami-app-pw"},
		"demo/solo":                  {"postgres", "solo-secret-pw"},
		"demo/payments-db":           {"payments", "cnpg-app-pw"},
		"demo/reports":               {"app", "reports-owner-pw"},
		"demo/acid-minimal":          {"foo_user", "zalando-foo-pw"},
		"demo/legacy":                {"crm_admin", "legacy-admin-pw"},
		"demo/db-sts":                {"postgres", "collision-sts-pw"},
		"demo/db-deploy":             {"postgres", "collision-deploy-pw"},
		"demo/analytics":             {"analyst", "analytics-secret-pw"},
		"demo/billing":               {"billing", "billing-app-pw"},
		"demo/acid-ledger":           {"ledger", "ledger-pw"},
		"demo/crm":                   {"postgres", "crm-pw"},
		"demo/locked":                {"postgres", "locked-pw"},
	}
	wantDB := map[string]string{"demo/orders-db": "orders", "demo/inventory": "inventory", "demo/files": "filer", "demo/pg": "appdb"}
	opt, err := discovery.OptionsFromConfig(config.Default().Discovery)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob("../discovery/testdata/*.yaml")
	seen := map[string]bool{}
	for _, f := range files {
		set, err := k8sfixture.Load(f, "demo")
		if err != nil {
			t.Fatal(err)
		}
		targets, _ := discovery.Build(discovery.Objects{Namespace: "demo", StatefulSets: set.StatefulSets, Deployments: set.Deployments,
			Services: set.Services, CNPG: set.CNPG, Zalando: set.Zalando}, opt)
		r := NewResolver(fake.NewClientset(set.Typed()...), fixtureWorkloads{set})
		for _, tg := range targets {
			seen[tg.ID] = true
			got, err := r.Resolve(context.Background(), "demo", tg.RW)
			if err != nil {
				t.Errorf("%s: %v", tg.ID, err)
				continue
			}
			if got != want[tg.ID] {
				t.Errorf("%s: got %s/%q want %s", tg.ID, got.User, got.Password, want[tg.ID])
			}
			if db, ok := wantDB[tg.ID]; ok {
				if got := r.DefaultDB(context.Background(), tg); got != db {
					t.Errorf("%s: default db %q want %q", tg.ID, got, db)
				}
			}
			if tg.RO != nil {
				if _, err := r.Resolve(context.Background(), "demo", *tg.RO); err != nil {
					t.Errorf("%s ro: %v", tg.ID, err)
				}
			}
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("target %s not discovered", id)
		}
	}
}

func TestErrorsAndRedaction(t *testing.T) {
	r := NewResolver(fake.NewClientset(), nil)
	_, err := r.Resolve(context.Background(), "ns", discovery.CredentialRef{
		User:     discovery.ValueSource{Literal: "u"},
		Password: discovery.ValueSource{Secret: &discovery.KeyRef{Name: "missing", Key: "pw"}},
	})
	if err == nil || !strings.Contains(err.Error(), "secret ns/missing") {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.Resolve(context.Background(), "ns", discovery.CredentialRef{Password: discovery.ValueSource{Literal: "x"}}); err == nil {
		t.Fatal("literal passwords must be rejected")
	}
	c := Credentials{User: "u", Password: "hunter2"}
	if strings.Contains(c.String(), "hunter2") || strings.Contains(fmt.Sprintf("%v %+v", c, c), "hunter2") {
		t.Fatal("password leaked via fmt")
	}
	if r.DefaultDB(context.Background(), discovery.Target{}) != "postgres" {
		t.Fatal("default db fallback")
	}
}

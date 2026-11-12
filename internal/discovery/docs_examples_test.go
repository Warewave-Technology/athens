package discovery

import (
	"testing"

	"github.com/Warewave-Technology/athens/internal/config"
)

// TestDocsExamples keeps the manifests in docs/examples working with the
// default (annotation-only) configuration.
func TestDocsExamples(t *testing.T) {
	opt, err := OptionsFromConfig(config.Default().Discovery)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		file   string
		check  func(Target) bool
		detail string
	}{
		{"statefulset-minimal.yaml", func(x Target) bool {
			return x.ID == "demo/orders-db" && x.Host == "orders-db.demo.svc" && x.Port == 5432 && x.DefaultDB == "orders" && x.RW.Password.Secret != nil
		}, "env credentials, POSTGRES_DB"},
		{"deployment-production.yaml", func(x Target) bool {
			return x.DisplayName == "Billing" && x.Environment == "production" && x.RO != nil && x.DefaultDB == "billing" && x.SSLMode == "require"
		}, "explicit secret, ro secret, production"},
		{"cnpg-cluster.yaml", func(x Target) bool {
			return x.Source == SourceCNPG && x.Host == "payments-rw.demo.svc" && x.DefaultDB == "payments" && x.RW.Password.Secret.Name == "payments-app"
		}, "cnpg"},
		{"zalando-postgresql.yaml", func(x Target) bool {
			return x.Source == SourceZalando && x.DefaultDB == "ledger" &&
				x.RW.Password.Secret.Name == "ledger-owner.acid-ledger.credentials.postgresql.acid.zalan.do"
		}, "zalando"},
		{"external-database.yaml", func(x Target) bool {
			return x.Host == "crm-flexible.demo.svc" && x.SSLMode == "verify-ca" && x.CA != nil && x.CA.Name == "azure-postgres-ca" && x.DefaultDB == "crm"
		}, "ExternalName service"},
		{"access-annotations.yaml", func(x Target) bool {
			return x.ReadOnly && x.ReadOnlyRole == "athens_reader" && x.Access != nil && len(x.Access.Grants) == 2 && x.Access.Grants[0].Group == "system-admins"
		}, "readonly + access"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			targets, problems := Build(load(t, "../../docs/examples/"+tc.file), opt)
			if len(problems) != 0 || len(targets) != 1 {
				t.Fatalf("targets %+v problems %+v", targets, problems)
			}
			if targets[0].Access == nil || len(targets[0].Access.Grants) == 0 {
				t.Fatalf("example must carry an access annotation: %+v", targets[0])
			}
			if !tc.check(targets[0]) {
				t.Fatalf("%s: unexpected target %+v", tc.detail, targets[0])
			}
		})
	}
}

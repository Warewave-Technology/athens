# Discovery and credential resolution

Package: `internal/discovery` (finding targets) and `internal/credentials`
(resolving secrets at connect time).

## 1. Model

```go
type SourceKind string // "annotation" | "cnpg" | "zalando" | "generic"

type Target struct {
    ID          string            // "<namespace>/<name>", unique
    Namespace   string
    Name        string            // logical name (service or cluster name)
    DisplayName string            // annotation or Name
    Source      SourceKind
    Owner       ObjectRef         // workload / CR this came from (kind, name, uid)
    Host        string            // service DNS: <svc>.<ns>.svc (cluster domain from config)
    Port        int
    Databases   []string          // hint only; live list comes from pg_database
    DefaultDB   string            // db used for the initial connection
    SSLMode     string            // disable|allow|prefer|require|verify-ca|verify-full
    CARef       *SecretKeyRef     // for verify-*
    Environment string            // "production" | "staging" | "development" | "" (free text allowed)
    RW          CredentialRef     // required
    RO          *CredentialRef    // optional read-only credential
    Labels      map[string]string // copied from owner, for authz `targetLabels` (future)
    Warnings    []string          // non-fatal problems shown in admin UI
}

type CredentialRef struct {
    // exactly one of Secret / Literal is set
    Secret   *SecretCreds
    Literal  *LiteralCreds // only from plain env values in pod spec
}
type SecretCreds struct {
    Namespace, Name string
    UserKey, PasswordKey string // UserKey may be "" if User is fixed
    User string                 // fixed user when not from secret
}
type LiteralCreds struct {
    // Read from the workload object at connect time, NOT copied here.
    // Holds a pointer to (kind, ns, name, container, env var name).
    Workload ObjectRef; Container, UserEnv, PasswordEnv string; User string
}
```

Rules:
- Target objects hold **references**, never secret values.
- `internal/credentials.Resolve(ctx, CredentialRef) (user, password string, err)`
  does a live `GET` on the Secret (or reads the workload from informer cache for
  literal env). Results are kept only in memory inside the pool config.
- Registry (`discovery.Registry`) holds `map[ID]Target`, guarded by RWMutex,
  emits change events (`added/updated/removed`) consumed by the pool manager.

## 2. Informers

For each namespace in `discovery.namespaces`, a namespaced
`SharedInformerFactory` (`informers.WithNamespace`) for StatefulSets,
Deployments and Services (no Pods; ADR-016). CRD sources
via `dynamicinformer.NewFilteredDynamicSharedInformerFactory` per namespace,
started only if the CRD exists (check via discovery API at startup and every
5 min; start informer when it appears).

Resync period: `discovery.resync` (default 10m). Any event on a relevant object
triggers a debounced (500ms) full rebuild for that namespace. Rebuild = run all
sources over the cached objects of the namespace, merge with priority (§3),
diff against registry, emit events. This is simpler and correct; namespaces
are small.

## 3. Source priority and dedup

Order: `annotation` > `cnpg` > `zalando` > `generic`.

- Identity for dedup = `(namespace, host, port)`.
- Higher-priority source wins; lower one is dropped (debug log).
- `generic` must skip workloads owned by operators:
  - pods/sts with label `cnpg.io/cluster` or ownerReference kind `Cluster`
    (group `postgresql.cnpg.io`)
  - sts with label `application: spilo` or ownerReference kind `postgresql`
    (group `acid.zalan.do`)
- Opt-out: annotation `athens.warewave.io/enabled: "false"` on the workload,
  Service or CR removes it from all sources.
- Name collisions (two targets same ID): append `-<kind-short>` suffix
  (`-sts`, `-deploy`), add warning.

## 4. Sources

### 4.1 annotation (explicit, highest priority)
On a StatefulSet, Deployment or Service:

| Annotation (prefix `athens.warewave.io/`) | Required | Default |
|---|---|---|
| `enabled` | yes, `"true"` | |
| `service` | no (if on workload) | auto-match (§4.5) ; on a Service: itself |
| `port` | no | Service port named `postgres`/`postgresql`/`tcp-postgresql`, else 5432 |
| `secret` | yes* | |
| `user-key` | no | `username` |
| `password-key` | no | `password` |
| `user` | no | fixed user if secret has no user key |
| `readonly-secret` | no | (enables RO credential) |
| `readonly-user-key` / `readonly-password-key` | no | `username` / `password` |
| `database` | no | user name, then `postgres` |
| `display-name` | no | name |
| `environment` | no | `""` |
| `sslmode` | no | `discovery.defaultSSLMode` (prefer) |
| `ca-secret` / `ca-key` | no | `ca.crt` |

\* If `secret` missing on a workload: fall back to generic env resolution (§4.4)
for credentials but keep the annotation metadata. If still unresolved: target
not registered, warning recorded.

### 4.2 cnpg (`postgresql.cnpg.io/v1`, kind `Cluster`)
- Name = cluster name. Host = `<cluster>-rw.<ns>.svc`, port 5432.
- RW credential: Secret `<cluster>-app`, keys `username`/`password`
  (if `spec.bootstrap.initdb.secret.name` is set, use that secret).
- Superuser `<cluster>-superuser` used **only** if `discovery.cnpg.useSuperuser`
  is true AND `spec.enableSuperuserAccess` is true. Default false.
- RO credential: not auto-derived (CNPG has no RO user by default); annotation
  `athens.warewave.io/readonly-secret` on the Cluster CR is honoured.
- DefaultDB = `spec.bootstrap.initdb.database` or `app`.
- SSLMode default `require` (CNPG always serves TLS); `verify-full` possible
  with CA secret `<cluster>-ca` key `ca.crt` when `discovery.cnpg.verifyTLS`.
- Annotations from §4.1 (`display-name`, `environment`, ...) on the CR apply.

### 4.3 zalando (`acid.zalan.do/v1`, kind `postgresql`)
- Name = metadata.name. Host = `<name>.<ns>.svc` (master service), port 5432.
- Users: `spec.users` map. Choose credential user:
  1. annotation `athens.warewave.io/user` on CR, else
  2. owner of first database in `spec.databases` (sorted by key), else
  3. `postgres` only if `discovery.zalando.useSuperuser`.
- Secret name from template `discovery.zalando.secretTemplate`, default
  `{username}.{cluster}.credentials.postgresql.acid.zalan.do`, where username
  has `_` replaced by `-`. Keys `username`/`password`.
- Databases hint = keys of `spec.databases`; DefaultDB = first.
- SSLMode default `require`.

### 4.4 generic (StatefulSet / Deployment)
Candidate if any container image (repo part, without registry/tag) matches
`discovery.generic.imagePattern`, default:
`(^|/)(postgres|postgresql|postgis/postgis|pgvector/pgvector|timescale/timescaledb[^/]*|bitnami/postgresql[^/]*)$`.

Credential resolution in the matching container, first hit wins:

Password env names (in order): `POSTGRES_PASSWORD`, `POSTGRESQL_PASSWORD`,
`POSTGRES_POSTGRES_PASSWORD`, `POSTGRESQL_POSTGRES_PASSWORD`.
User env names: `POSTGRES_USER`, `POSTGRESQL_USERNAME`, `POSTGRESQL_USER`
(default `postgres`). Database env names: `POSTGRES_DB`, `POSTGRES_DATABASE`,
`POSTGRESQL_DATABASE` (default = user).

Pairing rule: if `POSTGRES_POSTGRES_PASSWORD` (bitnami admin) is chosen, user
is `postgres`; otherwise user from user env.

Value forms to support for each env:
1. `value: "..."` → `LiteralCreds` (warning: "plaintext password in pod spec").
2. `valueFrom.secretKeyRef` → `SecretCreds`.
3. `envFrom.secretRef` (whole secret as env) → `SecretCreds` with key = env name
   (respect `prefix`).
4. `envFrom.configMapRef` → for user/db only (never accept password from
   ConfigMap; warn and skip).
5. `<NAME>_FILE` env pointing to a path → find the `volumeMount` whose
   `mountPath` is a prefix of the file path, its volume must be a `secret`
   volume (or `projected` with a secret source); key = file name relative to
   mountPath (respect `items[].path` mapping) → `SecretCreds`.
   Bitnami charts use this (`/opt/bitnami/postgresql/secrets/...`).
6. `valueFrom.fieldRef` / anything else → unresolved, warning.

Port: container port named `postgresql`/`postgres`/`tcp-postgresql`, else
containerPort 5432, else 5432.

### 4.5 Service matching (annotation without `service`, and generic)
Among Services in the namespace:
1. Selector non-empty and selector ⊆ workload pod template labels.
2. Exposes a port whose `targetPort` equals the container port (number or name).
3. Prefer non-headless (`clusterIP != None`); among equal, prefer name without
   `-hl`/`-headless`/`-repl`/`-replica`/`-read`/`-ro` suffix; then shortest name.
4. If only headless matches and workload is a StatefulSet: host =
   `<sts>-0.<svc>.<ns>.svc` (primary assumption), warning added.
5. No match → target not registered, warning.

Host uses `discovery.clusterDomain` (default `cluster.local`) only when
`discovery.useFQDN` is true; default short form `<svc>.<ns>.svc`.

## 5. Read-only credentials
`RO` is set when an annotation (`readonly-secret`) exists on the workload/CR/
Service. Pool manager uses RO for users whose effective level is `read`.
If RO auth fails, fall back to RW + READ ONLY tx and add a target warning.

## 6. Test fixtures (fake clientset), one file each in `internal/discovery/testdata/`
1. `official-literal.yaml`   — sts postgres:16, POSTGRES_PASSWORD literal, svc
2. `official-secretref.yaml` — deployment, secretKeyRef user+password, svc
3. `official-envfrom.yaml`   — envFrom secretRef with prefix
4. `official-file.yaml`      — POSTGRES_PASSWORD_FILE + secret volume with items
5. `bitnami.yaml`            — chart-like sts, `_FILE` vars, svc + `-hl` svc
6. `headless-only.yaml`      — sts with only headless svc → pod-0 host + warning
7. `no-service.yaml`         — → not registered, warning
8. `cnpg.yaml`               — Cluster CR + secrets + operator-owned pods (generic must skip)
9. `zalando.yaml`            — postgresql CR with users/databases + spilo sts (generic must skip)
10. `annotation-override.yaml` — annotated sts that would also match generic → annotation wins
11. `opt-out.yaml`           — enabled=false
12. `configmap-password.yaml` — password via configMapRef → skipped + warning
13. `name-collision.yaml`    — sts and deployment same name

Each fixture has an expected-targets golden JSON (credential refs included,
values never).

# Discovery and credential resolution

Package: `internal/discovery` (finding targets) and `internal/credentials`
(resolving secrets at connect time).

## 1. Model

> Implemented with the value-source credential model of ADR-017; the struct
> below is the original sketch.

```go
type SourceKind string // "annotation" | "cnpg" | "zalando"

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

Scope (ADR-024): with `discovery.namespaces` empty (the default) one
cluster-wide `SharedInformerFactory` watches StatefulSets, Deployments and
Services in every namespace; with namespaces listed, one namespaced factory
(`informers.WithNamespace`) per namespace. No Pods (ADR-016). CRD sources
via `dynamicinformer.NewFilteredDynamicSharedInformerFactory` with the same
scope, started only if the CRD exists (check via discovery API at startup
and every 5 min; start informer when it appears).

Resync period: `discovery.resync` (default 10m). Any event on a relevant object
triggers a debounced (500ms) full rebuild of the object's namespace. Rebuild =
run all sources over the cached objects of that namespace, merge with
priority (§3), diff against registry, emit events. In a cluster-wide watch,
namespaces without targets or problems are dropped from the status list so
the admin page shows only namespaces that matter.

## 2a. Annotation only (ADR-020, ADR-024)

An object becomes a target only if it carries `<prefix>/enabled: "true"`:
- StatefulSet / Deployment: annotation source; credentials from the `secret`
  annotation, else from the container env exactly as §4.4 describes.
- Service: annotation source (needs the `secret` annotation, §4.1).
- CNPG `Cluster` / Zalando `postgresql`: the cnpg / zalando sources (§4.2,
  §4.3).
Unannotated workloads and clusters are ignored silently; there is no
automatic image-based discovery any more.

## 2b. Access annotations (ADR-022, ADR-023)

Two annotations, on the same object as `enabled`, decide who may use a
target. There are no access rules in the configuration.

- `<prefix>/access: "system-admins:rw,developers:ro"` — the target's access
  list, **required**. Entries are separated by commas, semicolons or spaces;
  levels are `rw`/`write` or `ro`/`read`; a group listed twice gets the
  higher level. A group is the value in the token's `groups` claim (an Entra
  object ID, or a name when Entra is configured to emit names), or a name
  from `authz.groupAliases`. Invalid entries are dropped with a warning.
  Without the annotation, or when it grants nobody, nobody can use the
  target (it is still listed on Admin → Discovery with a warning).
- `<prefix>/readonly: "true"` — every user's level on the target is capped at
  `read`: READ ONLY transactions, the read-only credential when present, no
  grid edits, DML/DDL rejected in the editor. Any value other than
  `true`/`false` is treated as `true` (with a warning).

The global `authz.readOnly` switch still applies, and `authz.adminGroups`
gets no data access by itself.

## 3. Source priority and dedup

Order: `annotation` (workload or Service) > `cnpg` > `zalando`.

- Identity for dedup = `(namespace, host, port)`.
- Higher-priority source wins; lower one is dropped (debug log).
- Operator-owned workloads (CNPG instance pods, Zalando spilo StatefulSets)
  carry no annotation and are therefore ignored; annotate the `Cluster` /
  `postgresql` instead.
- A Service annotated `athens.warewave.io/enabled: "false"` hides the
  workloads that would connect through it.
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
| `readonly-provision` | no | `"true"`: athens creates and manages the RO login `athens_read` (§5); ignored with a warning when `readonly-secret` is set |
| `database` | no | env, else user name; `postgres` if that does not exist |
| `display-name` | no | name |
| `environment` | no | `""` |
| `sslmode` | no | `discovery.defaultSSLMode` (prefer) |
| `ca-secret` / `ca-key` | no | `ca.crt` |
| `readonly` | no | `"true"` caps every user at read (§2b) |
| `access` | yes, to be usable | `group:rw,group:ro,...`: who may use the target (§2b) |

\* If `secret` missing on a workload: fall back to env resolution (§4.4)
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

### 4.4 Credentials from the workload env (annotated StatefulSet / Deployment without `secret`)
The postgres container is the first one whose image (repo part, without
registry/tag) matches `discovery.imagePattern`, default
`(^|/)(postgres|postgresql|postgis/postgis|pgvector/pgvector|timescale/timescaledb[^/]*|bitnami/postgresql[^/]*)$`;
without a match, the first container.

Credential resolution in that container, first hit wins:

Password env names (in order): `POSTGRES_PASSWORD`, `POSTGRESQL_PASSWORD`,
`POSTGRES_POSTGRES_PASSWORD`, `POSTGRESQL_POSTGRES_PASSWORD`.
User env names: `POSTGRES_USER`, `POSTGRESQL_USERNAME`, `POSTGRESQL_USER`
(default `postgres`). Database env names: `POSTGRES_DB`, `POSTGRES_DATABASE`,
`POSTGRESQL_DATABASE` (default = user, the official image's convention).

The initial database is only used to connect and list databases. If it does
not exist (SQLSTATE 3D000: images that don't create a database named after
the user, or data initialised with other env), the pool manager connects to
`postgres`, then `template1`, instead and shows admins the warning
`database "<guess>" does not exist; connecting to "postgres" instead`. The
`database` annotation fixes the guess; a database the user picks explicitly
never falls back.

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

### 4.5 Service matching (annotated workload without `service`)
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

## 5. Read-only credentials (ADR-028)
Read users only ever connect with an RO login; the pool manager uses it for
users whose effective level is `read`. Writers use RW.

- `RO` is set when `readonly-secret` exists on the workload/CR/Service.
- Otherwise `Target.ROProvision` is set by `readonly-provision: "true"`:
  athens creates the role `athens_read` over the RW login on the first read
  pool (`CREATE ROLE ... LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE
  NOREPLICATION NOBYPASSRLS INHERIT PASSWORD '<SCRAM verifier>'`, a marker
  comment, `GRANT pg_read_all_data`) in one guarded transaction; later runs
  `ALTER ROLE ... WITH LOGIN PASSWORD '<verifier>'` only. The transaction is
  refused if the role then has any other attribute or membership.
  PostgreSQL 14+ only. The password is random, held in memory, never
  stored; it is replaced at every athens start and after an authentication
  failure of a pool that used the current password (a generation counter
  keeps pools of one target from replacing each other's password). An
  existing `athens_read` without the marker comment is never changed.
- With neither, `authz.Level` returns `none` instead of `read` for the
  target (it is invisible to read users) and discovery adds the warning
  "read users cannot connect" when the access list grants `ro` or the target
  is `readonly`.
- No fallback: a rejected or unreadable RO login, or a failed provisioning,
  is an error for read users (`read_login_unavailable`) and an admin
  warning. Only RW connections (and the RW health check) set the target
  status.
- Every read transaction checks that the RO login is not privileged
  (superuser, REPLICATION, BYPASSRLS, or member of such a role or of
  pg_monitor-like roles); if it is, read users get `read_login_privileged`.

## 6. Test fixtures (fake clientset), one file each in `internal/discovery/testdata/`
Every workload and operator resource in the fixtures is annotated
`enabled: "true"` unless the fixture is about ignoring it.

1. `official-literal.yaml`   — sts postgres:16, POSTGRES_PASSWORD literal, svc
2. `official-secretref.yaml` — deployment, secretKeyRef user+password, svc
3. `official-envfrom.yaml`   — envFrom secretRef with prefix
4. `official-file.yaml`      — POSTGRES_PASSWORD_FILE + secret volume with items
5. `bitnami.yaml`            — chart-like sts, `_FILE` vars, svc + `-hl` svc
6. `headless-only.yaml`      — sts with only headless svc → pod-0 host + warning
7. `no-service.yaml`         — → not registered, warning
8. `cnpg.yaml`               — Cluster CR + secrets + operator-owned (unannotated) objects that must be ignored
9. `zalando.yaml`            — postgresql CR with users/databases + spilo sts that must be ignored
10. `annotation-override.yaml` — annotated sts with explicit secret, ro secret, sslmode
11. `opt-out.yaml`           — enabled=false, and a Service opt-out hiding its workload
12. `configmap-password.yaml` — password via configMapRef → skipped + warning
13. `name-collision.yaml`    — sts and deployment same name

Each fixture has an expected-targets golden JSON (credential refs included,
values never).
14. `operators-annotated.yaml` — annotated and unannotated CNPG/Zalando clusters
15. `access.yaml`            — readonly + access annotations, invalid entries, empty access

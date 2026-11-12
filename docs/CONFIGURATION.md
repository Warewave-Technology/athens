# Configuration reference

athens reads one YAML file (`--config`, default `/etc/athens/config.yaml`;
the Helm chart renders it from `.Values.config`). Environment variables can
override it. At startup athens validates the whole file and reports every
problem at once. Unknown keys are errors, so a typo never silently falls back
to a default.

- [Minimal configuration](#minimal-configuration)
- [Environment overrides](#environment-overrides)
- [server](#server) · [oidc](#oidc) · [auth](#auth) · [authz](#authz) ·
  [discovery](#discovery) · [limits](#limits) · [audit](#audit) ·
  [store](#store) · [log](#log)
- [Command-line flags](#command-line-flags)
- [Helm values](#helm-values)

The annotated full example is [`config.example.yaml`](../config.example.yaml).

## Minimal configuration

Everything not shown here has a default.

```yaml
server:
  baseURL: https://athens.example.com
oidc:
  issuer: https://login.microsoftonline.com/<TENANT_ID>/v2.0
  clientID: <APPLICATION_CLIENT_ID>
  # clientSecret: from ATHENS_OIDC_CLIENTSECRET
auth: {}
  # sessionKey: from ATHENS_AUTH_SESSIONKEY
authz:
  adminGroups: ["<GROUP_ID_PLATFORM_ADMINS>"]
  rules:
    - name: dba
      groups: ["<GROUP_ID_DBA>"]
      namespaces: ["*"]
      level: write
discovery:
  namespaces: ["payments", "orders"]
```

## Environment overrides

Any scalar or list of strings can be set with `ATHENS_` + the upper-cased YAML
path joined by `_`:

| Setting | Variable | Example |
|---|---|---|
| `oidc.clientSecret` | `ATHENS_OIDC_CLIENTSECRET` | from a Secret |
| `auth.sessionKey` | `ATHENS_AUTH_SESSIONKEY` | from a Secret |
| `limits.statementTimeout` | `ATHENS_LIMITS_STATEMENTTIMEOUT` | `60s` |
| `authz.readOnly` | `ATHENS_AUTHZ_READONLY` | `true` |
| `discovery.namespaces` | `ATHENS_DISCOVERY_NAMESPACES` | `payments,orders` (comma separated) |
| `log.level` | `ATHENS_LOG_LEVEL` | `debug` |

`authz.rules` and the maps (`authz.groupAliases`, `server.devHostOverrides`)
can only be set in the file. The Helm chart already passes the two secrets
this way; add others with `extraEnv`.

## server

| Key | Default | Description |
|---|---|---|
| `listen` | `:8080` | address for the UI, API and health probes |
| `metricsListen` | `:9090` | address for Prometheus metrics (keep it off the ingress) |
| `baseURL` | — (required) | external URL, used for the OIDC redirect and Origin checks. `https` and no path; athens must run at the root of its host |
| `trustedProxies` | `[]` | CIDRs whose `X-Forwarded-For` is trusted for the client IP in the audit log, usually your ingress controller's pod CIDR |
| `insecureDev` | `false` | allows an `http` baseURL and turns off HSTS and the secure cookie flag. Local development only |
| `devHostOverrides` | `{}` | `"host:port": "local:port"` rewrites for port-forwarded targets. Only with `insecureDev` |

## oidc

| Key | Default | Description |
|---|---|---|
| `issuer` | — (required) | `https://login.microsoftonline.com/<tenant-id>/v2.0` |
| `clientID` | — (required) | application (client) ID of the app registration |
| `clientSecret` | — (required) | client secret; set it with `ATHENS_OIDC_CLIENTSECRET` |
| `redirectPath` | `/auth/callback` | must match the redirect URI registered in Entra (`<baseURL><redirectPath>`) |
| `scopes` | `[openid, profile, email]` | requested scopes |
| `allowedGroups` | `[]` | if set, only members of these group IDs may sign in |
| `workloadIdentity.enabled` | `false` | not implemented yet; keep `false` |
| `workloadIdentity.tokenFile` | `/var/run/secrets/azure/tokens/azure-identity-token` | for the future workload identity support |

## auth

| Key | Default | Description |
|---|---|---|
| `sessionKey` | — (required) | 32+ random bytes, base64 (`openssl rand -base64 32`); set it with `ATHENS_AUTH_SESSIONKEY`. Encrypts the short-lived sign-in state cookie |
| `sessionTTL` | `8h` | absolute session lifetime |
| `idleTTL` | `1h` | a session ends after this long without requests |

## authz

Details and examples: [ACCESS-CONTROL.md](ACCESS-CONTROL.md).

| Key | Default | Description |
|---|---|---|
| `readOnly` | `false` | emergency switch: everyone is capped at `read` |
| `adminGroups` | `[]` | group IDs that see the Administration pages (no data access by itself) |
| `rules` | `[]` | list of rules, below |
| `annotationAccess` | `true` | let the `athens.warewave.io/access` annotation replace the rules for its database |
| `groupAliases` | `{}` | `name: group-id` pairs usable in `access` annotations. Names may not contain `:`, `,` or spaces |

A rule:

| Key | Description |
|---|---|
| `name` | label used in error messages |
| `groups` | group object IDs (at least one) |
| `namespaces` | namespace globs (default `*`) |
| `targets` | target name globs: the workload, Service or cluster name (default `*`) |
| `databases` | database name globs (default `*`) |
| `level` | `read` or `write` |

Each rule needs at least one of `namespaces`, `targets`, `databases`. The
highest matching level wins.

```yaml
authz:
  adminGroups: ["7c1f0f6e-1111-4aaa-9bbb-000000000009"]
  groupAliases:
    dba: "7c1f0f6e-1111-4aaa-9bbb-000000000001"
    payments-dev: "7c1f0f6e-1111-4aaa-9bbb-000000000002"
  rules:
    - {name: dba, groups: ["7c1f0f6e-1111-4aaa-9bbb-000000000001"], namespaces: ["*"], level: write}
    - {name: payments-read, groups: ["7c1f0f6e-1111-4aaa-9bbb-000000000002"], namespaces: ["payments*"], level: read}
```

## discovery

How databases are found: [DATABASES.md](DATABASES.md) (how-to) and
[DISCOVERY.md](DISCOVERY.md) (exact rules).

| Key | Default | Description |
|---|---|---|
| `namespaces` | — (required) | namespaces to watch. No wildcards; the chart creates a Role in each |
| `requireAnnotation` | `true` | only objects annotated `<prefix>/enabled: "true"` become targets. `false` also finds unannotated postgres workloads and operator clusters |
| `annotationPrefix` | `athens.warewave.io` | prefix of every annotation |
| `resync` | `10m` | full informer resync (changes are seen immediately anyway) |
| `useFQDN` | `false` | connect to `<svc>.<ns>.svc.<clusterDomain>` instead of `<svc>.<ns>.svc` |
| `clusterDomain` | `cluster.local` | used with `useFQDN` |
| `defaultSSLMode` | `prefer` | sslmode for workloads and Services without the `sslmode` annotation (operators default to `require`) |
| `cnpg.enabled` | `true` | watch CloudNativePG `Cluster`s. A missing CRD is not an error; athens checks again every 5 minutes |
| `cnpg.useSuperuser` | `false` | use `<cluster>-superuser` on clusters with `enableSuperuserAccess` |
| `cnpg.verifyTLS` | `false` | `verify-full` against the `<cluster>-ca` Secret |
| `zalando.enabled` | `true` | watch Zalando `postgresql`s |
| `zalando.useSuperuser` | `false` | fall back to `postgres` when no database owner is found |
| `zalando.secretTemplate` | `{username}.{cluster}.credentials.postgresql.acid.zalan.do` | operator Secret name; must contain `{username}` |
| `generic.enabled` | `true` | detect plain postgres StatefulSets/Deployments by image |
| `generic.imagePattern` | official, bitnami, postgis, pgvector, timescale | regular expression over the image repository (no registry, no tag) |

To also detect a custom image, for example `registry.local/team/pg-custom:16`,
extend the pattern:

```yaml
discovery:
  generic:
    imagePattern: '(^|/)(postgres|postgresql|postgis/postgis|pgvector/pgvector|timescale/timescaledb[^/]*|bitnami/postgresql[^/]*|team/pg-custom)$'
```

## limits

| Key | Default | Description |
|---|---|---|
| `statementTimeout` | `30s` | `statement_timeout` for every query |
| `lockTimeout` | `5s` | `lock_timeout` for every query |
| `maxRows` | `1000` | most rows the SQL editor returns per statement; the row-limit picker can ask for fewer, never more (at most 50 000) |
| `gridMaxPageSize` | `500` | largest grid page (max 500) |
| `maxCellBytes` | `65536` | longer values are cut in results; the row panel loads the full value on demand |
| `poolMaxConns` | `4` | connections per pool (one pool per target, database and level) |
| `concurrentQueriesPerUser` | `3` | parallel editor queries per user; more get 429 |
| `historyPerUser` | `500` | editor history entries kept per user |

## audit

Details: [OPERATIONS.md — Audit trail](OPERATIONS.md#audit-trail).

| Key | Default | Description |
|---|---|---|
| `logSQL` | `true` | store SQL text in audit records (cut at 8 KiB) |
| `logValues` | `false` | store row values of grid writes and grid filter values (otherwise only column names and primary keys) |
| `logReads` | `true` | record grid page reads, full cell reads and structure views |
| `retentionDays` | `90` | records older than this are purged daily from the store (not from your log pipeline) |

## store

| Key | Default | Description |
|---|---|---|
| `path` | `/data/athens.db` | SQLite file for users, sessions, history, saved queries, drafts and the audit log. Needs a persistent volume |

## log

| Key | Default | Description |
|---|---|---|
| `level` | `info` | `debug`, `info`, `warn` or `error`. Logs are JSON on stdout; audit records are also logged at every level |

## Command-line flags

| Flag | Default | Description |
|---|---|---|
| `--config` | `/etc/athens/config.yaml` | configuration file |
| `--kubeconfig` | in-cluster, then `$KUBECONFIG`, then `~/.kube/config` | Kubernetes access |
| `--dev` | `false` | proxy the UI to the Vite dev server |
| `--dev-url` | `http://localhost:5173` | Vite dev server address |
| `--version` | | print version and exit |

## Helm values

The chart ([`deploy/helm/athens`](../deploy/helm/athens)) takes the
configuration above under `config:` and adds:

| Value | Default | Description |
|---|---|---|
| `image.repository` / `image.tag` | `ghcr.io/warewave-technology/athens` / chart appVersion | image |
| `secret.existingSecret` | `""` | Secret with keys `clientSecret` and `sessionKey` (recommended) |
| `secret.clientSecret` / `secret.sessionKey` | `""` | used to create the Secret when `existingSecret` is empty |
| `persistence.enabled` / `size` / `storageClass` / `existingClaim` | `true` / `1Gi` / `""` / `""` | volume for the SQLite store |
| `ingress.enabled` / `className` / `host` / `tls` / `annotations` | `false` | Ingress |
| `service.port` / `metricsPort` | `8080` / `9090` | Service ports |
| `serviceMonitor.enabled` | `false` | Prometheus Operator ServiceMonitor |
| `networkPolicy.enabled` | `true` | ingress from the ingress controller namespace, egress to the API server, Entra ID, DNS and Postgres |
| `networkPolicy.ingressNamespaceSelector` | `kubernetes.io/metadata.name: ingress-nginx` | who may reach port 8080 |
| `networkPolicy.metricsNamespaceSelector` | `kubernetes.io/metadata.name: monitoring` | who may scrape port 9090 |
| `networkPolicy.postgresPorts` | `[5432]` | Postgres ports allowed towards the discovery namespaces |
| `networkPolicy.extraEgress` | `[]` | extra egress rules, e.g. an external database |
| `resources` | 50m / 128Mi requests, 512Mi limit | |
| `extraEnv` | `[]` | extra environment variables (overrides) |
| `podAnnotations`, `podLabels`, `nodeSelector`, `tolerations`, `affinity` | | scheduling |

For each namespace in `config.discovery.namespaces`, the chart creates a Role
and RoleBinding that can `get/list/watch` StatefulSets, Deployments, Services
and the CNPG/Zalando resources, and `get` Secrets and ConfigMaps. It does not
grant `list` on Secrets, so athens reads only the Secrets that discovered
databases reference.

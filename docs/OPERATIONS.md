# Operations

## Architecture in one paragraph

One pod (`replicas: 1`, `Recreate`) serves the SPA, `/api/v1`, `/auth/*` and
health probes on `:8080` and Prometheus metrics on `:9090`. State lives in
SQLite at `/data/athens.db` on a PVC: users, sessions, query history, saved
queries, drafts, UI state and the audit log. Every audit record is also
written to stdout as JSON (`"log":"audit"`), so the cluster log pipeline holds
a second copy. Credentials for Postgres targets are read from Secrets when a
connection pool is created and live only in memory.

## Health and readiness

| Endpoint | Meaning |
|---|---|
| `GET /healthz` | process is alive |
| `GET /readyz` | store reachable, OIDC provider initialized, discovery caches synced (JSON body shows which check fails) |
| `GET :9090/metrics` | Prometheus metrics, prefix `athens_` |

Useful metrics: `athens_discovery_targets`, `athens_discovery_errors_total`,
`athens_pool_conns`, `athens_queries_total{result}`,
`athens_auth_logins_total{result}`, `athens_audit_write_errors_total` (must stay 0).

## Backups

The PVC holds user data (history, saved queries) and the audit log.

```bash
kubectl -n athens exec deploy/athens -- /athens --version   # confirm the pod
```

SQLite is in WAL mode; take a consistent copy with the volume snapshot
mechanism of your storage class (`VolumeSnapshot`), or scale to zero first:

```bash
kubectl -n athens scale deploy/athens --replicas=0
```

then copy `/data/athens.db*` from the volume with a helper pod and scale back
to 1. Losing the PVC loses history/saved queries but not the audit trail
shipped through stdout.

Audit records older than `audit.retentionDays` (default 90) are purged daily;
sessions are purged every 10 minutes.

## Audit trail

athens writes every audit record twice: to the `audit_log` table on the PVC
(what Admin → Audit shows) and to stdout as one JSON line with
`"log":"audit"`. The SQLite copy is convenient but not tamper-proof: anyone who
can exec into the pod or mount the PVC can change it. Treat the stdout copy as
the record of truth and ship it to append-only storage.

Events: `login`, `login_denied`, `logout`, `query_execute`, `query_cancel`,
`row_insert`, `row_update`, `row_delete`, `access_denied`, `confirm_required`,
`rows_read`, `cell_read`, `structure_view` (reads, `audit.logReads`),
`saved_query_create|update|delete`, `history_delete`, `admin_view`,
`audit_export`. Drafts and UI state are autosaved on every edit and are not
audited.

### Shipping the stdout copy

Select lines whose JSON has `log == "audit"` from the athens container and
send them to a store with retention and write-once guarantees (S3 Object Lock,
Azure immutable blob storage, a SIEM). Examples:

Fluent Bit (Kubernetes filter already parsing JSON into the record):

```ini
[FILTER]
    Name    grep
    Match   kube.*athens*
    Regex   log ^audit$

[OUTPUT]
    Name    azure_blob          # or s3 with Object Lock, splunk, es, ...
    Match   kube.*athens*
    # ...
```

Vector:

```toml
[transforms.athens_audit]
type      = "filter"
inputs    = ["kubernetes_logs"]
condition = '.kubernetes.container_name == "athens" && parse_json!(.message).log == "audit"'
```

Loki (Promtail/Alloy with the JSON stage) — query:

```logql
{namespace="athens", container="athens"} | json | log="audit"
```

Alert on `increase(athens_audit_write_errors_total[10m]) > 0` (the SQLite copy
is failing) and on the absence of audit lines from a running pod in your log
backend.

### Exporting

Admin → Audit → **CSV** / **JSON Lines** downloads every record matching the
applied filters (up to 100 000 per export; the response carries
`X-Athens-Export-Capped: true` when the cap was hit). Exports are audited
(`audit_export`). In CSV, cells that start with `= + - @` get a leading `'` so
spreadsheets do not evaluate them.

## Upgrades

Upgrading to 0.1.3 or later ends existing sessions once: the session
cookies were renamed to `__Host-athens_session` / `__Host-athens_csrf`
(ADR-027), so everyone signs in again.

Upgrading to 0.2.0 changes read access (ADR-028): users with `ro` access
only connect with a read-only login. Before upgrading, check Admin →
Discovery for databases that grant `ro` (or are `readonly`) and give each
one `athens.warewave.io/readonly-provision: "true"` or a `readonly-secret`;
after the upgrade, the warning "read users cannot connect" lists any you
missed. Remove `athens.warewave.io/readonly-role` annotations; they are
ignored.

`helm upgrade` replaces the pod (Recreate); expect a few seconds of downtime.
Schema migrations of the SQLite store run automatically at start and are
forward-only; back up the PVC before major upgrades. Sessions survive
upgrades.

## Rotating secrets

| Secret | How |
|---|---|
| Entra client secret | create a new secret in the app registration, update `clientSecret` in the Kubernetes Secret, `kubectl rollout restart deploy/athens`, then delete the old Entra secret |
| Session key | update `sessionKey` and restart. In-flight logins (10 min window) fail and must be restarted; existing sessions stay valid (only the login-state cookie is encrypted with the key) |
| Postgres passwords | rotate in the database and the Secret. athens retries once with re-read credentials on an authentication failure, so the next request succeeds without a restart |

## Troubleshooting

| Symptom | Check |
|---|---|
| Sign-in page "too many groups" | the user is in more than 200 groups and the token has an overage claim. With Entra ID P1/P2: emit "Groups assigned to the application" and assign the groups to the enterprise app (README step 1.3/1.4). On a Free tenant there is no v1 workaround besides fewer group memberships for that user (ADR-007) |
| Everyone signs in but nobody sees a database | the groups claim is empty: "Groups assigned to the application" is set but no groups are assigned (common on Entra ID Free, which cannot assign groups). Switch the groups claim to "Security groups" (README step 1.3) |
| Sign-in "not authorized for this application" | `oidc.allowedGroups` is set and the user is in none of them |
| Sign-in "tenant mismatch" | `oidc.issuer` tenant differs from the user's `tid` (guest accounts from other tenants are rejected) |
| A user sees no targets | no database's `athens.warewave.io/access` annotation lists one of the user's groups. The token carries group **object IDs** unless Entra is set to emit names; use IDs or `authz.groupAliases`. Ask the user to sign out and in again after group changes (groups are snapshotted at login) |
| A database is missing | Admin → Discovery lists every annotated database with warnings (for example "no access annotation") and, per namespace, the annotated objects that could not be added (`no_service`, `credentials_unresolved`, ...) |
| Target shows `unreachable` | Admin → Pools shows the last connection error; check NetworkPolicy egress (`networkPolicy.postgresPorts`), the Secret keys, and `sslmode` |
| `/readyz` stays 503 with `discovery:false` | the ServiceAccount lacks list/watch (check the generated ClusterRole, or the Role in each listed namespace) or the API server is unreachable (NetworkPolicy egress to 443/6443) |
| `audit_write_errors_total` increases | the PVC is full or read-only; audit records still reach stdout |
| Queries fail with "statement timeout exceeded" | raise `limits.statementTimeout` (default 30s) |

Logs are JSON on stdout. Credentials, cookies, tokens and SQL parameters are
never logged; SQL text appears only in audit records (`audit.logSQL`).

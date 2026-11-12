# athens — Specification

Status: v1 draft for implementation. Owner: Warewave.

## 1. Purpose

A single in-cluster web application that gives engineers browser access to all
PostgreSQL databases running in the cluster:

- Login only through Microsoft Entra ID (OIDC). No local users.
- PostgreSQL instances are opted in by annotation on their StatefulSet,
  Deployment, Service, CloudNativePG or Zalando cluster; athens finds the
  endpoint and credential references itself.
- Access per user is derived from Entra group membership and the database's
  `access` annotation (ADR-023); the athens config holds no access rules.
- Functional reference: pg-view (schema explorer, data grid/editor, SQL editor
  with history/favorites/drafts, read-only mode, guardrails). Visual reference:
  `docs/design/`.

### 1.1 Non-goals (v1)
- Non-PostgreSQL engines.
- Managing PostgreSQL roles/users, backups, replication.
- Multi-cluster. (One deployment per cluster.)
- Multi-replica HA (SQLite store => single replica; see ADR-003).
- ER diagrams, query plan visualization, data export beyond CSV of a result.

## 2. Users and roles

| Role | Source | Can |
|---|---|---|
| Viewer | `access` entry `ro` | see target, browse schema/data, run SQL in READ ONLY tx |
| Editor | `access` entry `rw` | + edit rows in grid, run DML/DDL in SQL editor (with confirm for destructive) |
| App admin | member of `authz.adminGroups` | + view audit log, discovery status, pool status for all targets |

A user whose groups are not on a target's `access` annotation does not see
that target at all (404, not 403, to avoid enumeration); a target without
the annotation is invisible to everyone. The `readonly` annotation caps
everyone at read (DISCOVERY.md §2b, ADR-022, ADR-023).

## 3. Architecture

```
Browser ──HTTPS──> Ingress ──> athens (1 pod)
                                 │  ├─ OIDC (login.microsoftonline.com)
                                 │  ├─ kube-apiserver (informers, GET secrets)
                                 │  ├─ SQLite on PVC (/data/athens.db)
                                 │  └─ pgxpool ──> Postgres targets (ns A, B, ...)
```

Single Go binary serves: SPA static assets, `/api/v1/*`, `/auth/*`,
`/healthz`, `/readyz`, `/metrics` (metrics on separate port, default `:9090`).

### 3.1 Process lifecycle
1. Load + validate config (fail fast with clear messages).
2. Open SQLite, run migrations.
3. Init OIDC provider (discovery doc fetch; retry with backoff up to 60s).
4. Start discovery informers (cluster-wide, or per `discovery.namespaces`
   entry); wait for cache sync
   (timeout 60s; on timeout start anyway, `/readyz` stays false until synced).
5. Start HTTP servers. On SIGTERM: stop accepting, cancel running queries,
   wait up to 20s, close pools, close store.

## 4. Configuration

File `/etc/athens/config.yaml` (path via `--config`), every scalar overridable
by env `ATHENS_<UPPER_SNAKE_PATH>` (e.g. `ATHENS_OIDC_CLIENTSECRET`).
Secrets (client secret, session key) should come from env/Secret, not the file.
See `config.example.yaml` for the full annotated example. Validation rules:

- `server.baseURL` required, must be https unless `server.insecureDev=true`.
- `oidc.issuer` must match `https://login.microsoftonline.com/<tenant>/v2.0`.
- Exactly one of `oidc.clientSecret` or `oidc.workloadIdentity.enabled` (M8).
- `discovery.namespaces` optional (empty = whole cluster, ADR-024); entries
  are explicit names, no `*`.
- Removed settings (`authz.rules`, `authz.annotationAccess`,
  `discovery.requireAnnotation`, `discovery.generic`) fail with a message
  naming their replacement.
- Limits must be positive; `limits.maxRows <= 50000`.

## 5. Authentication (Entra ID)

### 5.1 Flow
Authorization Code + PKCE, confidential client (client secret) in v1.

- `GET /auth/login?returnTo=/path` → generate `state`, `nonce`, PKCE verifier;
  store in a short-lived (10 min) HttpOnly cookie `athens_auth` (encrypted with
  session key, AES-GCM). Redirect to Entra authorize endpoint with scopes
  `openid profile email`.
- `GET /auth/callback` → verify state, exchange code, verify ID token
  (issuer, audience = clientID, nonce, expiry, signature via JWKS).
- Claims used: `oid` (stable user id), `preferred_username` / `email`,
  `name`, `groups` (object IDs), `tid` (must equal configured tenant).
- `returnTo` must be a relative path starting with `/` and not `//` (open
  redirect protection).

### 5.2 Groups
- Entra App registration must set `groupMembershipClaims = "ApplicationGroup"`
  (only groups assigned to the enterprise app are emitted) or `SecurityGroup`.
- Groups overage (`_claim_names.groups` present, >200 groups): v1 rejects login
  with a clear error page telling the admin to use ApplicationGroup. (Graph API
  fallback is out of scope, see ADR-007.)
- `oidc.allowedGroups` (optional): if set, user must be in at least one, else 403
  page "not authorized for this application".

### 5.3 Sessions
- Server-side sessions in SQLite. Cookie `athens_session` = 32 random bytes
  base64url. Flags: HttpOnly, Secure, SameSite=Lax, Path=/.
- Session stores user id, email, name, groups (JSON), created_at, expires_at,
  last_seen_at. Absolute TTL `auth.sessionTTL` (default 8h), idle TTL
  `auth.idleTTL` (default 1h). Expired sessions purged every 10 min.
- Groups are snapshotted at login; changes in Entra apply at next login.
- `POST /auth/logout` (HTML form post with hidden field `csrf` = session CSRF
  token, plus the Origin check of §5.4) deletes session, clears cookies,
  redirects (303) to Entra `end_session_endpoint` with
  `post_logout_redirect_uri = baseURL`. Without a valid token the session is
  kept and the user is redirected to `/` (ADR-015).
- Cookies are `SameSite=Lax` (not Strict): the OIDC callback is a top-level
  cross-site GET from Entra and must carry `athens_auth`. Response mode is
  `query` (not `form_post`) for the same reason.

### 5.4 CSRF
A random CSRF token is generated per session (stored in `sessions.csrf_token`)
and exposed to the SPA via the non-HttpOnly cookie `athens_csrf`. All non-GET
`/api/*` requests require header `X-Athens-CSRF` equal to the session's token
(constant-time compare) AND `Origin` (or `Referer`) host equal to `baseURL`
host. Otherwise 403 `csrf`. The session cookie value is stored only as
SHA-256 in the DB.

## 6. Data access

### 6.1 Targets
A *target* is one PostgreSQL server endpoint discovered in the cluster (see
DISCOVERY.md). Inside a target, databases are listed live from `pg_database`
(`datallowconn AND NOT datistemplate`), filtered by authz `databases` globs.

Target ID: `<namespace>/<name>` where name is the discovered logical name
(service or cluster name). Both parts are DNS-1123 labels, so the API addresses
a target as two path segments `/targets/{ns}/{name}` (no `%2F`, ADR-012).

### 6.2 Pool manager (`internal/pgconn`)
- Key: `(targetID, database, credentialKind)` where credentialKind ∈ {rw, ro}.
  `ro` exists only if target has a read-only credential (DISCOVERY §5).
- `pgxpool` per key, created lazily, `MaxConns = limits.poolMaxConns` (default 4),
  `MinConns = 0`, `MaxConnIdleTime = 5m`. Pool closed after 15 min unused.
- On auth failure (SQLSTATE 28P01 / 28000): re-resolve credentials, recreate
  pool once, retry the operation once. Then surface error.
- On target removed from discovery: close its pools, cancel its running queries.
- TLS: `sslmode` from target (default `prefer`). `verify-full` supported with
  CA from annotation-referenced Secret (DISCOVERY §4.1).
- `connect_timeout` 5s.

### 6.3 Per-query session setup
Every operation (catalog, grid, editor) runs as:

```
BEGIN [READ ONLY]            -- READ ONLY when effective level == read
SET LOCAL application_name = 'athens:<email>'      -- truncated to 63 bytes
SET LOCAL statement_timeout = '<limits.statementTimeout>'
SET LOCAL lock_timeout = '<limits.lockTimeout>'
SET LOCAL idle_in_transaction_session_timeout = '<limits.statementTimeout*2>'
<statement>
COMMIT | ROLLBACK
```

Use `pgx.TxOptions{AccessMode: pgx.ReadOnly}` for read; values for SET LOCAL
via `set_config($1, $2, true)` with bind params (no string interpolation).

After every SQL editor execution the connection is reset with `DISCARD ALL`
before it returns to the pool (user SQL can change session state even inside a
READ ONLY tx, e.g. `set_config(..., false)`, advisory locks, temp tables). If
the reset fails the connection is destroyed. Pools use
`pgx.QueryExecModeExec` (no server-side prepared statement cache), which is
compatible with `DISCARD ALL` and returns TEXT results (ADR-011).

When effective level is `read` and target has a `ro` credential, use it
(defense in depth). Otherwise `rw` credential + READ ONLY transaction.
Document in UI tooltip: READ ONLY tx is not a sandbox (e.g. functions with
side effects via dblink); a dedicated read-only role is the strong control.

### 6.4 Cancellation
Each running editor query gets a `queryId` (ULID). Registry maps queryId →
(userID, cancel func). `POST /api/v1/queries/{id}/cancel` allowed only for the
owner (or app admin). Cancelling the context makes pgx send a cancel request.
HTTP client disconnect also cancels.

### 6.5 Result shaping
- Rows are requested in TEXT result format for every column (ADR-004).
  Values → JSON string or `null`. Column metadata: name, type OID, type name
  (`format_type`), nullable unknown for editor results.
- Cell truncation: values > `limits.maxCellBytes` (default 64 KiB) are cut and
  flagged `truncated: true`; full value via the cell endpoint (API.md).
- `bytea` shown as returned by Postgres (`\x...` hex), subject to truncation.
- Result row cap: `limits.maxRows` (default 1000 editor, grid page size max 500).
  Stop reading after cap, set `truncated: true`, close rows (no full fetch).

## 7. SQL editor and guardrails

### 7.1 Single statement
v1 executes exactly one statement per request. Parse with `go-pgquery`; if
parse yields ≠ 1 statement → 400 `multiple_statements`. Parse error → 400
`syntax_error` with position (line/col) from parser.

### 7.2 Classification (`internal/sqlclass`)
From the parse tree's top node:

| Class | Statements |
|---|---|
| `read` | SELECT (no INTO, no data-modifying CTE, no FOR UPDATE/SHARE, no side-effect function), EXPLAIN without ANALYZE, SHOW, VALUES, TABLE |
| `dml` | INSERT, UPDATE, DELETE, MERGE, COPY FROM, SELECT INTO, SELECT ... FOR UPDATE, data-modifying CTE, EXPLAIN ANALYZE of dml |
| `ddl` | CREATE/ALTER/DROP/COMMENT/GRANT/REVOKE/TRUNCATE/REINDEX/VACUUM/CLUSTER/REFRESH MATERIALIZED VIEW |
| `session` | SET, RESET, BEGIN, COMMIT, ROLLBACK, SAVEPOINT, LISTEN, NOTIFY, PREPARE, EXECUTE, DEALLOCATE, DISCARD, DECLARE/FETCH/CLOSE cursor, LOCK outside data access, LOAD, etc. → rejected 400 `unsupported_statement` (transactions are managed by athens) |
| `nontx` | statements PostgreSQL refuses inside a transaction block: VACUUM, CREATE/DROP DATABASE, CREATE/DROP TABLESPACE, ALTER SYSTEM, `CREATE INDEX CONCURRENTLY`, `DROP INDEX CONCURRENTLY`, `REINDEX ... CONCURRENTLY`, `ALTER TYPE ... ADD VALUE` on PG < 12 → rejected 400 `unsupported_statement` in v1 (ADR-013) |
| `other` | anything else → treated as `ddl` |

`destructive` flag = DROP *, TRUNCATE, ALTER TABLE ... DROP COLUMN/CONSTRAINT,
DELETE or UPDATE without WHERE, DELETE/UPDATE where WHERE is a constant true.

EXPLAIN: with `ANALYZE` the class (and destructive flag) of the explained
statement applies; without `ANALYZE` it is `read` (nothing executes).

Side-effect functions: a `SELECT`/`VALUES` that calls a function from the
side-effect list is classified `dml` (ADR-014), so `read` users cannot run it.
The list (schema-qualified or not, case-insensitive): `pg_terminate_backend`,
`pg_cancel_backend`, `pg_reload_conf`, `pg_rotate_logfile`, `pg_switch_wal`,
`pg_create_restore_point`, `pg_promote`, `pg_wal_replay_pause`,
`pg_wal_replay_resume`, `pg_create_*_replication_slot`,
`pg_drop_replication_slot`, `pg_replication_origin_*`,
`pg_logical_emit_message`, `pg_file_write`, `pg_file_rename`, `pg_file_unlink`,
`lo_import`, `lo_export`, `lo_unlink`, `lo_create`, `lo_from_bytea`, `lo_put`,
`dblink*`, `set_config`, `pg_advisory_*lock*` (not `*unlock*`), `pg_notify`,
`nextval`, `setval`, `pg_stat_reset*`, `pg_import_system_collations`,
`txid_current`, `pg_current_xact_id`. This is defense in depth, not a sandbox.

### 7.3 Enforcement
- level `read`: class must be `read` (EXPLAIN ANALYZE of SELECT allowed). Else
  403 `forbidden_statement`. Still executed in READ ONLY tx.
- level `write`: any class except `session`. If `destructive` and request lacks
  `confirm: true` → 409 `confirmation_required` with `{class, destructive,
  summary}`; the UI shows a confirm dialog with the statement summary and
  re-submits with `confirm: true`.
- Target flagged `environment: production` (DISCOVERY §4.1): ALL `dml`/`ddl`
  require `confirm: true`, not just destructive.
- Global `readOnly: true` in config forces level `read` for everyone.

### 7.4 History, favorites, drafts
- Every execution (success or error) appended to `query_history` for the user
  (keep last `limits.historyPerUser`, default 500; trim on insert).
- Saved queries (favorites): CRUD, scoped to user, optionally tagged with
  target+database.
- Drafts: one per (user, target, database); autosaved by UI (debounce 1s) via
  PUT; restored when the editor opens.

## 8. Data grid

- Page-based: `limit` (default 100, max 500), `offset`. Sort by one or more
  columns. Filters: list of `{column, op, value}` combined with AND.
  Ops: `eq neq lt lte gt gte like ilike is_null not_null in` (`in` takes array).
  Column names validated against the table's columns (from catalog) before use.
- Comparison values bound as text with the column type OID (ADR-018; was `col = $1::<format_type>`).
- Row count: `reltuples` estimate by default; `?exactCount=true` runs
  `count(*)` with `statement_timeout` = 5s; on timeout return estimate + flag.
- Editing (M7): only tables (relkind `r`/`p`) with a primary key. Views,
  matviews, foreign tables, PK-less tables are read-only in the grid (UI shows
  why).
  - Update: `UPDATE t SET c1=$1::type1,... WHERE pk1=$n::pktype... RETURNING *`;
    must affect exactly 1 row, else ROLLBACK and 409 `row_conflict`.
  - Optimistic check: request includes `original` values of edited columns;
    WHERE also requires `c IS NOT DISTINCT FROM $k::type` for each edited
    column → concurrent modification gives 409 instead of silent overwrite.
  - Insert: only provided columns; omitted → DEFAULT. `RETURNING *`.
  - Delete: by PK, exactly 1 row. Bulk delete: up to 100 PKs in one tx, all
    must match, else rollback.
  - All grid writes are audited with table, pk, changed columns (not values
    unless `audit.logValues=true`).

## 9. Schema explorer (catalog)

Queries against `pg_catalog` only (not information_schema; faster and complete).
- Schemas: exclude `pg_catalog`, `information_schema`, `pg_toast*`, `pg_temp*`
  unless `?system=true`.
- Relations per schema: name, kind (`table, partitioned, view, matview,
  foreign, sequence`), `reltuples` estimate, total size
  (`pg_total_relation_size`, skip for views), comment. Partitions shown under
  their parent, not top level.
- Structure: columns (name, `format_type`, not null, default, identity/generated,
  comment, ordinal), primary key, foreign keys (both directions), unique and
  check constraints (`pg_get_constraintdef`), indexes (`pg_get_indexdef`,
  size, unique/primary, valid), triggers (name, enabled, `pg_get_triggerdef`).
- Search: client-side over loaded schema tree; server endpoint returns all
  relations of a database in one call (cap 20k, flag if capped).
- UI remembers last opened table per (target, database) via `ui_state`.

## 10. Audit

Written to SQLite `audit_log` AND to stdout as JSON (`"log":"audit"`) so it can
be shipped by the cluster log pipeline. Audit write failure in SQLite must not
block the user action but must log an error and increment a metric.

Events: `login`, `login_denied`, `logout`, `query_execute`, `query_cancel`,
`row_insert`, `row_update`, `row_delete`, `access_denied`, `confirm_required`;
reads when `audit.logReads=true` (default): `rows_read` (each grid page; sort,
filter columns and operators, values only with `audit.logValues`),
`cell_read` (column + pk), `structure_view`; the user's stored items:
`saved_query_create|update|delete`, `history_delete`; admin: `admin_view`
(throttled to one per user, page and filter set per 5 min, the pages poll),
`audit_export`. Drafts and UI state are autosaved and not audited (ADR-021).

Fields: ts, event, user_id, email, client_ip (from `X-Forwarded-For` only if
`server.trustedProxies` CIDR matches), user_agent, target_id, database,
object (schema.table), statement_class, destructive, sql (if
`audit.logSQL=true`, default true, truncated 8 KiB), rows_affected,
duration_ms, result (`ok|error|denied|cancelled`), error_code (SQLSTATE).

Retention: `audit.retentionDays` (default 90), purge daily. Admins can export
the filtered log as CSV or JSON Lines (`GET /admin/audit/export`). Shipping the
stdout copy to append-only storage: OPERATIONS.md "Audit trail".

## 11. Observability

Metrics (prefix `athens_`):
- `http_requests_total{route,method,code}`, `http_request_duration_seconds{route}`
- `queries_total{target,class,result}`, `query_duration_seconds{class}`
- `discovery_targets{namespace,source}`, `discovery_errors_total{source,reason}`
- `pool_conns{target,state}` (acquired/idle), `pool_create_total{target,result}`
- `auth_logins_total{result}`, `audit_write_errors_total`
Labels never contain user identity or SQL. `route` is the pattern, not the path.

Logs: slog JSON, level via config. Never log credentials, cookies, tokens, SQL
parameters. Request log: method, route, status, duration, user id.

## 12. HTTP security
- Headers: `Content-Security-Policy: default-src 'self'; img-src 'self' data:;
  style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'`,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`,
  `Strict-Transport-Security` (when not insecureDev).
- Request body limit 1 MiB (SQL editor 256 KiB statement limit).
- Per-user concurrency limit: max `limits.concurrentQueriesPerUser` (default 3)
  running editor queries → 429.
- Error responses never include stack traces; DB errors pass through message,
  SQLSTATE, detail, hint, position (they are the user's own query errors).
  Connection errors are generic ("target unavailable") + logged server-side.

## 13. Frontend

Visual design from `docs/design/`. If a screen is missing in design, follow
existing design language: the Aegean theme (ADR-019, light + dark + system).

Screens / components:
1. **Login redirect** — unauthenticated `/api` → 401; SPA redirects to
   `/auth/login?returnTo=<current>`.
2. **Shell** — left sidebar: brand, connection card (server picker grouped by
   namespace with environment badge, database picker; production gets the
   laurel ring), table search (⌘K), schema tree, user row (theme, admin, sign
   out). Main area: tab strip, per-tab header with `Data | Structure | SQL`.
3. **Schema tree** — schemas → relations grouped by kind, row estimate badge,
   search box filters live; keyboard navigation.
4. **Table tab** — segmented `Data | Structure | SQL` (SQL opens a query tab
   seeded with `select * … limit 100`).
   - Data: virtualized grid (TanStack Table + Virtual), pagination bar, sort by
     header click (shift for multi), filter builder, NULL rendered distinctly,
     truncated cells with "load full value", side panel for row view/edit/insert,
     dirty-cell highlighting, Save/Discard, delete selected with confirm.
     Read-only reason banner when not editable.
   - Structure: columns, keys, indexes, constraints, triggers tables.
5. **SQL tab** — CodeMirror 6, PostgreSQL dialect, autocompletion from loaded
   catalog (schemas, tables, columns), Ctrl/Cmd+Enter run, Esc cancel, running
   timer, result grid (same component, read-only), messages pane
   (command tag, rows affected, duration, notices), error with position marker
   in editor, history drawer, favorites drawer, draft autosave, CSV download of
   current result (client-side).
6. **Confirm dialog** — for 409 `confirmation_required`; shows class,
   destructive flag, statement summary, target + environment badge; requires
   typing the database name when environment is production.
7. **Admin** (app admins only) — audit log table with filters (user, target,
   event, date range), discovery status (targets, source, last error),
   pool status.

State: TanStack Query for server state; minimal local state (zustand allowed)
for tabs/layout. Tabs and layout persisted in localStorage per user email.

## 14. Deployment (Helm chart `deploy/helm/athens`)

- Deployment, `replicas: 1`, `strategy: Recreate` (SQLite).
- PVC 1Gi for `/data` (storageClass configurable).
- Image: distroless static nonroot. `runAsNonRoot`, `readOnlyRootFilesystem`,
  drop ALL caps, `seccompProfile: RuntimeDefault`, `automountServiceAccountToken: true`.
- ServiceAccount + a `ClusterRole` + `ClusterRoleBinding` when
  `discovery.namespaces` is empty (ADR-024), else for each entry a `Role` +
  `RoleBinding`:
  - `apps`: statefulsets, deployments → get, list, watch
  - core: services → get, list, watch (no pods, ADR-016)
  - core: secrets → **get only** (no list/watch; ADR-005)
  - core: configmaps → **get only** (user/db from ConfigMaps; ADR-017)
  - `postgresql.cnpg.io`: clusters → get, list, watch (if `cnpg.enabled`)
  - `acid.zalan.do`: postgresqls → get, list, watch (if `zalando.enabled`)
- ConfigMap for config.yaml, Secret for client secret + session key
  (or existingSecret).
- Service (http 8080, metrics 9090), optional Ingress, optional ServiceMonitor.
- NetworkPolicy (optional, default on): ingress 8080 from ingress controller
  namespace selector, 9090 from monitoring; egress 5432 (configurable ports) to
  the target namespaces (all namespaces when cluster-wide), 443 to apiserver +
  0.0.0.0/0 (Entra), 53 DNS.
- Probes: liveness `/healthz`, readiness `/readyz` (store ok + discovery synced).

## 15. Testing strategy

- Unit: config validation, access annotation evaluation (table-driven), sqlclass
  (≥80 statements fixture file incl. tricky CTEs), discovery sources against
  fake clientset fixtures (one fixture per pattern in DISCOVERY.md §6), grid
  SQL builder (golden files), CSRF/returnTo validation.
- Integration (testcontainers Postgres 16 + 13): catalog queries, grid
  read/write incl. optimistic conflict, editor execution, timeouts, cancel,
  READ ONLY enforcement, text format for tricky types (numeric, int8, jsonb,
  arrays, bytea, timestamptz, interval, uuid, enum, composite, NULLs).
- Auth: mockoidc end-to-end login/callback/logout, groups overage rejection,
  tenant mismatch, nonce/state tampering.
- E2E (`hack/kind`): kind + CNPG cluster + Zalando cluster + bitnami chart +
  plain postgres sts (literal env) + plain sts with `_FILE` secret + annotated
  deployment; assert discovered targets and a query through the API.

## 16. Open items (to confirm with owner; defaults apply until then)
- Group names vs IDs in access annotations for the first deployment
  (Entra group-name claims or `authz.groupAliases`).
- Whether `write` is ever granted on production targets (default: allowed
  but with mandatory confirmation, §7.3).
- Design files to be placed under `docs/design/` (until then: the Aegean
  theme, ADR-019).

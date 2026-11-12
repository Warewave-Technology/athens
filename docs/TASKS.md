# Tasks

Work milestone by milestone. A milestone is done only when every acceptance
criterion passes and tests are green in CI. Tick boxes as you go.
MVP = M0–M6 (read-only grid). M7 adds grid editing. M8 = hardening extras.

---

## M0 — Repo, build, CI
- [x] `go mod init github.com/Warewave-Technology/athens`, Go 1.26, layout from CLAUDE.md
- [x] `internal/version` (name, version, commit, buildDate via ldflags)
- [x] `web/`: Vite + React + TS + pnpm, ESLint, Prettier, TanStack Query, router
- [x] `go:embed web/dist`; `--dev` flag proxies non-API paths to Vite (:5173)
- [x] Makefile targets from CLAUDE.md
- [x] Dockerfile: node build stage → go build stage (`CGO_ENABLED=0`, `-trimpath`) → `gcr.io/distroless/static-debian12:nonroot`
- [x] GitHub Actions: lint, unit tests, integration tests (service docker), build image, trivy scan
- [x] golangci-lint config (errcheck, gosec, govet, staticcheck, revive, sqlclosecheck, bodyclose)

**Accept:** `make build` produces one binary serving a placeholder SPA at `/`
and `/healthz` → 200; image < 40 MB; CI green.

## M1 — Config, store, auth
- [x] `internal/config`: struct per `config.example.yaml`, YAML + env override, validation (SPEC §4) with aggregated error list
- [x] `internal/store`: `Store` interface + SQLite impl, embedded migrations, PRAGMAs, purge jobs (sessions 10m, audit daily)
- [x] `internal/auth`: login, callback (state, nonce, PKCE, tid check, groups overage reject, allowedGroups), sessions (§5.3), logout with end-session redirect, `returnTo` validation
- [x] Middleware: `RequireSession` (401 JSON for /api, 302 for others), CSRF (§5.4), security headers (§12), request logging, panic recovery, body limits
- [x] `GET /api/v1/me`
- [x] Audit writer (`internal/audit`) with `login`, `login_denied`, `logout`
- [x] Frontend: 401 handling → redirect to `/auth/login?returnTo=`, user menu with logout

**Accept:** with mockoidc: full login → `/me` works; tampered state/nonce →
rejected; wrong tenant → rejected; overage claim → error page; expired/idle
session → 401; POST without CSRF header or wrong Origin → 403; open redirect
attempts (`//evil.com`, `https://evil`) fall back to `/`. Unit test coverage
for auth ≥ 80%.

## M2 — Discovery
- [x] Types and Registry (DISCOVERY §1), change event channel
- [x] Namespaced informer factories; CRD presence check + late start (§2)
- [x] Sources: annotation, cnpg, zalando, generic (§4), service matching (§4.5), priority/dedup/opt-out (§3)
- [x] `internal/credentials.Resolve` (secret GET, literal env from informer cache, `_FILE` mapping)
- [x] All 13 fixtures (+ annotated-service) + golden JSON (§6); golden must not contain secret values (test asserts)
- [x] `/readyz` waits for cache sync
- [x] Metrics: `discovery_targets`, `discovery_errors_total`
- [x] `GET /api/v1/admin/discovery`

**Accept:** all fixtures produce expected targets; adding/removing an annotated
sts in a live kind cluster updates the registry within 2s; RBAC with
secrets `get` only is sufficient (e2e runs with chart-generated Roles).

## M3 — Authz, pools, catalog
- [x] `internal/authz`: `Effective(user, target, db) Level` (none/read/write), glob matching, admin check, global readOnly; table-driven tests incl. overlapping rules
- [x] `internal/pgconn`: pool manager (§6.2), per-query tx wrapper (§6.3) as `Run(ctx, key, level, fn)`, auth-failure re-resolve + retry once, close on target removal, pool metrics
- [x] `internal/catalog`: schemas, relations (incl. partitions under parent, editable flag), structure (§9) — pg_catalog only, tested on PG 13 and 16
- [x] Endpoints: `/targets`, `/targets/{tid}/databases`, catalog endpoints (API.md)
- [x] Target `status` via cheap periodic check (`SELECT 1` every 60s only for targets with an open pool; else `unknown`)
- [x] Frontend: shell layout, target+database selectors (grouped by namespace, env badge), schema tree with search, Structure tab

**Accept:** user without matching rule gets 404 for target and it is absent
from `/targets`; `application_name` visible in `pg_stat_activity` as
`athens:<email>`; read-level user cannot write even via catalog path (READ ONLY
tx verified by integration test); rotated secret password → next request
succeeds after one retry.

## M4 — Data grid (read)
- [x] `internal/rows`: query builder for filters/sort/pagination (SPEC §8), identifier sanitizing, column validation, text-cast binds; golden SQL tests
- [x] TEXT result format for all columns (ADR-004), cell truncation, cell endpoint
- [x] Row count estimate / exact with 5s timeout
- [x] Frontend grid component (shared with SQL results): virtualization, NULL rendering, truncated-cell loader, sort, filter builder, pagination, row side panel (view only), column resize, copy cell/row
- [x] Remember last opened table per (target, db) via ui_state

**Accept:** 1M-row table pages in < 300ms (p95, local); injection attempts in
column names / filter values are rejected or safely bound (tests); all tricky
types (SPEC §15) round-trip as text; 100 columns × 500 rows renders smoothly.

## M5 — SQL editor
- [x] `internal/sqlclass` with ≥ 80 fixture statements (SPEC §7.2), destructive detection
- [x] `internal/query`: execute single statement, enforcement (§7.3), confirmation 409, production rule, notices capture, result cap, command tag, rowsAffected
- [x] Cancel registry + endpoint, client disconnect cancel, per-user concurrency limit (429)
- [x] History (trim), saved queries CRUD, drafts, ui-state endpoints
- [x] Audit `query_execute`, `query_cancel`, `access_denied`, `confirm_required`
- [x] Frontend: CodeMirror 6 + PG dialect + catalog autocompletion, run/cancel shortcuts, timer, results + messages panes, error position marker, history & favorites drawers, draft autosave, confirm dialog (typed db name for production), CSV export of result

**Accept:** `DROP TABLE` without confirm → 409, with confirm → executes and
audited; read user running `UPDATE` → 403 and audited; `SELECT pg_sleep(60)`
cancels within 1s via button; statement_timeout → 504; two statements → 400;
`BEGIN` → 400; history shows last 500 only.

## M6 — Audit UI, metrics, Helm, hardening (MVP complete)
- [x] Admin pages: audit log (filters, paging), discovery status, pool status
- [x] All metrics from SPEC §11; ServiceMonitor template
- [x] Helm chart per SPEC §14 incl. per-namespace Roles, NetworkPolicy, PSS restricted compliant, values schema (`values.schema.json`)
- [x] `hack/kind`: script to create cluster, install CNPG + Zalando operators, bitnami chart, plain sts fixtures, mockoidc, athens chart; `make e2e` runs API-level e2e
- [x] Docs: `README.md` (install, Entra app registration steps incl. redirect URI, `groupMembershipClaims`, client secret, group assignment), `docs/OPERATIONS.md` (backup of PVC, upgrades, rotating secrets, troubleshooting)
- [x] Security review checklist: no secrets in logs (grep test on log output in e2e), headers present, CSRF on all mutations (route table test), authz on all target routes (route table test)

**Accept:** `make e2e` green from scratch; `helm lint` + `kubeconform` pass;
pod runs under PSS `restricted`; trivy no HIGH/CRITICAL.

## M7 — Grid editing
- [x] Insert / update (optimistic) / delete (single + bulk ≤100) per SPEC §8 & API.md
- [x] Production targets: grid writes require confirm dialog
- [x] Audit `row_insert/update/delete` with pk + changed columns
- [x] Frontend: inline edit, dirty tracking, save/discard, side panel edit/insert with per-type input hints (json textarea, boolean toggle, NULL toggle, default toggle), conflict UX (reload row)

**Accept:** concurrent edit in psql between load and save → 409 and UI
offers reload; PK-less table not editable with reason shown; read user gets
403 on all write endpoints.

## M8 — Extras (after MVP feedback)
- [ ] Entra Workload Identity (client assertion, no client secret) for AKS
- [ ] Read-only credential preference + fallback warnings surfaced in UI
- [ ] Optional Postgres store backend (enables replicas > 1)
- [ ] Graph API group overage fallback (needs ADR)
- [ ] Multi-statement scripts (needs ADR)

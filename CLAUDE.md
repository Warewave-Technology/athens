# CLAUDE.md — athens

In-cluster PostgreSQL web client (pg-view-like) with Microsoft Entra ID SSO and
automatic discovery of PostgreSQL instances in target Kubernetes namespaces.
Product name: `athens`. Module `github.com/Warewave-Technology/athens`, image
`ghcr.io/warewave-technology/athens`. Keep names centralized in `internal/version`.

## Read first
- `docs/SPEC.md`      — scope, architecture, security model (source of truth)
- `docs/DISCOVERY.md` — exact discovery + credential resolution rules
- `docs/API.md`       — REST contract between backend and frontend
- `docs/TASKS.md`     — milestones with acceptance criteria; work strictly in order
- `docs/DECISIONS.md` — decisions already made; do not relitigate, append new ADRs
- `docs/design/`      — UI design files (visual source of truth for the frontend)

## Stack (fixed)
- Go 1.26+ (minimum required by client-go, sqlite, pgquery), single static binary, `CGO_ENABLED=0`
- HTTP: stdlib `net/http` (Go 1.22 routing patterns), no web framework
- Postgres driver: `github.com/jackc/pgx/v5` (+ `pgxpool`)
- K8s: `k8s.io/client-go` informers, `dynamic` client for CRDs
- OIDC: `github.com/coreos/go-oidc/v3` + `golang.org/x/oauth2`
- SQL classification: `github.com/wasilibs/go-pgquery` (pure Go, no cgo)
- App store: SQLite via `modernc.org/sqlite`, embedded SQL migrations
- Logging: `log/slog` JSON; metrics: `prometheus/client_golang`
- Frontend: React + TypeScript + Vite, TanStack Query/Table/Virtual,
  CodeMirror 6 (`@codemirror/lang-sql`, PostgreSQL dialect), embedded via `go:embed`
- Tests: stdlib `testing`, `testcontainers-go` (postgres), `fake` clientset,
  `github.com/oauth2-proxy/mockoidc`

## Layout
```
cmd/athens/            main: config load, wiring, graceful shutdown
internal/config/       config struct, YAML + env override, validation
internal/auth/         OIDC login/callback, sessions, CSRF, middleware
internal/authz/        level from access/readonly annotations (groups -> target -> level)
internal/discovery/    sources (annotation, cnpg, zalando, generic), registry
internal/credentials/  secret resolution at connect time (never cached on disk)
internal/pgconn/       pool manager, per-query session setup, cancel registry
internal/sqlclass/     statement classification (read/dml/ddl/destructive)
internal/catalog/      schema/table/structure queries against pg_catalog
internal/rows/         grid read/insert/update/delete query builders
internal/query/        SQL editor execution, result shaping
internal/store/        SQLite store + migrations (users, sessions, history, ...)
internal/audit/        audit writer (store + stdout JSON)
internal/httpapi/      handlers, routing, error model, security headers
internal/metrics/      prometheus collectors
internal/version/      name, version, build info
web/                   React app (Vite); build output web/dist embedded
deploy/helm/athens/    Helm chart
hack/kind/             kind e2e env: CNPG, Zalando, bitnami, plain sts fixtures
```

## Hard rules
1. Credentials NEVER leave the backend: not in API responses, logs, audit, errors,
   metrics labels, or the SQLite store. Resolve from Secrets at pool creation.
2. Authorization is enforced server-side on every request. The UI hiding a button
   is not a control.
3. All identifiers in generated SQL go through `pgx.Identifier{...}.Sanitize()`.
   All values go through bind parameters. No string concatenation of values. Ever.
4. Every query runs inside a transaction that first does `SET LOCAL
   application_name`, `statement_timeout`, `lock_timeout` (see SPEC §6.3).
5. Read access => transaction opened with `AccessMode: pgx.ReadOnly`.
6. Destructive statements require `confirm: true` from the client; enforced in
   backend (409 otherwise), see SPEC §7.
7. Every mutating action and every SQL editor execution writes an audit record.
8. No cgo. No new heavy dependencies without an ADR in DECISIONS.md.
9. Results are fetched in Postgres TEXT format (see DECISIONS ADR-004).

## Commands (create these in M0)
- `make dev`        backend with `--dev` (vite proxy) + `pnpm dev` in web/
- `make build`      web build + go build (embedded)
- `make test`       go unit tests
- `make test-int`   integration tests (testcontainers, needs Docker)
- `make e2e`        kind cluster + fixtures + e2e suite
- `make lint`       golangci-lint + eslint + tsc --noEmit
- `make image`      distroless image

## Working style
- Follow `docs/TASKS.md` milestone order. Finish acceptance criteria + tests
  before moving on. Tick checkboxes in TASKS.md as you go.
- Small commits, one concern each. Conventional commit prefixes.
- When a spec is ambiguous: choose the safer option, record it as an ADR.
- Keep this file under ~200 lines; put detail in docs/.

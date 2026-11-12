# REST API v1

Base: `/api/v1`. JSON in/out, UTF-8. Auth: session cookie. Mutations need CSRF
header (SPEC §5.4). A target is addressed by two path segments `{ns}/{name}`
(written `{tid}` below, e.g. `/targets/payments/orders-db/...`; ADR-012).
`{db}`, `{schema}`, `{rel}` are URL-encoded identifiers.

## Error model
```json
{ "error": { "code": "forbidden_statement", "message": "human text",
             "details": { } } }
```
| HTTP | code | when |
|---|---|---|
| 400 | `bad_request`, `syntax_error`, `multiple_statements`, `unsupported_statement`, `invalid_filter` | |
| 401 | `unauthenticated` | no/expired session → SPA redirects to login |
| 403 | `csrf`, `forbidden`, `forbidden_statement`, `not_editable` | |
| 404 | `not_found` | also used for targets/dbs the user can't see |
| 409 | `confirmation_required`, `row_conflict` | |
| 413 | `too_large` | |
| 422 | `db_error` | Postgres error from user's own statement: `details = {sqlstate, message, detail, hint, position, line, column}` |
| 429 | `too_many_queries` | |
| 502 | `target_unavailable` | connect/auth failure (generic message) |
| 504 | `timeout` | statement_timeout hit (SQLSTATE 57014 not from user cancel) |

## Auth (outside /api)
- `GET  /auth/login?returnTo=/x` → 302 Entra
- `GET  /auth/callback?code&state` → 302 returnTo
- `POST /auth/logout` (form field `csrf`) → 303 Entra end session
- `GET  /api/v1/me` → `{ "id","email","name","isAdmin":bool, "readOnlyGlobal":bool }`

## Targets
`GET /targets`
```json
{ "targets": [ { "id":"payments/orders-db", "namespace":"payments",
  "name":"orders-db", "displayName":"Orders", "source":"cnpg",
  "environment":"production", "level":"read|write",
  "status":"ok|unreachable|unknown", "warnings":["..."] } ] }
```
(`warnings` only for admins.)

`GET /targets/{tid}/databases` → `{ "databases":[{"name":"app","level":"write","sizeBytes":123}] }`

## Catalog
- `GET /targets/{tid}/db/{db}/schemas?system=false` → `{ "schemas":[{"name","owner","comment"}] }`
- `GET /targets/{tid}/db/{db}/relations` (all schemas, one call)
  ```json
  { "relations":[{"schema":"public","name":"orders","kind":"table",
     "rowEstimate":12000,"sizeBytes":4096000,"comment":null,
     "parent":null,"editable":true,"editableReason":null}],
    "capped": false }
  ```
  `kind`: `table|partitioned|view|matview|foreign|sequence`.
  `editable` false reasons: `no_primary_key|not_a_table|read_only_access`.
- `GET /targets/{tid}/db/{db}/relations/{schema}/{rel}/structure`
  ```json
  { "columns":[{"name","type","typeOid","notNull","default","identity",
                "generated","comment","position"}],
    "primaryKey":{"name","columns":[...]},
    "foreignKeys":[{"name","columns","refSchema","refTable","refColumns","onUpdate","onDelete","direction":"out|in"}],
    "uniques":[{"name","columns"}], "checks":[{"name","definition"}],
    "indexes":[{"name","definition","unique","primary","valid","sizeBytes"}],
    "triggers":[{"name","enabled","definition"}] }
  ```

## Grid rows
`POST /targets/{tid}/db/{db}/relations/{schema}/{rel}/rows/query`
(POST because filters are structured; it is read-only and still needs CSRF)
```json
// request
{ "limit":100, "offset":0,
  "sort":[{"column":"created_at","dir":"desc"}],
  "filters":[{"column":"status","op":"in","value":["new","paid"]},
             {"column":"deleted_at","op":"is_null"}],
  "exactCount": false }
// response
{ "columns":[{"name","type","typeOid"}],
  "rows":[["1","new",null]],           // TEXT values or null
  "truncatedCells":[[0,2]],            // [rowIdx,colIdx] cut at maxCellBytes
  "total": 12000, "totalExact": false, "durationMs": 12 }
```

`GET .../rows/cell?column=payload&pk=<base64url(JSON pk object)>` →
`{ "value": "..." }` full value (max 16 MiB).

`POST .../rows` (insert) `{ "values": {"col":"text-or-null"} }` →
`{ "row": [...] }` (RETURNING *)

`PATCH .../rows`
```json
{ "pk": {"id":"42"}, "set": {"status":"paid"}, "original": {"status":"new"} }
```
→ `{ "row":[...] }` | 409 `row_conflict`

`DELETE .../rows` `{ "pks": [{"id":"42"},{"id":"43"}] }` (≤100) →
`{ "deleted": 2 }` | 409 `row_conflict`

All values in requests are strings (Postgres text input syntax) or null; server
casts with `$n::<format_type>`.

## SQL editor
`POST /targets/{tid}/db/{db}/query`
```json
// request
{ "sql":"select * from orders where id = 1", "limit":1000, "confirm":false }
// response 200
{ "queryId":"01J...", "class":"read", "destructive":false,
  "columns":[{"name","typeOid","type"}], "rows":[[...]],
  "truncated":false, "truncatedCells":[], "rowsAffected":null,
  "commandTag":"SELECT 1", "notices":[{"severity","message"}],
  "durationMs": 8 }
// 409
{ "error":{"code":"confirmation_required","message":"...",
  "details":{"class":"ddl","destructive":true,"summary":"DROP TABLE public.orders",
             "environment":"production","requireTypedConfirmation":true}}}
```
Request starts → `queryId` must be known by the client for cancel before the
response arrives: client generates `clientQueryId` (UUID) in the request;
server uses it as `queryId` if valid and unique for the user.

`POST /queries/{queryId}/cancel` → 204

## History / favorites / drafts / ui state
- `GET /history?targetId&database&limit=50&before=<id>` → `{ "items":[{"id","targetId","database","sql","class","result","durationMs","rows","error","createdAt"}] }`
- `DELETE /history/{id}`
- `GET /saved-queries` · `POST /saved-queries {name,sql,targetId?,database?}` · `PUT /saved-queries/{id}` · `DELETE /saved-queries/{id}`
- `GET /drafts/{tid}/{db}` → `{ "sql", "updatedAt" }` · `PUT /drafts/{tid}/{db} {sql}`
- `GET /ui-state/{key}` · `PUT /ui-state/{key} {value}` (value ≤ 16 KiB JSON)

## Admin (isAdmin only, else 404)
- `GET /admin/audit?user&targetId&event&from&to&limit&before`
- `GET /admin/discovery` → targets with source, owner, warnings, last rebuild, errors
- `GET /admin/pools` → per key: total/idle/acquired conns, created, last error

## Health
- `GET /healthz` → 200 if process alive
- `GET /readyz` → 200 if store ok, OIDC provider initialized, discovery synced
- `GET :9090/metrics`

# Access control

Who can sign in, which databases each person sees, and what they may do
there. Everything on this page is enforced by the server on every request;
the UI only reflects it.

- [Sign-in](#sign-in)
- [Levels](#levels)
- [Rules in the configuration](#rules-in-the-configuration)
- [Annotations on the database](#annotations-on-the-database)
- [How the level is decided](#how-the-level-is-decided)
- [Worked examples](#worked-examples)
- [Guardrails for writers](#guardrails-for-writers)
- [Administrators](#administrators)
- [Emergency read-only switch](#emergency-read-only-switch)

## Sign-in

Users sign in with Microsoft Entra ID only (OpenID Connect, authorization code
with PKCE); there are no local accounts. athens reads the user's group object
IDs from the `groups` claim of the ID token at sign-in and keeps them in the
session.

- `oidc.allowedGroups` (optional): only members of one of these groups may
  sign in at all. Everyone else gets "not authorized for this application".
- Configure the app registration to emit **groups assigned to the
  application** (`groupMembershipClaims: ApplicationGroup`). A user in more
  than 200 groups otherwise gets an overage claim, and athens refuses the
  sign-in (ADR-007).
- Group changes take effect at the next sign-in. Sessions last
  `auth.sessionTTL` (8 h) and end after `auth.idleTTL` (1 h) without
  activity.

## Levels

| Level | The user can |
|---|---|
| none | not see the database at all (the API answers 404, not 403, so names don't leak) |
| `read` | browse schemas, structure and rows, download CSV, run read-only SQL. Every query runs in a `READ ONLY` transaction, with the database's read-only login when one is annotated. |
| `write` | everything above, plus edit, insert and delete rows in the grid and run DML/DDL in the SQL editor, with confirmations (see [Guardrails](#guardrails-for-writers)) |

## Rules in the configuration

`authz.rules` map Entra groups to databases. A rule matches when the user is
in one of its `groups` and the database matches its `namespaces`, `targets`
(the workload or cluster name) and `databases` globs. Omitted lists mean
`*`, but each rule needs at least one of the three.

```yaml
authz:
  rules:
    - name: dba
      groups: ["7c1f0f6e-1111-4aaa-9bbb-000000000001"]   # DBA team
      namespaces: ["*"]
      level: write
    - name: payments-read
      groups: ["7c1f0f6e-1111-4aaa-9bbb-000000000002"]   # payments developers
      namespaces: ["payments", "payments-staging"]
      level: read
    - name: payments-write-staging
      groups: ["7c1f0f6e-1111-4aaa-9bbb-000000000002"]
      namespaces: ["payments-staging"]
      level: write
    - name: bi-reporting
      groups: ["7c1f0f6e-1111-4aaa-9bbb-000000000003"]   # BI analysts
      namespaces: ["orders"]
      targets: ["orders-*"]
      databases: ["reporting", "app_ro*"]
      level: read
```

When several rules match, the highest level wins. Globs use Go's
`path.Match` syntax (`*`, `?`, `[a-z]`).

## Annotations on the database

The team that owns a database can override the rules for it with two
annotations on its StatefulSet, Deployment, Service, CNPG `Cluster` or
Zalando `postgresql` (ADR-022, recipes in [DATABASES.md](DATABASES.md#who-may-use-a-database)):

```yaml
athens.warewave.io/readonly: "true"
athens.warewave.io/access: "system-admins:rw,developers:ro"
```

- `readonly: "true"` caps every user at `read` on this database.
- `access` lists the groups that may use it, each with `rw` or `ro`. When
  present, it **replaces** the rules for this database: groups that are not
  listed lose access, even if a rule would grant it.

Groups in `access` are Entra group object IDs, or names that the athens
operators map to IDs:

```yaml
authz:
  groupAliases:
    system-admins: "7c1f0f6e-1111-4aaa-9bbb-000000000001"
    developers: "7c1f0f6e-1111-4aaa-9bbb-000000000002"
```

**Trust.** Anyone who can annotate workloads in a watched namespace can grant
access to that namespace's databases through athens. Usually the same people
can already read those databases' Secrets. If that does not hold in your
cluster, set `authz.annotationAccess: false`. athens then ignores `access`
(Admin → Discovery shows it as ignored) but still honours `readonly`, which
can only remove rights.

## How the level is decided

For a user, a database (target) and a database name:

1. If the target has an `access` annotation and `authz.annotationAccess` is
   on, the level is the highest one its entries give the user's groups (none
   if no entry matches). Otherwise it is the highest level of the matching
   `authz.rules`.
2. If the target has `readonly: "true"`, `write` becomes `read`.
3. If `authz.readOnly` is on, `write` becomes `read`.

Admin groups get no data access from being admins (see
[Administrators](#administrators)).

## Worked examples

The configuration from [Rules in the configuration](#rules-in-the-configuration),
with the aliases above. "DBA", "payments dev" and "BI" are members of the
first, second and third group.

| Database (namespace/name) | Annotations | DBA | payments dev | BI |
|---|---|---|---|---|
| `payments/ledger` | — | write | read | none |
| `payments-staging/ledger` | — | write | write | none |
| `orders/orders-db`, database `reporting` | — | write | none | read |
| `orders/orders-db`, database `app` | — | write | none | none |
| `payments/ledger` | `readonly: "true"` | read | read | none |
| `payments/ledger` | `access: "developers:rw"` | **none** | write | none |
| `payments/ledger` | `access: "system-admins:rw,developers:ro"` | write | read | none |
| `payments/ledger` | both of the above + `readonly: "true"` | read | read | none |
| `payments/ledger` | `access: ""` (or only invalid entries) | none | none | none |

The DBA row in the sixth line is the one that surprises people: `access`
replaces the rules, so add your DBA group to it when they need access.

## Guardrails for writers

Write access is not unlimited. For every SQL editor statement:
- **One statement per run.** Several statements return
  `multiple_statements`. Run a selection to execute just part of the editor.
- **Transactions are managed by athens.** `BEGIN`/`COMMIT`, `SET`, `LISTEN`
  and similar session statements are refused, and so are statements that
  cannot run in a transaction (`VACUUM`, `CREATE INDEX CONCURRENTLY`,
  `CREATE DATABASE`, ...).
- **Destructive statements need confirmation:** `DROP`, `TRUNCATE`,
  `ALTER TABLE ... DROP COLUMN/CONSTRAINT`, and `DELETE`/`UPDATE` without a
  `WHERE` clause (or with `WHERE true`).
- **Production databases** (`environment: production`) need confirmation for
  every data or schema change, and the user types the database name to
  confirm.
- **Timeouts.** Every statement runs with `statement_timeout` (30 s) and
  `lock_timeout` (5 s), and with `application_name` set to
  `athens:<user email>`, so DBAs can see who runs what in
  `pg_stat_activity`.
- **Read users** may only run reads. Functions with side effects
  (`pg_terminate_backend`, `set_config`, `nextval`, `dblink`, ...) count as
  writes, and the `READ ONLY` transaction stops anything that slips through.

Grid edits are guarded too:
- Rows are matched by primary key, and the original value of every edited
  column, so a row that changed in the meantime is reported as a conflict
  instead of being overwritten.
- Tables without a primary key, views and foreign tables cannot be edited in
  the grid.
- Every write is recorded in the audit log.

## Administrators

Members of `authz.adminGroups` get the **Administration** pages:
- **Discovery:** every target, where it came from, its access source and
  warnings.
- **Pools:** open connection pools per database.
- **Audit:** search and export the audit log.

Being an admin does not grant data access: admins see a database's rows only
if a rule or an `access` annotation gives them access. Non-admins get 404 from
the admin API.

## Emergency read-only switch

```yaml
authz:
  readOnly: true
```

or, without editing the config:

```bash
kubectl -n athens set env deploy/athens ATHENS_AUTHZ_READONLY=true
```

Everyone is capped at `read` until the switch is turned off. The UI shows a
"Read-only mode" notice.

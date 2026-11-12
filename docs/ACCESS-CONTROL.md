# Access control

Who can sign in, which databases each person sees, and what they may do
there. Everything on this page is enforced by the server on every request;
the UI only reflects it.

The short version: **access lives on the database, not in the athens
configuration.** The team that runs a database lists the Entra groups that
may use it in an annotation. A database without that annotation is visible
to nobody.

- [Sign-in](#sign-in)
- [Levels](#levels)
- [The access annotation](#the-access-annotation)
- [Read-only databases](#read-only-databases)
- [Group names or IDs](#group-names-or-ids)
- [How the level is decided](#how-the-level-is-decided)
- [Worked examples](#worked-examples)
- [Guardrails for writers](#guardrails-for-writers)
- [Administrators](#administrators)
- [Emergency read-only switch](#emergency-read-only-switch)
- [Trust model](#trust-model)

## Sign-in

Users sign in with Microsoft Entra ID only (OpenID Connect, authorization code
with PKCE); there are no local accounts. athens reads the user's groups from
the `groups` claim of the ID token at sign-in and keeps them in the session.

- **Which groups the token carries** is set by the app registration's groups
  claim, and it depends on the Entra licence:
  - *Entra ID P1/P2:* emit **groups assigned to the application** and assign
    the groups to the enterprise application. Only those groups reach
    athens, so users in hundreds of groups are fine.
  - *Entra ID Free:* groups cannot be assigned to an application, and
    "groups assigned to the application" would send an empty claim (nobody
    could use a database). Emit **security groups** instead: the token then
    lists every security group the user belongs to, nested ones included.
    A user in more than 200 groups gets an overage claim instead of a list,
    and athens refuses that sign-in (ADR-007).
- **Who may sign in:** with P1/P2, *Assignment required = Yes* on the
  enterprise application. With Free, or in addition, `oidc.allowedGroups`:
  only members of one of these groups may sign in; everyone else gets "not
  authorized for this application". Without either, every user of the
  tenant can sign in, and sees nothing unless an `access` annotation names
  one of their groups.
- Group changes take effect at the next sign-in. Sessions last
  `auth.sessionTTL` (8 h) and end after `auth.idleTTL` (1 h) without
  activity.

## Levels

| Level | The user can |
|---|---|
| none | not see the database at all (the API answers 404, not 403, so names don't leak) |
| `ro` / read | browse schemas, structure and rows, download CSV, run read-only SQL. Every query runs in a `READ ONLY` transaction, with the database's read-only login when one is annotated. |
| `rw` / write | everything above, plus edit, insert and delete rows in the grid and run DML/DDL in the SQL editor, with confirmations (see [Guardrails](#guardrails-for-writers)) |

## The access annotation

Put the annotation on the object that carries `athens.warewave.io/enabled`
(StatefulSet, Deployment, Service, CNPG `Cluster` or Zalando `postgresql`):

```yaml
metadata:
  annotations:
    athens.warewave.io/enabled: "true"
    athens.warewave.io/access: "dba:rw, payments-dev:ro"
```

```bash
kubectl -n payments annotate statefulset ledger 'athens.warewave.io/access=dba:rw,payments-dev:ro'
```

- Each entry is `group:level`. Levels are `rw` (or `write`) and `ro` (or
  `read`).
- Entries are separated by commas, semicolons or spaces.
- A group listed twice gets the higher level.
- The level applies to every database on that server.
- **No annotation means no access:** the database is discovered, shown to
  admins with a "no access annotation" warning, and hidden from everyone
  else.
- **Mistakes make access narrower, never wider:**
  - an invalid entry (`qa:admin`, `nogroup`) is ignored and shown as a
    warning on Admin → Discovery;
  - an annotation that ends up granting nobody hides the database.

Changes apply within about a second, with no restart.

## Read-only databases

```yaml
athens.warewave.io/readonly: "true"
```

caps everyone on the access list at `ro`, whatever their entry says. Any
value other than `true` or `false` counts as `true`. This is the switch for a
production database that people should be able to look at but never change
through athens.

## Group names or IDs

Entra ID puts group **object IDs** in tokens, so the simplest annotation uses
IDs (Entra admin center → Groups → the group → Overview → Object ID):

```yaml
athens.warewave.io/access: "7c1f0f6e-1111-4aaa-9bbb-000000000001:rw"
```

For readable annotations, either:

1. **Map names to IDs** in the athens configuration. This works with every
   licence and groups claim:

   ```yaml
   authz:
     groupAliases:
       dba: "7c1f0f6e-1111-4aaa-9bbb-000000000001"
       payments-dev: "7c1f0f6e-1111-4aaa-9bbb-000000000002"
   ```

   An entry matches when the user has the aliased ID or a group literally
   named like the entry.
2. **Or let Entra send group names.** In the app registration's **Token
   configuration**, edit the groups claim:
   - **Emit group name for cloud-only groups** sends display names, but only
     together with "Groups assigned to the application" (Entra ID P1/P2).
   - `sAMAccountName` sends names for groups synced from on-premises AD,
     with any groups claim.

   The names then work directly in annotations, `authz.adminGroups` and
   `oidc.allowedGroups`. With "Security groups" on a Free tenant, cloud-only
   groups stay IDs, so use aliases.

## How the level is decided

For a user and a database server (target):

1. No `access` annotation → **none**.
2. Otherwise the highest level among the entries that name one of the user's
   groups (directly or through `groupAliases`). If no entry matches →
   **none**.
3. `readonly: "true"` on the target turns `rw` into `ro`.
4. `authz.readOnly: true` in the configuration turns `rw` into `ro` for
   everyone.

Admin groups get no data access from being admins (see
[Administrators](#administrators)).

## Worked examples

"DBA", "payments dev" and "BI" are members of the groups `dba`,
`payments-dev` and `bi`.

| Database | Annotations | DBA | payments dev | BI |
|---|---|---|---|---|
| `payments/ledger` | — | none | none | none |
| `payments/ledger` | `access: "dba:rw, payments-dev:ro"` | rw | ro | none |
| `payments-staging/ledger` | `access: "dba:rw, payments-dev:rw"` | rw | rw | none |
| `payments/ledger` | `access: "dba:rw, payments-dev:ro"` + `readonly: "true"` | ro | ro | none |
| `orders/reporting` | `access: "bi:ro, dba:rw"` | rw | none | ro |
| `orders/reporting` | `access: "bi:ro, bi:rw"` | none | none | rw |
| `orders/reporting` | `access: ""` (or only invalid entries) | none | none | none |

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

Members of `authz.adminGroups` (the one access setting in the configuration)
get the **Administration** pages:
- **Discovery:** every annotated database, where it came from, its access
  list, warnings and problems. This is where a database without an `access`
  annotation shows up.
- **Pools:** open connection pools per database.
- **Audit:** search and export the audit log.

Being an admin does not grant data access: admins see a database's rows only
if its `access` annotation lists one of their groups. Non-admins get 404 from
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

Everyone is capped at `ro` until the switch is turned off. The UI shows a
"Read-only mode" notice. A later `helm upgrade` resets environment changes
made with `kubectl set env`; use `extraEnv` in your values to keep it.

## Trust model

Whoever can annotate a workload or operator resource can decide who uses its
database through athens. In a typical cluster, those are the people who
already run the database and can read its Secrets. If your RBAC is different
(for example, developers may edit Deployments but must not read production
Secrets), restrict who may change these annotations, for example with a
validating admission policy on `athens.warewave.io/access`, or limit athens
to trusted namespaces with `discovery.namespaces`. athens itself only `get`s
Secrets that an annotated object references in its own namespace (ADR-023,
ADR-024).

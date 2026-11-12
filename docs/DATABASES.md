# Adding databases to athens

This guide is for the teams that run PostgreSQL in Kubernetes. It shows how
to make a database appear in athens, with a recipe for each common setup and
the annotations that control what users can do.

The exact discovery rules are in [DISCOVERY.md](DISCOVERY.md). Every manifest
in [`examples/`](examples/) is checked by a test
(`internal/discovery/docs_examples_test.go`), so it works as written.

- [How discovery works](#how-discovery-works)
- [Recipes](#recipes)
  - [Plain StatefulSet (official image)](#plain-statefulset-official-image)
  - [Production database with an explicit login](#production-database-with-an-explicit-login)
  - [Bitnami Helm chart](#bitnami-helm-chart)
  - [CloudNativePG](#cloudnativepg)
  - [Zalando postgres-operator](#zalando-postgres-operator)
  - [Database outside the cluster](#database-outside-the-cluster)
- [Read-only access inside PostgreSQL](#read-only-access-inside-postgresql)
- [Who may use a database](#who-may-use-a-database)
- [Annotation reference](#annotation-reference)
- [Checking the result](#checking-the-result)
- [Troubleshooting](#troubleshooting)

## How discovery works

athens watches StatefulSets, Deployments, Services, CloudNativePG `Cluster`s
and Zalando `postgresql`s across the cluster, or only in the namespaces in
`discovery.namespaces` when the operators limit it. Two annotations put a
database in athens:

```yaml
athens.warewave.io/enabled: "true"                    # show this database
athens.warewave.io/access: "dba:rw, payments-dev:ro"  # who may use it, and how
```

Groups with `ro` also need a read-only login, because read users never use
the main login. One more line lets athens create it:

```yaml
athens.warewave.io/readonly-provision: "true"         # athens manages the read-only login
```

or point `readonly-secret` at a login of your own (see
[Read-only access inside PostgreSQL](#read-only-access-inside-postgresql)).
Without either, only the `rw` groups can use the database.

Nothing has to change in the athens configuration. Without `enabled` athens
ignores the object. Without `access` the database appears only to athens
admins, with a warning, and nobody can open it (see
[Who may use a database](#who-may-use-a-database)).

athens finds the rest by itself:

| You annotate | athens connects to | Credentials come from |
|---|---|---|
| StatefulSet / Deployment | the Service whose selector matches the pods | the postgres container env (`POSTGRES_PASSWORD`, `POSTGRESQL_PASSWORD`, `*_FILE`, `envFrom`, ...) or the `secret` annotation |
| Service | that Service | the `secret` annotation (required) |
| CNPG `Cluster` | `<cluster>-rw` | the `<cluster>-app` Secret |
| Zalando `postgresql` | `<cluster>` | the operator's `<user>.<cluster>.credentials...` Secret |

Changes are picked up within about a second: annotate, refresh athens, the
database is there. Removing the annotation (or setting it to `"false"`)
removes it.

athens reads Secret values only when it opens a connection pool and keeps them
in memory. They never appear in the UI, the API, logs or the audit trail.

## Recipes

### Plain StatefulSet (official image)

[`examples/statefulset-minimal.yaml`](examples/statefulset-minimal.yaml): the
only athens-specific lines are the annotations.

```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: orders-db
  annotations:
    athens.warewave.io/enabled: "true"
    athens.warewave.io/access: "orders-team:rw, analysts:ro"
    athens.warewave.io/readonly-provision: "true"   # read-only login for analysts
spec:
  # ...
  template:
    spec:
      containers:
        - name: postgres
          image: postgres:16
          env:
            - name: POSTGRES_USER
              valueFrom: {secretKeyRef: {name: orders-db, key: POSTGRES_USER}}
            - name: POSTGRES_PASSWORD
              valueFrom: {secretKeyRef: {name: orders-db, key: POSTGRES_PASSWORD}}
            - {name: POSTGRES_DB, value: orders}
```

athens resolves:
- **user:** `POSTGRES_USER` from the Secret.
- **password:** `POSTGRES_PASSWORD` from the Secret.
- **database:** `orders`.
- **host:** `orders-db.<namespace>.svc:5432`.
- **read-only login:** `athens_read`, created by athens over the main login
  (`orders` is the superuser the image creates).

The same works for an existing workload without editing its manifest:

```bash
kubectl -n orders annotate statefulset orders-db athens.warewave.io/enabled=true \
  'athens.warewave.io/access=orders-team:rw,analysts:ro' athens.warewave.io/readonly-provision=true
```

Supported credential forms in the container env:
- `valueFrom.secretKeyRef`
- `envFrom.secretRef` (a `prefix` is supported)
- `POSTGRES_PASSWORD_FILE` pointing into a mounted Secret volume
- a literal `value:`, which works but adds a "plaintext password in pod spec"
  warning

athens never takes a password from a ConfigMap.

### Production database with an explicit login

[`examples/deployment-production.yaml`](examples/deployment-production.yaml):

```yaml
metadata:
  name: billing-db
  annotations:
    athens.warewave.io/enabled: "true"
    athens.warewave.io/access: "dba:rw, billing-dev:ro"
    athens.warewave.io/secret: billing-db-admin          # keys username / password
    athens.warewave.io/readonly-secret: billing-db-ro    # used for read access
    athens.warewave.io/database: billing
    athens.warewave.io/display-name: Billing
    athens.warewave.io/environment: production
    athens.warewave.io/sslmode: require
```

- `secret` overrides the env lookup. Use `user-key` / `password-key` when the
  Secret uses other key names, or `user` for a fixed user name.
- `environment: production` shows a gold production badge. **Every** data or
  schema change on this database then needs a confirmation where the user
  types the database name, not only destructive statements.
- `readonly-secret` is the login athens uses for users with read access; see
  [Read-only access inside PostgreSQL](#read-only-access-inside-postgresql).

### Bitnami Helm chart

The chart creates a StatefulSet named `<release>-postgresql` whose container
reads the password from a mounted file. athens follows the `_FILE` variables
into the Secret volume, so annotating is enough:

```bash
helm install pg oci://registry-1.docker.io/bitnamicharts/postgresql -n reporting \
  --set auth.username=app --set auth.database=appdb
```

```bash
kubectl -n reporting annotate statefulset pg-postgresql athens.warewave.io/enabled=true \
  athens.warewave.io/access=reporting:rw
```

The chart also creates a headless `-hl` Service. athens prefers the regular
Service and connects to `pg-postgresql.reporting.svc`. A `helm upgrade` keeps
the annotation unless the chart rewrites the StatefulSet's annotations.

### CloudNativePG

[`examples/cnpg-cluster.yaml`](examples/cnpg-cluster.yaml): annotate the
`Cluster`, not its pods or Services.

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: payments
  annotations:
    athens.warewave.io/enabled: "true"
    athens.warewave.io/access: "payments-dev:rw, dba:rw"
    athens.warewave.io/display-name: Payments
    athens.warewave.io/environment: staging
spec:
  instances: 3
  bootstrap:
    initdb:
      database: payments
      owner: payments
```

athens connects to:
- **host:** `payments-rw` (always the primary), with `sslmode: require`.
- **login:** the `payments-app` Secret, or `spec.bootstrap.initdb.secret` when
  that is set.
- **database:** the `initdb` database.

Optional settings:
- `discovery.cnpg.verifyTLS: true` verifies the server certificate against the
  `payments-ca` Secret.
- `discovery.cnpg.useSuperuser: true` uses `<cluster>-superuser` instead,
  but only on clusters with `enableSuperuserAccess`. Leave it off unless you
  need it.

```bash
kubectl -n payments annotate clusters.postgresql.cnpg.io payments athens.warewave.io/enabled=true \
  'athens.warewave.io/access=payments-dev:rw,dba:rw'
```

### Zalando postgres-operator

[`examples/zalando-postgresql.yaml`](examples/zalando-postgresql.yaml):

```yaml
apiVersion: acid.zalan.do/v1
kind: postgresql
metadata:
  name: acid-ledger
  annotations:
    athens.warewave.io/enabled: "true"
    athens.warewave.io/access: "ledger-team:rw"
spec:
  users:
    ledger_owner: []
  databases:
    ledger: ledger_owner
```

athens picks its login in this order:
1. the user in the `athens.warewave.io/user` annotation;
2. otherwise the owner of the first database (`ledger_owner`);
3. `postgres`, but only with `discovery.zalando.useSuperuser: true`.

It reads that user's operator Secret,
`ledger-owner.acid-ledger.credentials.postgresql.acid.zalan.do` (underscores in
the user name become dashes). Use `discovery.zalando.secretTemplate` if your
operator names Secrets differently.

```bash
kubectl -n ledger annotate postgresqls.acid.zalan.do acid-ledger athens.warewave.io/enabled=true \
  athens.warewave.io/access=ledger-team:rw
```

### Database outside the cluster

[`examples/external-database.yaml`](examples/external-database.yaml): for a
managed database (Azure Database for PostgreSQL, RDS, Cloud SQL) or a VM,
create an `ExternalName` Service in a watched namespace and annotate it:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: crm-flexible
  annotations:
    athens.warewave.io/enabled: "true"
    athens.warewave.io/access: "crm-admins:rw, sales-analysts:ro"
    athens.warewave.io/secret: crm-flexible-login
    athens.warewave.io/readonly-secret: crm-flexible-ro  # login for sales-analysts
    athens.warewave.io/database: crm
    athens.warewave.io/sslmode: verify-ca
    athens.warewave.io/ca-secret: azure-postgres-ca      # key ca.crt
spec:
  type: ExternalName
  externalName: crm-prod.postgres.database.azure.com
  ports:
    - {name: postgres, port: 5432}
```

Use `verify-ca`, not `verify-full`. athens connects to
`crm-flexible.<ns>.svc`, which is not the name on the server certificate, so
host name verification would fail. `verify-ca` still checks the certificate
chain against your CA. The Helm chart's NetworkPolicy only allows Postgres
egress inside the discovery namespaces, so add the external address to
`networkPolicy.extraEgress`:

```yaml
networkPolicy:
  extraEgress:
    - to: [{ipBlock: {cidr: 20.50.0.0/16}}]
      ports: [{protocol: TCP, port: 5432}]
```

## Read-only access inside PostgreSQL

Users with `ro` access only ever connect with a **read-only login**; they
never use the main (`secret`) login. Each database chooses one of two:

| | `readonly-secret` | `readonly-provision: "true"` |
|---|---|---|
| Who creates the login | you, once | athens, over the main login |
| Login name | yours | `athens_read` |
| Password | in a Secret you manage | random, kept in athens' memory only, new at every athens restart |
| What it may read | whatever you grant | everything (`pg_read_all_data`) |
| PostgreSQL | any version | 14 or later |
| Main login needs | nothing extra | superuser, or `CREATEROLE` plus the right to grant `pg_read_all_data` |

If both are set, `readonly-secret` wins. **With neither, read users cannot
use the database:** it does not appear for them at all (the database stays
available to `rw` users), and admins see the warning "read users cannot
connect". athens never falls back to the main login, and never switches
roles on it: on a superuser login a role switch is not a boundary.

On top of the login, read users always run in `READ ONLY` transactions and
the SQL editor accepts only reads from them.

**The read-only login must not be privileged.** athens checks it in every
read transaction and refuses read users (`read_login_privileged`, plus an
admin warning) when the login is, or is a member of, a superuser, a
`REPLICATION` or `BYPASSRLS` role, or one of `pg_monitor`,
`pg_read_all_settings`, `pg_read_all_stats`, `pg_signal_backend` and the
server-file roles. A grant made later is seen by the next query.

Independent of the login, athens never shows the catalogs that hold
credentials (`pg_authid`, `pg_shadow`, `pg_user_mapping`, `pg_subscription`,
`information_schema._pg_user_mappings` ...), neither in the SQL editor nor
in the grid. `pg_read_all_data` could read the password hashes otherwise.

### Let athens create the login

```bash
kubectl -n billing annotate deployment billing-db athens.warewave.io/readonly-provision=true
```

At the first read, athens connects with the main login and, in one
transaction:

```sql
CREATE ROLE athens_read WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE
  NOREPLICATION NOBYPASSRLS INHERIT PASSWORD 'SCRAM-SHA-256$4096:...';
COMMENT ON ROLE athens_read IS 'managed by athens (...)';
GRANT pg_read_all_data TO athens_read;
```

- Only a SCRAM verifier is sent; the password itself never leaves athens and
  is never written to a Secret, the store or a log.
- Later runs (an athens restart, or a rejected password) only `ALTER ROLE
  athens_read WITH LOGIN PASSWORD ...`, which a `CREATEROLE` login on a
  managed service may do.
- An existing `athens_read` role without athens' comment is left alone; read
  users then cannot connect and admins see why. The same happens when the
  role has gained anything beyond `LOGIN` and `pg_read_all_data` (another
  attribute or role membership): athens refuses to use it until it is fixed
  or dropped.
- Roles are server-wide: one `athens_read` serves every database on the
  server. Run one athens per PostgreSQL server; two installations would keep
  replacing each other's password.
- To remove it: drop the annotation and `DROP ROLE athens_read;`.

If the setup fails (PostgreSQL 13, a main login that may not create roles,
a foreign or widened `athens_read`), read users get `read_login_unavailable` and admins
see the reason on the Discovery page. Writers are not affected.

### Bring your own login

A login you create can be narrower than `pg_read_all_data`. Create it and
point `readonly-secret` at it:

```sql
-- PostgreSQL 14+
CREATE ROLE billing_ro LOGIN PASSWORD 'change-me-too';
GRANT pg_read_all_data TO billing_ro;
```

```sql
-- older versions, or to limit what read users see: per schema
CREATE ROLE billing_ro LOGIN PASSWORD 'change-me-too';
GRANT CONNECT ON DATABASE billing TO billing_ro;
GRANT USAGE ON SCHEMA public TO billing_ro;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO billing_ro;
ALTER DEFAULT PRIVILEGES FOR ROLE billing_owner IN SCHEMA public GRANT SELECT ON TABLES TO billing_ro;
```

```bash
kubectl -n billing create secret generic billing-db-ro \
  --from-literal=username=billing_ro --from-literal=password='change-me-too'
```

```bash
kubectl -n billing annotate deployment billing-db athens.warewave.io/readonly-secret=billing-db-ro
```

If PostgreSQL rejects the login or the Secret cannot be read, read users get
`read_login_unavailable` and admins see the warning "the read-only login
(readonly-secret) was rejected or its Secret cannot be read". athens reads
the Secret again on the next attempt, so fixing the Secret is enough. The
database's status for writers is not affected.

## Who may use a database

The `access` annotation lists the Entra groups that may use the database and
their level. It is required: a database without it is visible to nobody but
the admins' Discovery page.

```bash
kubectl -n hr annotate statefulset hr-db 'athens.warewave.io/access=system-admins:rw,developers:ro'
```

**Make it read-only for everyone on the list** (for example, production):

```bash
kubectl -n hr annotate statefulset hr-db athens.warewave.io/readonly=true \
  athens.warewave.io/readonly-provision=true
```

With these annotations ([`examples/access-annotations.yaml`](examples/access-annotations.yaml)),
only `system-admins` and `developers` see the database, and both get read
access through the read-only login. A `readonly` database without
`readonly-provision` or `readonly-secret` is usable by nobody.

| `access` entry | Meaning |
|---|---|
| `system-admins:rw` | read and write (`write` also works) |
| `developers:ro` | read only (`read` also works) |
| `3f2a9c1e-...:rw` | an Entra group object ID, used as is |

What happens in edge cases:
- **Separators:** entries are separated by commas, semicolons or spaces.
- **Duplicates:** a group listed twice gets the higher level.
- **Group names:** Entra tokens carry group IDs unless the app registration
  is set to emit names. Use IDs, configure Entra to send names, or ask the
  athens operators for `authz.groupAliases` (see
  [ACCESS-CONTROL.md](ACCESS-CONTROL.md#group-names-or-ids)).
- **Mistakes make access narrower, never wider:**
  - an invalid entry (`qa:admin`, `nogroup`) is ignored and shown as a warning;
  - an `access` value that grants nobody hides the database from everyone;
  - a `readonly` value other than `true`/`false` counts as `true`;
  - `ro` without a read-only login gives no access (warning "read users
    cannot connect").

To take a database out of athens, remove `enabled` (or set it to `"false"`).
Removing only `access` keeps it discovered but closed to everyone.

## Annotation reference

All annotations use the prefix `athens.warewave.io/` (configurable with
`discovery.annotationPrefix`). They go on the StatefulSet, Deployment,
Service, CNPG `Cluster` or Zalando `postgresql`.

| Annotation | Default | Purpose |
|---|---|---|
| `enabled` | — | `"true"` puts the database in athens |
| `secret` | env lookup | Secret with the login (required on a Service) |
| `user-key` / `password-key` | `username` / `password` | key names in `secret` |
| `user` | from `secret` | fixed user name (Zalando: which user's Secret to use) |
| `readonly-secret` | — | Secret with the read-only login read users connect with |
| `readonly-provision` | `false` | `"true"`: athens creates and manages the read-only login `athens_read` (PostgreSQL 14+); `readonly-secret` wins |
| `readonly-user-key` / `readonly-password-key` | `username` / `password` | key names in `readonly-secret` |
| `database` | `POSTGRES_DB` and similar env, else the user name; `postgres` if that does not exist | database opened first (only used to list the databases) |
| `service` | matched by selector | Service to connect through (on a workload) |
| `port` | the Service's postgres port, else 5432 | port override |
| `sslmode` | `discovery.defaultSSLMode` (`prefer`) | `disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full` |
| `ca-secret` / `ca-key` | — / `ca.crt` | CA bundle for `verify-ca` / `verify-full` |
| `display-name` | object name | name shown in the UI |
| `environment` | — | `production`, `staging`, `development`; `production` requires typed confirmation for every change |
| `access` | nobody | `group:rw,group:ro,...`: the groups that may use the database |
| `readonly` | `false` | `"true"` caps everyone at read access |

Annotations that only make sense on a workload or Service (`service`,
`port`, `secret`) are ignored on operator resources. For those, athens uses
the operator's Services and Secrets.

## Checking the result

Admins see every discovered database under **Administration → Discovery**:

- **Target and owner:** the athens ID (`<namespace>/<name>`) and the object
  that produced it.
- **Found by:** annotation, CloudNativePG, Zalando or image detection.
- **Endpoint:** host, port and first database, plus the TLS mode.
- **Access:** the groups from the `access` annotation and a `read-only`
  badge, or "no access annotation".
- **Warnings:** anything athens had to guess or ignore.
- **Namespaces table:** objects that looked like databases but could not be
  added (the problems listed below).

Users see the databases they may use in the server picker at the top of the
sidebar.

## Troubleshooting

| Problem on Admin → Discovery | Cause | Fix |
|---|---|---|
| `no_service` | no Service selects the pods and exposes the postgres port | create a Service, or add the `service` annotation |
| `service_not_found` | the `service` annotation names a Service that does not exist | fix the name |
| `credentials_unresolved` | no usable password in the container env, or a Service without `secret` | add the `secret` annotation |
| `no_container` | the annotated workload has no containers | check the object |

| Warning on the target | Meaning |
|---|---|
| plaintext password in pod spec | the password is a literal env value; move it to a Secret |
| only headless service ... assuming pod `<name>-0` is the primary | only a headless Service matches; athens connects to pod 0 |
| name ... is used by several targets; renamed | two objects share a name; one gets a `-sts` / `-deploy` / `-svc` suffix |
| invalid ... annotation ignored | an annotation value was not understood |
| access entry ... ignored | see [Who may use a database](#who-may-use-a-database) |
| read users cannot connect: no read-only login | the database grants `ro` (or is `readonly`) but has neither `readonly-secret` nor `readonly-provision`; read users do not see it |
| the read-only login (readonly-secret) was rejected or its Secret cannot be read | PostgreSQL refused the `readonly-secret` login, or the Secret or key is missing; fix the Secret or the role |
| the read-only login is privileged | the read-only login is a superuser or similar; read users are refused; use an unprivileged login |
| read-only login setup failed: ... | `readonly-provision` could not create `athens_read`; the message says why (PostgreSQL 13, main login may not create roles, a foreign `athens_read` role, or one with extra attributes or memberships) |
| readonly-provision ignored: readonly-secret is set | both annotations are set; the Secret's login is used |
| database "..." does not exist; connecting to "postgres" instead | athens guessed the first database from the env or the user name and it doesn't exist; set `athens.warewave.io/database` to a database that does |

The database does not appear at all:
1. Is the annotation spelled exactly `athens.warewave.io/enabled: "true"`
   (quoted, on the workload or `Cluster`, not on the pod template)?
2. If the operators limited athens with `discovery.namespaces`, is the
   namespace in that list?
3. Is the database hidden for you only? Check Admin → Discovery. If it is
   there, check its `access` annotation: does it list one of your groups, by
   the same ID or name your token carries?

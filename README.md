# athens

In-cluster PostgreSQL web client with Microsoft Entra ID single sign-on and
automatic discovery of PostgreSQL instances in Kubernetes namespaces.

- **Sign-in only through Entra ID** (OIDC, authorization code + PKCE). No local users.
- **Discovery** of plain StatefulSets/Deployments (official, bitnami, postgis,
  pgvector, timescale images), CloudNativePG and Zalando clusters, and
  explicitly annotated workloads/Services. Credentials are read from Secrets at
  connect time and never leave the backend.
- **Access per Entra group**: rules map groups to namespaces/targets/databases
  with `read` or `write` level. Read access always runs in `READ ONLY`
  transactions (and with a read-only DB role when one is annotated).
- **Schema explorer, data grid, SQL editor** with history, saved queries,
  drafts, confirmation of destructive statements and audit log.

Design and rules: [`docs/SPEC.md`](docs/SPEC.md), [`docs/DISCOVERY.md`](docs/DISCOVERY.md),
[`docs/API.md`](docs/API.md), [`docs/DECISIONS.md`](docs/DECISIONS.md).
Operations: [`docs/OPERATIONS.md`](docs/OPERATIONS.md).

## Install

### 1. Register the Entra ID application

1. **Entra admin center → App registrations → New registration**
   - Name: `athens`
   - Supported account types: *Accounts in this organizational directory only*
   - Redirect URI: platform **Web**, `https://<your-athens-host>/auth/callback`
2. **Certificates & secrets → New client secret.** Copy the value (it is shown once).
3. **Token configuration → Add groups claim** → select **Groups assigned to the
   application** (this sets `groupMembershipClaims` to `ApplicationGroup`).
   Emit group IDs in the ID token. Users in more than 200 groups would get an
   overage claim, which athens rejects (ADR-007); `ApplicationGroup` avoids it.
4. **Enterprise applications → athens → Properties**: set *Assignment
   required?* to **Yes**, then **Users and groups → Add user/group** and assign
   every group you reference in `authz` rules.
5. **Manifest** (optional check): `"groupMembershipClaims": "ApplicationGroup"`.
6. Note the **Directory (tenant) ID** and **Application (client) ID**.
7. Front-channel logout URL (optional): `https://<your-athens-host>/`.

### 2. Create the secret

```bash
kubectl create namespace athens
```

```bash
kubectl -n athens create secret generic athens \
  --from-literal=clientSecret='<client secret from step 1.2>' \
  --from-literal=sessionKey="$(openssl rand -base64 32)"
```

### 3. Install the chart

```yaml
# values.yaml
image:
  tag: "0.1.0"
config:
  server:
    baseURL: https://athens.example.com
    trustedProxies: ["10.0.0.0/8"]        # your ingress controller's pod CIDR
  oidc:
    issuer: https://login.microsoftonline.com/<TENANT_ID>/v2.0
    clientID: <APPLICATION_CLIENT_ID>
  authz:
    adminGroups: ["<GROUP_ID_PLATFORM_ADMINS>"]
    rules:
      - name: dba
        groups: ["<GROUP_ID_DBA>"]
        namespaces: ["*"]
        level: write
      - name: payments-read
        groups: ["<GROUP_ID_PAYMENTS_DEV>"]
        namespaces: ["payments"]
        level: read
  discovery:
    namespaces: ["payments", "orders"]    # a Role is created in each
secret:
  existingSecret: athens
ingress:
  enabled: true
  className: nginx
  host: athens.example.com
  tls: [{hosts: [athens.example.com], secretName: athens-tls}]
```

```bash
helm upgrade --install athens deploy/helm/athens -n athens -f values.yaml
```

The pod runs under Pod Security `restricted`, with a read-only root file
system and a 1 GiB PVC for the SQLite store (single replica, ADR-003). The
NetworkPolicy (on by default) allows ingress from the ingress controller
namespace and egress to the Kubernetes API, Entra ID, DNS and Postgres ports in
the discovery namespaces; adjust `networkPolicy.*` for your cluster.

See [`config.example.yaml`](config.example.yaml) for every setting. Any scalar
can be overridden with `ATHENS_<PATH>` environment variables, e.g.
`ATHENS_LIMITS_STATEMENTTIMEOUT=60s`.

### Make databases discoverable

Discovery is opt-in: a database appears in athens only when its StatefulSet,
Deployment, Service, CloudNativePG `Cluster` or Zalando `postgresql` carries
`athens.warewave.io/enabled: "true"` (ADR-020). The minimum is that one line;
athens then reads the credentials from the container env (or from the
operator's secrets for CNPG/Zalando). Add more annotations for an explicit
secret, a read-only credential, an environment badge or a display name:

```yaml
metadata:
  annotations:
    athens.warewave.io/enabled: "true"
    athens.warewave.io/secret: orders-db-admin        # keys: username, password
    athens.warewave.io/readonly-secret: orders-db-ro  # recommended for production
    athens.warewave.io/environment: production        # every write needs confirmation
    athens.warewave.io/display-name: Orders
```

```bash
kubectl -n payments annotate statefulset orders-db athens.warewave.io/enabled=true
```

Two annotations control access per database (ADR-022):

```yaml
    athens.warewave.io/readonly: "true"                          # everyone gets read only
    athens.warewave.io/access: "system-admins:rw,developers:ro"  # who may use it
```

`access` replaces the `authz.rules` for that database: only the listed groups
(Entra group IDs, or names mapped in `authz.groupAliases`) see it. With
`readonly: "true"` everyone on the list is `ro`.

All annotations are listed in [`docs/DISCOVERY.md`](docs/DISCOVERY.md) §4.1
and §2b.
Setting `discovery.requireAnnotation: false` switches to automatic discovery of
unannotated postgres workloads and operator clusters.

## Development

Requirements: Go 1.26+, Node 22+ with pnpm, Docker, kind, helm.

```bash
make build      # web + embedded binary in bin/athens
```

```bash
make test       # unit tests (Go + web)
```

```bash
make test-int   # integration tests against Postgres 16 and 13 (testcontainers)
```

```bash
make e2e        # kind cluster with CNPG, Zalando, bitnami and plain fixtures
```

```bash
make lint       # golangci-lint, eslint, tsc, helm lint
```

Local UI development against the kind cluster:

```bash
./hack/kind/cluster.sh
```

```bash
./hack/kind/port-forward.sh   # keep running; writes hack/dev/config.local.yaml
```

```bash
make dev                      # mock IdP :7082, athens :7080, Vite :5173
```

Open <http://localhost:7080>. The mock IdP signs you in as a dev user who is an
admin and writer.

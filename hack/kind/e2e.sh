#!/usr/bin/env bash
# End-to-end test: kind cluster + CNPG + Zalando + bitnami + plain fixtures +
# mock IdP + athens chart (PSS restricted, NetworkPolicy on), then an
# in-cluster API-level test Job and a log scan for leaked secrets.
set -euo pipefail
cd "$(dirname "$0")"
ROOT=$(cd ../.. && pwd)
CLUSTER=${ATHENS_KIND_CLUSTER:-athens-dev}
export KUBECONFIG=$PWD/.kube/config
CNPG_VERSION=1.30.1
ZALANDO_CHART_VERSION=2.0.3
log() { printf '\n=== %s\n' "$*"; }

log "cluster + plain fixtures"
./cluster.sh

log "images"
for target in athens mockidp e2e; do
  tag=athens:e2e
  [ "$target" = athens ] || tag=athens-$target:e2e
  docker build -q --target "$target" -t "$tag" "$ROOT" >/dev/null
  kind load docker-image --name "$CLUSTER" "$tag" >/dev/null
done

log "CloudNativePG $CNPG_VERSION"
kubectl apply --server-side -f "https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-${CNPG_VERSION%.*}/releases/cnpg-${CNPG_VERSION}.yaml" >/dev/null
kubectl -n cnpg-system rollout status deploy/cnpg-controller-manager --timeout=300s

log "Zalando postgres-operator $ZALANDO_CHART_VERSION"
helm upgrade --install postgres-operator postgres-operator \
  --repo https://opensource.zalando.com/postgres-operator/charts/postgres-operator \
  --version "$ZALANDO_CHART_VERSION" -n zalando --create-namespace --wait --timeout 5m >/dev/null

log "bitnami postgresql"
helm upgrade --install pg oci://registry-1.docker.io/bitnamicharts/postgresql -n athens-demo \
  --set auth.username=app --set auth.password=bitnami-app-pw-e2e --set auth.postgresPassword=bitnami-admin-pw-e2e \
  --set auth.database=appdb --set auth.usePasswordFiles=true --set primary.persistence.size=1Gi \
  --wait --timeout 10m >/dev/null

log "operator clusters"
for i in $(seq 1 30); do kubectl apply -f operators/ >/dev/null 2>&1 && break; sleep 5; done
kubectl -n athens-demo wait cluster/cnpg-db --for=condition=Ready --timeout=600s
for i in $(seq 1 120); do
  kubectl -n athens-demo get pod acid-e2e-0 >/dev/null 2>&1 && break; sleep 5
done
kubectl -n athens-demo wait pod/acid-e2e-0 --for=condition=Ready --timeout=600s
for i in $(seq 1 60); do
  kubectl -n athens-demo get secret foo-user.acid-e2e.credentials.postgresql.acid.zalan.do >/dev/null 2>&1 && break; sleep 5
done
kubectl -n athens-demo wait --for=condition=Ready pod --all --timeout=600s

log "mock IdP + athens (namespace athens-system, PSS restricted)"
kubectl apply -f system/mockidp.yaml >/dev/null
kubectl -n athens-system rollout restart deploy/mockidp >/dev/null
kubectl -n athens-system rollout status deploy/mockidp --timeout=120s
helm upgrade --install athens "$ROOT/deploy/helm/athens" -n athens-system -f values-e2e.yaml --wait --timeout 5m >/dev/null
kubectl -n athens-system rollout restart deploy/athens >/dev/null
kubectl -n athens-system rollout status deploy/athens --timeout=180s

log "e2e job"
kubectl -n athens-system delete job athens-e2e --ignore-not-found >/dev/null
kubectl apply -f system/e2e-job.yaml >/dev/null
status=failed
for i in $(seq 1 180); do
  if kubectl -n athens-system wait job/athens-e2e --for=condition=Complete --timeout=1s >/dev/null 2>&1; then status=ok; break; fi
  if kubectl -n athens-system wait job/athens-e2e --for=condition=Failed --timeout=1s >/dev/null 2>&1; then break; fi
  sleep 4
done
kubectl -n athens-system logs job/athens-e2e || true

log "secret leak scan of athens logs"
logs=$(kubectl -n athens-system logs deploy/athens)
secrets="orders-pw-e2e scratch-literal-pw files-pw-e2e crm-admin-pw-e2e crm-ro-pw-e2e bitnami-app-pw-e2e bitnami-admin-pw-e2e athens-e2e-client-secret ZTJlLW9ubHktc2Vzc2lvbi1rZXktMzItYnl0ZXMtbG9uZyE="
for s in cnpg-db-app foo-user.acid-e2e.credentials.postgresql.acid.zalan.do; do
  secrets="$secrets $(kubectl -n athens-demo get secret "$s" -o jsonpath='{.data.password}' | base64 -d)"
done
leak=0
for s in $secrets; do
  if grep -qF -- "$s" <<<"$logs"; then echo "LEAK: a secret value appears in athens logs"; leak=1; fi
done
grep -q '"log":"audit"' <<<"$logs" || { echo "no audit records on stdout"; leak=1; }
[ $leak = 0 ] && echo "no secret values in $(wc -l <<<"$logs") log lines"

if [ "$status" = ok ] && [ $leak = 0 ]; then log "E2E PASSED"; else log "E2E FAILED"; exit 1; fi

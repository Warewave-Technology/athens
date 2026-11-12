#!/usr/bin/env bash
# Creates (or reuses) the athens kind cluster and installs the fixtures.
# The kubeconfig is written to hack/kind/.kube/config; your current context
# is not touched.
set -euo pipefail
cd "$(dirname "$0")"
CLUSTER=${ATHENS_KIND_CLUSTER:-athens-dev}
export KUBECONFIG=$PWD/.kube/config
mkdir -p .kube

if ! kind get clusters | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER" --kubeconfig "$KUBECONFIG" --wait 120s
fi
kubectl apply -f fixtures/plain.yaml
for f in fixtures/*.yaml; do
  [ "$f" = fixtures/plain.yaml ] || kubectl apply -f "$f"
done
echo "KUBECONFIG=$KUBECONFIG"

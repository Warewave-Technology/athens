#!/usr/bin/env bash
# Port-forwards every Service in the demo namespaces and writes
# hack/dev/config.local.yaml (hack/dev/config.yaml + server.devHostOverrides),
# which `make dev` prefers when present.
set -euo pipefail
cd "$(dirname "$0")"
export KUBECONFIG=$PWD/.kube/config
NAMESPACES=${ATHENS_DEV_NAMESPACES:-athens-demo}
out=$(mktemp)
echo "  devHostOverrides:" > "$out"
port=25432
trap 'kill 0' EXIT
for ns in $NAMESPACES; do
  for svc in $(kubectl -n "$ns" get svc -o jsonpath='{.items[*].metadata.name}'); do
    sport=$(kubectl -n "$ns" get svc "$svc" -o jsonpath='{.spec.ports[0].port}')
    if [ "$(kubectl -n "$ns" get svc "$svc" -o jsonpath='{.spec.clusterIP}')" = None ]; then
      pod=$(kubectl -n "$ns" get pods -o name | grep "/${svc%-hl}-0$" || true)
      [ -n "$pod" ] || continue
      kubectl -n "$ns" port-forward "$pod" "$port:$sport" >/dev/null &
      echo "    \"${pod#pod/}.$svc.$ns.svc:$sport\": \"127.0.0.1:$port\"" >> "$out"
    else
      kubectl -n "$ns" port-forward "svc/$svc" "$port:$sport" >/dev/null &
      echo "    \"$svc.$ns.svc:$sport\": \"127.0.0.1:$port\"" >> "$out"
    fi
    port=$((port + 1))
  done
done
awk -v f="$out" '{print} /^  insecureDev: true/ {while ((getline l < f) > 0) print l}' ../dev/config.yaml > ../dev/config.local.yaml
rm -f "$out"
grep -A20 devHostOverrides ../dev/config.local.yaml | head -20
echo "port-forwards running; Ctrl-C to stop"
wait

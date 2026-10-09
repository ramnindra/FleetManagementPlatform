#!/usr/bin/env bash
# Brings up the platform on a local kind cluster with NO devices registered:
# controller + EMQX + Postgres via Helm, exposed on localhost:8000 (API/web UI)
# and localhost:1883 (MQTT). Start the simulator against it afterwards:
#
#   scripts/k8s-up.sh            # deploy (reuses the cluster if it exists)
#   scripts/k8s-up.sh --fresh    # delete and recreate the cluster first (zero devices)
#   scripts/k8s-up.sh down       # delete the cluster
#
# Needs: docker, kind, helm, kubectl. Frees host ports 8000/1883 by stopping
# the docker-compose stack if it is running.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CLUSTER=device-controller
CTX="kind-$CLUSTER"
NS=device-controller
CHART="$ROOT/kubernetes/helm/device-controller"
IMAGE=cloud-device-controller-controller:latest
PF_PIDS="$ROOT/.k8s-port-forward.pids"
KUBECTL=(kubectl --context "$CTX")

bold() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }

stop_port_forwards() {
  if [ -f "$PF_PIDS" ]; then
    xargs kill 2>/dev/null < "$PF_PIDS" || true
    rm -f "$PF_PIDS"
  fi
}

if [ "${1:-}" = "--fresh" ]; then
  stop_port_forwards
  kind delete cluster --name "$CLUSTER"
fi

if [ "${1:-}" = "down" ]; then
  stop_port_forwards
  kind delete cluster --name "$CLUSTER"
  exit 0
fi

bold "1. kind cluster ($CLUSTER)"
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  echo "cluster already exists"
else
  kind create cluster --name "$CLUSTER" --config "$ROOT/kubernetes/kind/cluster.yaml"
fi
"${KUBECTL[@]}" get nodes

bold "2. Build and load the controller image"
docker build -q -t "$IMAGE" "$ROOT/controller"
kind load docker-image "$IMAGE" --name "$CLUSTER"

bold "3. Helm install (controller + EMQX + Postgres)"
helm upgrade --install device-controller "$CHART" -f "$CHART/values-dev.yaml" \
  --kube-context "$CTX" --create-namespace --namespace "$NS" --timeout 5m
"${KUBECTL[@]}" -n "$NS" rollout status statefulset/device-controller-postgres --timeout=180s
"${KUBECTL[@]}" -n "$NS" rollout status statefulset/device-controller-emqx --timeout=180s
"${KUBECTL[@]}" -n "$NS" rollout status deployment/device-controller --timeout=180s
"${KUBECTL[@]}" -n "$NS" get pods -o wide

bold "4. Expose the cluster on localhost (port-forward)"
if (cd "$ROOT" && docker compose ps -q 2>/dev/null | grep -q .); then
  echo "stopping docker-compose stack to free ports 8000/1883"
  (cd "$ROOT" && docker compose down)
fi
stop_port_forwards
"${KUBECTL[@]}" -n "$NS" port-forward svc/device-controller 8000:8000 >/dev/null 2>&1 &
echo $! > "$PF_PIDS"
"${KUBECTL[@]}" -n "$NS" port-forward svc/device-controller-emqx 1883:1883 >/dev/null 2>&1 &
echo $! >> "$PF_PIDS"
for _ in $(seq 1 30); do curl -sf localhost:8000/readyz >/dev/null 2>&1 && break; sleep 1; done
curl -sf localhost:8000/readyz && echo
echo "controller: http://localhost:8000/ui/   (stop with: scripts/k8s-up.sh down)"

bold "Ready — the cluster has no devices yet"
echo "Registered devices: $(curl -s localhost:8000/api/v1/devices | python3 -c 'import sys,json;print(len(json.load(sys.stdin)))')"
echo "Start the simulator:  scripts/simulate.sh --count 5     (or: cd simulator && go run ./cmd/simulator --cluster localhost --count 5)"


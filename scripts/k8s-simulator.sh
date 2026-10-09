#!/usr/bin/env bash
# Runs the simulated fleet INSIDE the kind cluster (Helm release "simulator"),
# connected to the in-cluster controller/broker. Instructions sent from the web
# UI show up in the simulator pod's logs.
#
#   scripts/k8s-simulator.sh [count] [extra helm --set args...]
#   scripts/k8s-simulator.sh 20 --set simulator.deviceTypes.gpu=5 --set simulator.failureRate=0.1
#   scripts/k8s-simulator.sh logs        # follow instructions as they arrive
#   scripts/k8s-simulator.sh down        # remove the simulator
#
# Needs the cluster from scripts/k8s-up.sh.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CLUSTER=device-controller
CTX="kind-$CLUSTER"
NS=device-controller
IMAGE=cloud-device-controller-simulator:latest

case "${1:-}" in
  logs) exec kubectl --context "$CTX" -n "$NS" logs -f deploy/simulator ;;
  down) helm uninstall simulator --kube-context "$CTX" --namespace "$NS"; exit 0 ;;
esac

COUNT="${1:-10}"; [ $# -gt 0 ] && shift
kind get clusters | grep -qx "$CLUSTER" || { echo "no '$CLUSTER' cluster; run scripts/k8s-up.sh first"; exit 1; }

docker build -q -t "$IMAGE" "$ROOT/simulator" >/dev/null
kind load docker-image "$IMAGE" --name "$CLUSTER" >/dev/null
helm upgrade --install simulator "$ROOT/kubernetes/helm/simulator" \
  --kube-context "$CTX" --namespace "$NS" --set simulator.count="$COUNT" "$@"
kubectl --context "$CTX" -n "$NS" rollout status deployment/simulator --timeout=120s
echo
echo "Fleet of $COUNT nodes starting. Web UI: http://localhost:8000/ui/"
echo "Watch instructions arrive:  scripts/k8s-simulator.sh logs"

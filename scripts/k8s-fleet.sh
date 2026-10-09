#!/usr/bin/env bash
# Deploys the platform to a local kind cluster (scripts/k8s-up.sh), then
# connects a fleet of simulated devices (default 50) to it and prints a
# load-test report.
#
#   scripts/k8s-fleet.sh [device-count]     # default 50
#   scripts/k8s-fleet.sh down               # delete the cluster
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
bold() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }

if [ "${1:-}" = "down" ]; then exec "$ROOT/scripts/k8s-up.sh" down; fi
COUNT="${1:-50}"

"$ROOT/scripts/k8s-up.sh"

bold "5. Connect $COUNT simulated devices"
REPORT="$ROOT/docs/load-test-results"
(cd "$ROOT/simulator" && go run ./cmd/load-test --count "$COUNT" \
  --api-endpoint http://localhost:8000 --mqtt-host localhost --admin-api-key dev-admin-key \
  --heartbeat-interval 10s --steady-state 30s --sample-commands 20 --reconnect-sample 10 \
  --report-dir "$REPORT")

bold "6. Result"
curl -s localhost:8000/metrics | grep -E '^controller_devices_(online|offline)'
LATEST="$(ls -t "$REPORT"/load-test-*.md | head -1)"
sed -n 1,22p "$LATEST"

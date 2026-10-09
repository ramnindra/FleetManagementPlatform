#!/usr/bin/env bash
# Runs the simulator against a cluster. All arguments are passed through to the
# simulator CLI; --cluster defaults to localhost (the port-forward set up by
# scripts/k8s-up.sh).
#
#   scripts/simulate.sh --count 5
#   scripts/simulate.sh --cluster 10.0.0.5 --count 50 --device-type gpu
#   scripts/simulate.sh --config simulator/simulator.example.yaml --count 3
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
args=("$@")
has_target=0
for a in "${args[@]:-}"; do case "$a" in --cluster*|--config*|--api-endpoint*) has_target=1;; esac; done
[ "$has_target" = 1 ] || args=(--cluster localhost "${args[@]:-}")
cd "$ROOT/simulator" && exec go run ./cmd/simulator "${args[@]}"

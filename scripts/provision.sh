#!/usr/bin/env bash
# Registers every device in an inventory file with the controller and renders
# one enrollment bundle (config.yaml + README.txt) per device.
#
#   scripts/provision.sh [inventory.yaml] [output-dir]
#
# Env: API_ENDPOINT (default http://localhost:8000), ADMIN_API_KEY (default
# dev-admin-key), MQTT_HOST (default localhost). Re-running skips devices that
# are already registered.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
INVENTORY="${1:-$ROOT/provisioning/inventory/devices.example.yaml}"
OUTPUT="${2:-$ROOT/provisioning/output}"

cd "$ROOT/provisioning"
go run ./cmd/bulk-provision \
  --inventory "$INVENTORY" \
  --output-dir "$OUTPUT" \
  --api-endpoint "${API_ENDPOINT:-http://localhost:8000}" \
  --admin-api-key "${ADMIN_API_KEY:-dev-admin-key}" \
  --mqtt-host "${MQTT_HOST:-localhost}"

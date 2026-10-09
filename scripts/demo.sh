#!/usr/bin/env bash
# End-to-end demo: provisions the example inventory, starts a real device agent
# for each device from its enrollment bundle, then shows the fleet coming online
# and executing commands.
#
#   scripts/demo.sh                # uses the running stack, or starts one and stops it afterwards
#   KEEP_STACK=1 scripts/demo.sh   # leave a stack that this script started running
#
# Flow: bring up Postgres+EMQX+controller -> register devices (bulk-provision)
# -> each agent enrolls with its one-time token -> heartbeats -> send commands
# -> show acks, device status and metrics -> clean up the agents.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
API="${API_ENDPOINT:-http://localhost:8000}"
KEY="${ADMIN_API_KEY:-dev-admin-key}"
WORK="$(mktemp -d)"
PIDS=()
STARTED_STACK=0

bold() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
json() { python3 -m json.tool 2>/dev/null || cat; }

cleanup() {
  for pid in "${PIDS[@]:-}"; do [ -n "$pid" ] && kill "$pid" 2>/dev/null || true; done
  rm -rf "$WORK"
}
trap cleanup EXIT

bold "1. Controller stack"
if curl -sf "$API/readyz" >/dev/null 2>&1; then
  echo "controller already running at $API"
else
  STARTED_STACK=1
  (cd "$ROOT" && docker compose up --build -d)
  for _ in $(seq 1 60); do curl -sf "$API/readyz" >/dev/null 2>&1 && break; sleep 2; done
  curl -sf "$API/readyz" >/dev/null || { echo "controller did not become ready"; exit 1; }
fi
echo "ready: $(curl -s "$API/readyz")"

# Unique device ids per run, so the demo can be repeated against a live stack.
RUN="$(date +%s)"
INV="$WORK/inventory.yaml"
cat > "$INV" <<YAML
devices:
  - device_id: demo-switch-$RUN
    device_type: switch
    heartbeat_interval_seconds: 5
    attributes: {site: dc1, rack: "3"}
  - device_id: demo-gpu-$RUN
    device_type: gpu
    heartbeat_interval_seconds: 5
    attributes: {site: dc1, gpu_model: A100}
  - device_id: demo-edge-$RUN
    device_type: edge
    heartbeat_interval_seconds: 5
    attributes: {site: branch-04}
YAML

bold "2. Provision devices (register + render enrollment bundles)"
"$ROOT/scripts/provision.sh" "$INV" "$WORK/bundles" 2>&1 | grep -E "provisioned|summary" | sed 's/^/  /'
ls "$WORK/bundles" | sed 's/^/  bundle: /'

bold "3. Start one agent per device (each enrolls with its one-time token)"
(cd "$ROOT/device-agent" && go build -o "$WORK/device-agent" ./cmd/device-agent)
for dir in "$WORK"/bundles/*/; do
  id="$(basename "$dir")"
  # The bundle targets /etc/device-agent; point the credential at a temp dir.
  sed "s#/etc/device-agent/#$WORK/creds/#" "$dir/config.yaml" > "$WORK/$id.yaml"
  AGENT_CONFIG_PATH="$WORK/$id.yaml" "$WORK/device-agent" > "$WORK/$id.log" 2>&1 &
  PIDS+=("$!")
  echo "  started agent for $id (pid $!)"
done

bold "4. Wait for devices to come online"
IDS=($(ls "$WORK/bundles"))
for _ in $(seq 1 30); do
  online=0
  for id in "${IDS[@]}"; do
    curl -s "$API/api/v1/devices/$id/status" | grep -q '"online":true' && online=$((online+1))
  done
  [ "$online" -eq "${#IDS[@]}" ] && break
  sleep 1
done
for id in "${IDS[@]}"; do
  printf '  %-26s ' "$id"; curl -s "$API/api/v1/devices/$id/status" | python3 -c \
    'import sys,json;d=json.load(sys.stdin);print("status=%s online=%s last_seen=%.1fs ago"%(d["status"],d["online"],d["seconds_since_last_seen"] or -1))'
done
[ "$online" -eq "${#IDS[@]}" ] || { echo "not all devices came online"; exit 1; }

bold "5. Send commands and read the acks"
for id in "${IDS[@]}"; do
  cid="$(curl -s -X POST "$API/api/v1/devices/$id/commands" -H 'Content-Type: application/json' \
    -d '{"action":"get_status"}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["command_id"])')"
  sleep 1
  printf '  %-26s get_status -> ' "$id"
  curl -s "$API/api/v1/commands/$cid" | python3 -c 'import sys,json;d=json.load(sys.stdin);print(d["status"],d["result"])'
done

gpu="demo-gpu-$RUN"
echo; echo "  update_config on $gpu (heartbeat 5s -> 10s, then an out-of-range value):"
for v in 10 999999; do
  cid="$(curl -s -X POST "$API/api/v1/devices/$gpu/commands" -H 'Content-Type: application/json' \
    -d "{\"action\":\"update_config\",\"params\":{\"heartbeat_interval_seconds\":$v}}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["command_id"])')"
  sleep 1
  printf '    interval=%-7s -> ' "$v"
  curl -s "$API/api/v1/commands/$cid" | python3 -c 'import sys,json;d=json.load(sys.stdin)["result"];print("applied=%s rejected=%s"%(d["applied"],d["rejected"]))'
done

bold "6. Security: commands outside the allowlist are refused"
printf '  action "rm -rf /" -> HTTP '
curl -s -o /dev/null -w '%{http_code}\n' -X POST "$API/api/v1/devices/$gpu/commands" \
  -H 'Content-Type: application/json' -d '{"action":"rm -rf /"}'

bold "7. Recent messages from $gpu (newest first)"
curl -s "$API/api/v1/devices/$gpu/messages?limit=4" | python3 -c '
import sys,json
for m in json.load(sys.stdin): print("  #%-4d %-9s %s"%(m["id"],m["kind"],json.dumps(m["payload"])[:90]))'

bold "8. Fleet metrics"
curl -s "$API/metrics" | grep -E '^controller_(devices_online|devices_offline|commands_published_total|command_acks_received_total|heartbeats_received_total\{device_id="demo-gpu)' | sed 's/^/  /'

bold "Done"
echo "  Web UI: $API/ui/   (devices stay registered; agents are stopped on exit)"
if [ "${KEEP_STACK:-0}" != "1" ] && [ "${STARTED_STACK:-0}" = "1" ]; then (cd "$ROOT" && docker compose down); fi

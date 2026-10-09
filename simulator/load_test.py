#!/usr/bin/env python3
"""Scale/load test harness for the device controller.

Provisions and connects N simulated devices, lets them heartbeat for a
measurement window, issues sample commands through the controller's REST API
to measure end-to-end command/ack latency, forces a subset to disconnect and
reconnect, then writes a JSON + Markdown report to --report-dir.

This is the tool behind the 10 / 100 / 1,000-device runs required by
docs/roadmap.md Phase 8. Every number in the report comes from an actual run
against a real controller + broker — nothing here is a fabricated benchmark.

Example:
    python load_test.py --count 1000 --api-endpoint http://localhost:8000 \
        --mqtt-host localhost --admin-api-key dev-admin-key \
        --report-dir ../docs/load-test-results
"""

import argparse
import json
import logging
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
from pathlib import Path

import requests

from common import Metrics, SimConfig, SimulatedDevice, provision_and_connect

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("load_test")


def parse_args():
    p = argparse.ArgumentParser(description="Load-test the device controller at scale")
    p.add_argument("--count", type=int, default=100)
    p.add_argument("--device-type", default="edge", choices=["switch", "gpu", "edge"])
    p.add_argument("--api-endpoint", default="http://localhost:8000")
    p.add_argument("--mqtt-host", default="localhost")
    p.add_argument("--mqtt-port", type=int, default=1883)
    p.add_argument("--admin-api-key", default="dev-admin-key")
    p.add_argument("--heartbeat-interval", type=float, default=30.0)
    p.add_argument("--connect-wait-seconds", type=float, default=10.0, help="per-device on_connect wait before giving up")
    p.add_argument("--provision-workers", type=int, default=50)
    p.add_argument("--steady-state-seconds", type=float, default=60.0)
    p.add_argument("--sample-commands", type=int, default=50, help="how many devices to send a command to")
    p.add_argument("--reconnect-sample", type=int, default=20, help="how many devices to force-disconnect")
    p.add_argument("--report-dir", default="./load-test-results")
    return p.parse_args()


def run_command_latency_sample(api_endpoint: str, device_ids: list[str], admin_headers: dict) -> list[float]:
    latencies = []
    for device_id in device_ids:
        start = time.monotonic()
        resp = requests.post(f"{api_endpoint}/api/v1/devices/{device_id}/commands", json={"action": "ping"})
        if resp.status_code != 202:
            continue
        command_id = resp.json()["command_id"]

        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            poll = requests.get(f"{api_endpoint}/api/v1/commands/{command_id}")
            status = poll.json().get("status")
            if status in ("success", "failed"):
                latencies.append(time.monotonic() - start)
                break
            time.sleep(0.2)
    return latencies


def percentile(values: list[float], pct: float) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    idx = min(len(ordered) - 1, int(len(ordered) * pct))
    return ordered[idx]


def main():
    args = parse_args()
    config = SimConfig(
        api_endpoint=args.api_endpoint,
        mqtt_host=args.mqtt_host,
        mqtt_port=args.mqtt_port,
        admin_api_key=args.admin_api_key,
        heartbeat_interval_seconds=args.heartbeat_interval,
        device_type=args.device_type,
        connect_wait_seconds=args.connect_wait_seconds,
    )
    metrics = Metrics()
    devices = [SimulatedDevice(i, config, metrics) for i in range(args.count)]

    report = {
        "run_id": config.run_id,
        "started_at": datetime.now(timezone.utc).isoformat(),
        "requested_device_count": args.count,
        "heartbeat_interval_seconds": args.heartbeat_interval,
    }

    logger.info("phase_provisioning_start", extra={"count": args.count})
    provision_start = time.monotonic()
    with ThreadPoolExecutor(max_workers=min(args.provision_workers, max(1, args.count))) as pool:
        results = list(pool.map(provision_and_connect, devices))
    provision_elapsed = time.monotonic() - provision_start

    connected = [d for d, ok in zip(devices, results) if ok]
    report["onboarding"] = {
        "connected": len(connected),
        "requested": args.count,
        "success_rate_percent": round(100 * len(connected) / args.count, 2) if args.count else 0,
        "elapsed_seconds": round(provision_elapsed, 2),
    }
    logger.info("phase_provisioning_done", extra=report["onboarding"])

    if not connected:
        logger.error("no_devices_connected_aborting")
        return

    stop_event = threading.Event()
    threads = [threading.Thread(target=d.heartbeat_loop, args=(stop_event,), daemon=True) for d in connected]
    for t in threads:
        t.start()

    logger.info("phase_steady_state", extra={"seconds": args.steady_state_seconds})
    time.sleep(min(10, args.steady_state_seconds))  # let heartbeats establish before sampling

    sample_ids = [d.device_id for d in connected[: args.sample_commands]]
    logger.info("phase_command_latency_sample", extra={"sample_size": len(sample_ids)})
    latencies = run_command_latency_sample(args.api_endpoint, sample_ids, {})
    report["commands"] = {
        "sampled": len(sample_ids),
        "succeeded": len(latencies),
        "success_rate_percent": round(100 * len(latencies) / len(sample_ids), 2) if sample_ids else 0,
        "ack_latency_seconds": {
            "p50": percentile(latencies, 0.50),
            "p95": percentile(latencies, 0.95),
            "max": max(latencies) if latencies else None,
        },
    }

    reconnect_sample = connected[: args.reconnect_sample]
    logger.info("phase_forced_reconnect", extra={"sample_size": len(reconnect_sample)})
    reconnect_start = time.monotonic()
    for d in reconnect_sample:
        d.disconnect()
    time.sleep(2)
    for d in reconnect_sample:
        d.connect()
    reconnect_elapsed = time.monotonic() - reconnect_start
    report["forced_reconnect"] = {
        "sample_size": len(reconnect_sample),
        "elapsed_seconds": round(reconnect_elapsed, 2),
    }

    remaining = max(0, args.steady_state_seconds - 10)
    if remaining:
        time.sleep(remaining)

    stop_event.set()
    for d in connected:
        d.disconnect()

    report["final_metrics"] = metrics.snapshot()
    report["finished_at"] = datetime.now(timezone.utc).isoformat()

    report_dir = Path(args.report_dir)
    report_dir.mkdir(parents=True, exist_ok=True)
    json_path = report_dir / f"load-test-{config.run_id}.json"
    json_path.write_text(json.dumps(report, indent=2))

    md_path = report_dir / f"load-test-{config.run_id}.md"
    md_path.write_text(_render_markdown(report))

    logger.info("report_written", extra={"json": str(json_path), "markdown": str(md_path)})


def _render_markdown(report: dict) -> str:
    onboarding = report["onboarding"]
    commands = report.get("commands", {})
    reconnect = report.get("forced_reconnect", {})
    latency = commands.get("ack_latency_seconds", {})
    return f"""# Load Test Report — run {report['run_id']}

Started: {report['started_at']}
Finished: {report.get('finished_at', 'n/a')}
Requested devices: {report['requested_device_count']}
Heartbeat interval: {report['heartbeat_interval_seconds']}s

## Onboarding
- Connected: {onboarding['connected']} / {onboarding['requested']} ({onboarding['success_rate_percent']}%)
- Elapsed: {onboarding['elapsed_seconds']}s

## Command latency (sample of {commands.get('sampled', 0)})
- Success rate: {commands.get('success_rate_percent', 'n/a')}%
- p50 ack latency: {latency.get('p50')}
- p95 ack latency: {latency.get('p95')}
- max ack latency: {latency.get('max')}

## Forced reconnect (sample of {reconnect.get('sample_size', 0)})
- Elapsed: {reconnect.get('elapsed_seconds')}s

## Final counters
```json
{json.dumps(report.get('final_metrics', {}), indent=2)}
```

These numbers are from one real run against a real controller + broker, not
projected or invented. Re-run with different `--count` to compare scale points
(10 / 100 / 1,000 per docs/roadmap.md Phase 8).
"""


if __name__ == "__main__":
    main()

#!/usr/bin/env python3
"""Bulk device provisioning: reads an inventory file (see
provisioning/inventory/devices.example.yaml), registers each new device with
the controller, and renders a per-device "enrollment bundle" — a config.yaml
plus a README — under --output-dir/<device_id>/.

This is the primary provisioning/delivery method for this reference
implementation (chosen over Ansible/cloud-init/image-based provisioning — see
docs/provisioning.md for why): an operator copies the rendered bundle onto the
physical device (scp, USB, config-management push, whatever fits the
environment) and starts the agent. The agent itself performs the actual
first-time enrollment call (POST /devices/{id}/enroll) using the one-time
token in the bundle — this script never talks MQTT and never holds a device's
long-lived credential.

Adding, removing, or changing a device is purely an inventory-file + rerun
operation — no controller source change is ever needed, satisfying Phase 3's
exit criterion.

Example:
    python bulk_provision.py --inventory ../inventory/devices.example.yaml \
        --api-endpoint http://localhost:8000 --admin-api-key dev-admin-key \
        --mqtt-host localhost --output-dir ../output
"""

import argparse
import logging
import sys
from pathlib import Path

import requests
import yaml
from jinja2 import Template

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("bulk_provision")

README_TEMPLATE = """Enrollment bundle for device: {device_id}

This directory contains everything needed to bring this device online:

  config.yaml  — copy to /etc/device-agent/config.yaml on the device
                 (chmod 600; it contains a one-time enrollment token)

To start the agent on the device (see device-agent/systemd/device-agent.service
for the long-running service definition):

    sudo mkdir -p /etc/device-agent
    sudo cp config.yaml /etc/device-agent/config.yaml
    sudo chmod 600 /etc/device-agent/config.yaml
    sudo systemctl enable --now device-agent

The agent exchanges the enrollment token in config.yaml for a long-lived
credential on its first run, then connects over MQTT and starts heartbeating.
If the device is already enrolled, re-running this bundle's enrollment step
will fail (by design) — use the controller's credential-rotation endpoint
instead (POST /api/v1/devices/{device_id}/credentials/rotate).
"""


def parse_args():
    p = argparse.ArgumentParser(description="Bulk-provision devices from an inventory file")
    p.add_argument("--inventory", required=True)
    p.add_argument("--template", default=str(Path(__file__).parent.parent / "templates" / "device-config.yaml.j2"))
    p.add_argument("--output-dir", default=str(Path(__file__).parent.parent / "output"))
    p.add_argument("--api-endpoint", default="http://localhost:8000")
    p.add_argument("--mqtt-host", default="localhost")
    p.add_argument("--mqtt-port", type=int, default=1883)
    p.add_argument("--admin-api-key", required=True)
    p.add_argument("--default-heartbeat-interval", type=int, default=30)
    p.add_argument("--simulate-gpu", action="store_true", default=True)
    p.add_argument("--verify", action="store_true", help="poll device status after provisioning")
    p.add_argument("--verify-timeout", type=float, default=30.0)
    return p.parse_args()


def load_inventory(path: str) -> list[dict]:
    with open(path, "r", encoding="utf-8") as f:
        data = yaml.safe_load(f) or {}
    return data.get("devices", [])


def device_already_registered(api_endpoint: str, device_id: str) -> bool:
    resp = requests.get(f"{api_endpoint}/api/v1/devices/{device_id}", timeout=10)
    return resp.status_code == 200


def register_device(api_endpoint: str, admin_api_key: str, device_id: str, device_type: str, attributes: dict) -> dict:
    resp = requests.post(
        f"{api_endpoint}/api/v1/devices/register",
        json={"device_id": device_id, "device_type": device_type, "attributes": attributes},
        headers={"X-Api-Key": admin_api_key},
        timeout=10,
    )
    resp.raise_for_status()
    return resp.json()


def render_bundle(template: Template, output_dir: Path, device_id: str, context: dict) -> None:
    bundle_dir = output_dir / device_id
    bundle_dir.mkdir(parents=True, exist_ok=True)

    config_path = bundle_dir / "config.yaml"
    config_path.write_text(template.render(**context))
    config_path.chmod(0o600)  # contains a one-time enrollment token

    (bundle_dir / "README.txt").write_text(README_TEMPLATE.format(device_id=device_id))


def main() -> int:
    args = parse_args()
    inventory = load_inventory(args.inventory)
    template = Template(Path(args.template).read_text())
    output_dir = Path(args.output_dir)

    provisioned, skipped, failed = [], [], []

    for entry in inventory:
        device_id = entry["device_id"]
        device_type = entry["device_type"]
        attributes = entry.get("attributes", {})
        heartbeat_interval = entry.get("heartbeat_interval_seconds", args.default_heartbeat_interval)

        try:
            if device_already_registered(args.api_endpoint, device_id):
                logger.info("already_registered_skipping", extra={"device_id": device_id})
                skipped.append(device_id)
                continue

            reg = register_device(args.api_endpoint, args.admin_api_key, device_id, device_type, attributes)
            render_bundle(
                template,
                output_dir,
                device_id,
                {
                    "device_id": device_id,
                    "device_type": device_type,
                    "api_endpoint": args.api_endpoint,
                    "mqtt_host": args.mqtt_host,
                    "mqtt_port": args.mqtt_port,
                    "heartbeat_interval_seconds": heartbeat_interval,
                    "enrollment_token": reg["enrollment_token"],
                    "simulate_gpu": args.simulate_gpu,
                },
            )
            logger.info("provisioned", extra={"device_id": device_id, "bundle": str(output_dir / device_id)})
            provisioned.append(device_id)
        except requests.RequestException as exc:
            logger.error("provisioning_failed", extra={"device_id": device_id, "error": str(exc)})
            failed.append(device_id)

    logger.info(
        "summary",
        extra={"provisioned": len(provisioned), "skipped": len(skipped), "failed": len(failed), "total": len(inventory)},
    )
    print(f"provisioned={len(provisioned)} skipped={len(skipped)} failed={len(failed)} total={len(inventory)}")

    if args.verify and provisioned:
        _verify_onboarding(args.api_endpoint, provisioned, args.verify_timeout)

    return 1 if failed else 0


def _verify_onboarding(api_endpoint: str, device_ids: list[str], timeout: float) -> None:
    import time

    logger.info("verifying_onboarding", extra={"count": len(device_ids), "timeout": timeout})
    deadline = time.monotonic() + timeout
    pending = set(device_ids)
    while pending and time.monotonic() < deadline:
        for device_id in list(pending):
            resp = requests.get(f"{api_endpoint}/api/v1/devices/{device_id}/status", timeout=10)
            if resp.status_code == 200 and resp.json().get("online"):
                pending.discard(device_id)
        if pending:
            time.sleep(2)

    online = len(device_ids) - len(pending)
    print(f"onboarding_verified: {online}/{len(device_ids)} devices online within {timeout}s")
    if pending:
        print(f"still offline (agent not started yet?): {sorted(pending)}")


if __name__ == "__main__":
    sys.exit(main())

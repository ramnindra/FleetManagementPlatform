"""Shared simulated-device logic used by both simulator.py (smoke tests with a
handful of devices) and load_test.py (hundreds to 1,000 devices). A simulated
device goes through the exact same register -> enroll -> connect -> heartbeat
-> command/ack flow as the real device-agent, just driven over threads instead
of being one process per device, so it's an honest exercise of the controller
and broker.
"""

import json
import logging
import random
import threading
import time
from dataclasses import dataclass, field

import paho.mqtt.client as mqtt
import requests

logger = logging.getLogger(__name__)


class Metrics:
    """Thread-safe counters shared across all simulated devices in a run."""

    def __init__(self):
        self._lock = threading.Lock()
        self.devices_provisioned = 0
        self.devices_provision_failed = 0
        self.devices_connected = 0
        self.devices_connect_failed = 0
        self.heartbeats_sent = 0
        self.commands_received = 0
        self.commands_acked = 0
        self.reconnects = 0

    def incr(self, field_name: str, amount: int = 1) -> None:
        with self._lock:
            setattr(self, field_name, getattr(self, field_name) + amount)

    def snapshot(self) -> dict:
        with self._lock:
            return {k: v for k, v in self.__dict__.items() if not k.startswith("_")}


@dataclass
class SimConfig:
    api_endpoint: str
    mqtt_host: str
    mqtt_port: int
    admin_api_key: str
    heartbeat_interval_seconds: float
    device_type: str = "edge"
    run_id: str = field(default_factory=lambda: f"{int(time.time())}-{random.randint(1000, 9999)}")
    # How long each simulated device waits for its own on_connect callback to
    # fire before giving up. At 1,000 devices this is also a measurement of
    # the SIMULATOR's own scheduling, not just the broker/controller: with
    # one OS thread per device (paho's model), CPython's GIL has to service
    # ~1,000 threads, and a connection that the server accepted promptly can
    # still miss a short client-side wait if this process is behind on
    # scheduling. See docs/load-test-report.md.
    connect_wait_seconds: float = 10.0


class SimulatedDevice:
    def __init__(self, index: int, config: SimConfig, metrics: Metrics):
        self.device_id = f"{config.device_type}-sim-{config.run_id}-{index:05d}"
        self.config = config
        self.metrics = metrics
        self._client: mqtt.Client | None = None
        self._credential: str | None = None
        self._connected_event = threading.Event()
        self._expected_disconnect = False

    def provision(self, timeout: float = 10.0) -> bool:
        try:
            reg = requests.post(
                f"{self.config.api_endpoint}/api/v1/devices/register",
                json={"device_id": self.device_id, "device_type": self.config.device_type},
                headers={"X-Api-Key": self.config.admin_api_key},
                timeout=timeout,
            )
            reg.raise_for_status()
            token = reg.json()["enrollment_token"]

            enroll_resp = requests.post(
                f"{self.config.api_endpoint}/api/v1/devices/{self.device_id}/enroll",
                json={"enrollment_token": token},
                timeout=timeout,
            )
            enroll_resp.raise_for_status()
            self._credential = enroll_resp.json()["device_credential"]
            self.metrics.incr("devices_provisioned")
            return True
        except requests.RequestException as exc:
            logger.warning("provision_failed", extra={"device_id": self.device_id, "error": str(exc)})
            self.metrics.incr("devices_provision_failed")
            return False

    def connect(self) -> bool:
        self._client = mqtt.Client(callback_api_version=mqtt.CallbackAPIVersion.VERSION2, client_id=self.device_id)
        self._client.username_pw_set(self.device_id, self._credential)
        self._client.reconnect_delay_set(min_delay=1, max_delay=30)
        self._client.on_connect = self._on_connect
        self._client.on_disconnect = self._on_disconnect
        self._client.on_message = self._on_message
        try:
            self._client.connect(self.config.mqtt_host, self.config.mqtt_port, keepalive=30)
            self._client.loop_start()
            connected = self._connected_event.wait(timeout=self.config.connect_wait_seconds)
            if connected:
                self.metrics.incr("devices_connected")
                return True

            # Without this, a client that never connected keeps retrying
            # forever in its background thread (reconnect_delay_set already
            # has it looping) — at scale, hundreds of these zombie clients
            # kept hammering EMQX's auth webhook pool for the rest of the
            # test, which is almost certainly why command ack latency looked
            # broken in an earlier 1,000-device run even though the acks
            # were actually arriving (see docs/load-test-report.md).
            self._client.loop_stop()
            self._client.disconnect()
            self.metrics.incr("devices_connect_failed")
            return False
        except (ConnectionRefusedError, OSError) as exc:
            logger.warning("connect_failed", extra={"device_id": self.device_id, "error": str(exc)})
            self.metrics.incr("devices_connect_failed")
            return False

    def _on_connect(self, client, userdata, flags, reason_code, properties=None):
        # Must check this: paho calls on_connect for a REJECTED connection
        # too (e.g. a failed auth), just with a non-success reason_code. Only
        # setting the event on a real success is what makes
        # devices_connected/devices_connect_failed trustworthy counters.
        if reason_code != 0 and str(reason_code) != "Success":
            return
        client.subscribe(f"devices/{self.device_id}/cmd", qos=1)
        self._connected_event.set()

    def _on_disconnect(self, client, userdata, flags, reason_code, properties=None):
        # Only count unplanned drops as "reconnects" — an intentional
        # disconnect() call (forced-reconnect test, shutdown) sets this flag
        # first so normal teardown doesn't inflate the metric.
        if self._connected_event.is_set() and not self._expected_disconnect:
            self.metrics.incr("reconnects")
        self._expected_disconnect = False
        self._connected_event.clear()

    def _on_message(self, client, userdata, msg):
        try:
            payload = json.loads(msg.payload.decode("utf-8"))
        except (json.JSONDecodeError, UnicodeDecodeError):
            return
        self.metrics.incr("commands_received")
        command_id = payload.get("command_id")
        ack = {"command_id": command_id, "status": "success", "result": {"simulated": True}}
        client.publish(f"devices/{self.device_id}/cmd/ack", json.dumps(ack), qos=1)
        self.metrics.incr("commands_acked")

    def send_heartbeat(self) -> None:
        if self._client is None:
            return
        self._client.publish(f"devices/{self.device_id}/heartbeat", json.dumps({"status": "healthy", "ts": time.time()}), qos=1)
        self.metrics.incr("heartbeats_sent")

    def disconnect(self) -> None:
        if self._client is not None:
            self._expected_disconnect = True
            self._client.loop_stop()
            self._client.disconnect()

    def heartbeat_loop(self, stop_event: threading.Event) -> None:
        # Jitter avoids every device's heartbeat landing in the same instant,
        # which matters once you have hundreds of them on the same interval.
        time.sleep(random.uniform(0, self.config.heartbeat_interval_seconds))
        while not stop_event.is_set():
            self.send_heartbeat()
            stop_event.wait(self.config.heartbeat_interval_seconds)


def provision_and_connect(device: SimulatedDevice) -> bool:
    if not device.provision():
        return False
    return device.connect()

#!/usr/bin/env python3
"""Spins up N simulated devices against a running controller + broker.

Used for small-scale smoke/integration runs (10-100 devices). For the
1,000-device scale test with full metrics and a report, use load_test.py
instead — this script just proves end-to-end connectivity.

Example:
    python simulator.py --count 10 --api-endpoint http://localhost:8000 \
        --mqtt-host localhost --admin-api-key dev-admin-key
"""

import argparse
import logging
import signal
import sys
import threading
import time
from concurrent.futures import ThreadPoolExecutor

from common import Metrics, SimConfig, SimulatedDevice, provision_and_connect

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
logger = logging.getLogger("simulator")


def parse_args():
    p = argparse.ArgumentParser(description="Simulate N devices talking to the controller")
    p.add_argument("--count", type=int, default=10)
    p.add_argument("--device-type", default="edge", choices=["switch", "gpu", "edge"])
    p.add_argument("--api-endpoint", default="http://localhost:8000")
    p.add_argument("--mqtt-host", default="localhost")
    p.add_argument("--mqtt-port", type=int, default=1883)
    p.add_argument("--admin-api-key", default="dev-admin-key")
    p.add_argument("--heartbeat-interval", type=float, default=10.0)
    p.add_argument("--duration-seconds", type=float, default=60.0, help="0 = run until Ctrl-C")
    return p.parse_args()


def main():
    args = parse_args()
    config = SimConfig(
        api_endpoint=args.api_endpoint,
        mqtt_host=args.mqtt_host,
        mqtt_port=args.mqtt_port,
        admin_api_key=args.admin_api_key,
        heartbeat_interval_seconds=args.heartbeat_interval,
        device_type=args.device_type,
    )
    metrics = Metrics()
    devices = [SimulatedDevice(i, config, metrics) for i in range(args.count)]

    logger.info("provisioning_and_connecting", extra={"count": args.count})
    with ThreadPoolExecutor(max_workers=min(50, args.count)) as pool:
        results = list(pool.map(provision_and_connect, devices))

    connected = [d for d, ok in zip(devices, results) if ok]
    logger.info("connected_summary", extra={"connected": len(connected), "requested": args.count})
    if not connected:
        logger.error("no_devices_connected")
        sys.exit(1)

    stop_event = threading.Event()
    signal.signal(signal.SIGINT, lambda *_: stop_event.set())
    signal.signal(signal.SIGTERM, lambda *_: stop_event.set())

    threads = [threading.Thread(target=d.heartbeat_loop, args=(stop_event,), daemon=True) for d in connected]
    for t in threads:
        t.start()

    start = time.time()
    try:
        while not stop_event.is_set():
            if args.duration_seconds and (time.time() - start) > args.duration_seconds:
                break
            time.sleep(2)
            logger.info("metrics", extra=metrics.snapshot())
    finally:
        stop_event.set()
        for d in connected:
            d.disconnect()

    logger.info("final_metrics", extra=metrics.snapshot())


if __name__ == "__main__":
    main()

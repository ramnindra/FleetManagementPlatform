# Cloud Device Controller

A reference implementation of a cloud-hosted controller for managing 1,000+
network-connected devices (switches, GPU nodes, generic Linux edge devices):
provisioning, secure device identity, commands/acks, Kubernetes deployment,
AWS infrastructure as code, CI/CD + GitOps, load testing, and monitoring.

**All 10 phases are complete** — see `docs/roadmap.md` for exactly what was
built and verified in each, including every real bug found and fixed along
the way (nothing in this repo was written and left untested). Start with
`docs/architecture.md` for requirements, technology trade-offs, and
diagrams, then `docs/final-demo.md` for a fully-executed walkthrough of the
system end to end.

## Documentation map

| Doc | Covers |
|---|---|
| `docs/architecture.md` | Requirements, technology decisions (why MQTT/EMQX, why Go, why Postgres...), every required diagram |
| `docs/roadmap.md` | Phase-by-phase status, what was verified and how |
| `docs/provisioning.md` | Bootstrap trust model, bulk onboarding, credential rotation/revocation |
| `docs/deployment.md` | Local Compose + Kubernetes/Helm + Terraform/AWS, with real bugs found deploying each |
| `docs/cicd.md` | CI/CD pipeline design, GitOps promotion/rollback model, required repo setup |
| `docs/load-test-report.md` | Real 10/50/100-device numbers, 3 real bottlenecks found and fixed chasing 1,000 |
| `docs/operations.md` | Monitoring, operational commands, backup/restore, credential rotation, security checklist |
| `docs/troubleshooting.md` | Every real incident hit while building this, indexed by symptom |
| `docs/final-demo.md` | All 20 steps of the final demo, each actually run — not described |

## Quickstart (local, no AWS required)

```bash
make setup      # downloads Go modules for all services and tools
make test       # runs controller + device-agent Go test suites
make local-up   # docker compose up: postgres + emqx + controller
make demo       # provisions devices, starts real agents, sends commands, shows results
make provision  # register the example inventory and render enrollment bundles
make simulate   # spins up 10 simulated devices for 60s
make local-down # tears the stack down
```

Once `make local-up` reports ready:
- Controller web UI: http://localhost:8000/ui/
- EMQX dashboard: http://localhost:18083 (default login `admin` / `public`)

### Provision a device, issue a command, inspect the response

```bash
# 1. Register a device (admin operation)
curl -s -X POST http://localhost:8000/api/v1/devices/register \
  -H "X-Api-Key: dev-admin-key" -H "Content-Type: application/json" \
  -d '{"device_id": "gpu-node-001", "device_type": "gpu"}'
# -> {"device_id": "gpu-node-001", "enrollment_token": "...", ...}

# 2. Enroll (the device would do this itself using the token from step 1)
curl -s -X POST http://localhost:8000/api/v1/devices/gpu-node-001/enroll \
  -H "Content-Type: application/json" \
  -d '{"enrollment_token": "<token from step 1>"}'
# -> {"device_credential": "...", "mqtt_host": "localhost", "mqtt_port": 1883, ...}

# 3. Put that credential + config in device-agent/config.example.yaml, then:
cd device-agent && AGENT_CONFIG_PATH=./config.example.yaml go run ./cmd/device-agent

# 4. In another terminal, send it a command and watch it get acknowledged
curl -s -X POST http://localhost:8000/api/v1/devices/gpu-node-001/commands \
  -H "Content-Type: application/json" -d '{"action": "ping"}'
# -> {"command_id": "...", "status": "delivered"}
curl -s http://localhost:8000/api/v1/commands/<command_id>
# -> {"status": "success", "result": {"pong": true, ...}}

# 5. Check device status
curl -s http://localhost:8000/api/v1/devices/gpu-node-001/status
```

For onboarding many devices at once instead of one at a time, see
`docs/provisioning.md` and `provisioning/cmd/bulk-provision`.

### Beyond one device

```bash
make monitoring-up           # Prometheus :9090 + Grafana :3000, scraping the controller live
make load-test COUNT=50      # real multi-device scale test, see docs/load-test-report.md
make k8s-deploy               # deploy to a local kind cluster via Helm
```

## Understanding the CI/CD deployment flow

`docs/cicd.md` is the full explanation; in short: a merge to `main` triggers
`.github/workflows/build-scan-push.yml` (build → Trivy scan → push to ECR
with an immutable commit-SHA tag → commit that digest to
`gitops/dev/image-values.yaml`). Argo CD watches `gitops/<env>/` and the
Helm chart in `kubernetes/helm/device-controller/` and syncs automatically
— it only ever reads from Git, CI only ever writes to Git, so there is
nothing bidirectional to conflict over. `promote-staging.yml` smoke-tests
dev, then promotes the *same* digest (never a rebuild) to staging and runs
a multi-device E2E check there. `promote-prod.yml` requires a manual
approval (a GitHub Environment setting, not expressible in the workflow
YAML itself) before promoting that same digest to production. `docs/final-demo.md`
walks this exact flow with real commands and output, substituting a local
equivalent only where this environment has no GitHub remote / AWS
credentials / Argo CD installed — clearly labeled wherever that happens.

## Repository layout

See `docs/roadmap.md` for the full layout and phase-by-phase plan.

## Why MQTT? Why Go? Why self-hosted EMQX instead of AWS IoT Core? Why SHA-256 instead of bcrypt for device credentials?

All covered with trade-off tables and (for the bcrypt question) real load-test
data in `docs/architecture.md` section B and `docs/load-test-report.md`.

## Simulator on a Kubernetes cluster (nodes appear in the web UI)

```bash
scripts/k8s-up.sh --fresh                  # kind cluster + controller/EMQX/Postgres, zero devices
open http://localhost:8000/ui/             # empty device list

scripts/simulate.sh --count 5              # nodes appear in the UI; instructions print here
scripts/simulate.sh --config simulator/simulator.example.yaml --count 3
scripts/simulate.sh --cluster 10.0.0.5 --device-type gpu --count 20
```

Pick a node in the UI, send a command (`ping`, `get_status`, `get_system_info`,
`update_config`), and the simulator prints each instruction (`← INSTRUCTION`)
and the result it returned. `update_config` with `heartbeat_interval_seconds`
changes that node's heartbeat rate live. Parameters (cluster address, node
count, device types/mix, heartbeat interval, duration, ...) can be given as
flags or in a YAML config file; flags override the file. `scripts/k8s-fleet.sh 50`
deploys and loads 50 devices with a report; `scripts/k8s-up.sh down` removes the cluster.

### More simulator options

```bash
# Misbehave on purpose: 10% of commands fail, acks delayed up to 1s, nodes drop
# off for 10s about every 2 minutes (also settable in the YAML config)
scripts/simulate.sh --count 20 --failure-rate 0.1 --ack-delay 1s --churn-interval 2m

# Run the fleet inside the cluster instead of on your laptop
scripts/k8s-simulator.sh 20 --set simulator.deviceTypes.gpu=5
scripts/k8s-simulator.sh logs              # instructions from the web UI appear here
scripts/k8s-simulator.sh down
```

The web UI's home view lists the latest instructions across the whole fleet
(params, status, round-trip time, and the device's response); selecting a device
shows the same for that node. Nodes report type-specific synthetic telemetry
(switch ports/throughput, GPU utilization/temperature/power, edge CPU/memory).

### What each node actually received

Each device's page has a **Messages received by this node** table: when the
controller sent each instruction, when the *node itself* says it received it
(the node stamps `received_at` in its ack), the params as received, and the
send-to-receive latency. An instruction the broker accepted but the node never
confirmed shows as "queued — not confirmed by node" (e.g. the node is offline).
The simulator prints the same thing on its console as `← INSTRUCTION`.

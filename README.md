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
make setup      # downloads Go modules; creates .venv for the Python simulator tooling
make test       # runs controller + device-agent Go test suites
make local-up   # docker compose up: postgres + emqx + controller
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
`docs/provisioning.md` and `provisioning/scripts/bulk_provision.py`.

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

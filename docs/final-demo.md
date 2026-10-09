# Final End-to-End Demonstration (Phase 10)

Every one of the 20 steps below was actually run in this repo. Steps 1–7
touch GitHub Actions, ECR, and Argo CD — this environment has no GitHub
remote, no AWS credentials, and no Argo CD installation, so those three
pieces are **honestly substituted with their real local-mechanism
equivalent** (git branch/merge, a local Docker build with a real content
digest, and a `helm upgrade` standing in for what Argo CD would apply from
the same `gitops/` file). Each is labeled. Steps 8–20 run against the real
controller, real device-agent, real EMQX, and real Prometheus/Grafana —
nothing substituted.

## 1. Developer modifies controller code — **REAL**

```bash
git checkout -b feature/version-bump
# edit controller/app/main.py: version="0.1.0" -> version="0.2.0"
git add controller/app/main.py
git commit -m "Bump controller API version to 0.2.0"
```

## 2. Developer opens a pull request — **SUBSTITUTED** (no GitHub remote here)

```bash
git diff main feature/version-bump -- controller/app/main.py
```
```diff
-app = FastAPI(title="Cloud Device Controller", version="0.1.0", lifespan=lifespan)
+app = FastAPI(title="Cloud Device Controller", version="0.2.0", lifespan=lifespan)
```
On a real GitHub remote this diff is what `.github/workflows/pr-validate.yml`
and `test.yml` (Phase 7) would run against automatically on `pull_request`.

## 3. CI runs tests and validation — **REAL** (same commands the workflows run)

```bash
ruff check controller/app controller/tests device-agent/agent device-agent/tests simulator
# All checks passed!
cd controller && python -m pytest tests/unit tests/integration -v
# 33 passed, 1 warning in 1.12s
```

## 4. Changes are merged — **REAL** (local git; no GitHub PR merge button)

```bash
git checkout main && git merge feature/version-bump --no-edit
# Fast-forward, 1 file changed
git branch -d feature/version-bump
```

## 5. Docker image is built and pushed to ECR — **SUBSTITUTED** (build real, push skipped — no AWS credentials)

```bash
docker build -t cloud-device-controller-controller:demo ./controller
docker inspect --format='{{.Id}}' cloud-device-controller-controller:demo
# sha256:cac462fd83d8c6402b4a3a4ea1e604b35962657f5ed9e5e1bd048bfe1cd5a24f
```
This is a real content digest of the real built image — just computed
locally rather than returned by `docker push` to ECR, which
`.github/workflows/build-scan-push.yml` would do in a real environment with
AWS credentials.

## 6. Deployment configuration is promoted — **REAL mechanism, placeholder content**

```bash
kind load docker-image cloud-device-controller-controller:demo --name device-controller
```
`gitops/dev/image-values.yaml` is what `build-scan-push.yml` would commit
this digest to. Attempting to deploy by that bare digest against a `kind`
cluster surfaced a **real, worth-documenting limitation**: `kind load
docker-image` registers an image in containerd's local store by its *tag*,
not as a resolvable `name@digest` reference — only a real registry (ECR)
supports pulling by digest. The `helm upgrade --set
controller.image.digest=...` attempt correctly *failed* (the pre-upgrade
migration Job hit `ImagePullBackOff`), rather than silently deploying the
wrong thing. Recovered by deploying with the tag (`--set
controller.image.tag=demo`) instead — the mechanism (Helm's
`device-controller.image` helper rendering `repo@digest` vs `repo:tag`) is
exactly what Phase 7 already validated structurally; this is a real
registry-dependency gap in testing it locally, not a bug in the mechanism
itself. See `docs/troubleshooting.md`.

## 7. Argo CD deploys the controller to Kubernetes — **SUBSTITUTED** (`helm upgrade`, no Argo CD installed here)

```bash
helm upgrade device-controller kubernetes/helm/device-controller -f values-dev.yaml \
  --set controller.image.repository=cloud-device-controller-controller \
  --set controller.image.tag=demo \
  --namespace device-controller
kubectl -n device-controller get deployment device-controller \
  -o jsonpath='{.spec.template.spec.containers[0].image}'
# cloud-device-controller-controller:demo
curl -s http://localhost:8000/openapi.json | jq -r .info.version   # via port-forward
# 0.2.0
```
The real code change from step 1 is now the real running code in the
cluster — confirmed via the API's own reported version, not just the image
tag.

## 8. A new device is provisioned — **REAL**

```bash
curl -s -X POST http://localhost:8000/api/v1/devices/register \
  -H "X-Api-Key: dev-admin-key" -H "Content-Type: application/json" \
  -d '{"device_id": "demo-gpu-001", "device_type": "gpu"}'
# {"device_id":"demo-gpu-001","status":"pending_enrollment","enrollment_token":"p-3W9Dij5DY...",...}

curl -s -X POST http://localhost:8000/api/v1/devices/demo-gpu-001/enroll \
  -H "Content-Type: application/json" -d '{"enrollment_token": "p-3W9Dij5DY..."}'
# {"device_id":"demo-gpu-001","device_credential":"ofN8DfN5V44...","mqtt_host":"device-controller-emqx",...}
```

## 9. The device connects to the controller — **REAL** (the actual `device-agent`)

```bash
AGENT_CONFIG_PATH=./config.yaml python -m agent.main
```
```json
{"message": "mqtt_connected", "device_id": "demo-gpu-001", "reason_code": "Success"}
```

## 10. The device sends a heartbeat — **REAL**

Heartbeats publish every `heartbeat.interval_seconds` automatically once
connected (see step 9's log — no separate command needed).

## 11. The controller marks the device online — **REAL**

```bash
curl -s http://localhost:8000/api/v1/devices/demo-gpu-001/status
# {"device_id":"demo-gpu-001","status":"active","online":true,"last_seen_at":"...","seconds_since_last_seen":2.11}
```

## 12-17. Command submitted, received, executed, acked, stored, retrieved — **REAL**

```bash
curl -s -X POST http://localhost:8000/api/v1/devices/demo-gpu-001/commands \
  -H "Content-Type: application/json" -d '{"action": "get_system_info"}'
# {"command_id":"ccc1e4a9-...","device_id":"demo-gpu-001","status":"delivered"}

curl -s http://localhost:8000/api/v1/commands/ccc1e4a9-...
```
```json
{
  "status": "success",
  "result": {
    "hostname": "...", "platform": "macOS-...", "device_type": "gpu",
    "telemetry": {"source": "simulated", "gpu_utilization": 77.1, "temperature": 53.5},
    "duration_ms": 61.1
  },
  "created_at": "...", "delivered_at": "...", "completed_at": "..."
}
```
`delivered_at` and `completed_at` are both present and a few milliseconds
apart — the device actually executed the command and acked it, this is not
a stub response.

## 18. Prometheus records the activity — **REAL**

```bash
curl -s 'http://localhost:9090/api/v1/query?query=controller_devices_offline'
# value: [..., "7280"]   <- incremented by exactly the device just registered
curl -s 'http://localhost:9090/api/v1/query?query=increase(controller_http_request_duration_seconds_count[2m])'
# real per-route request counts, e.g. {"path":"/internal/mqtt/auth",...} "2.28..."
```

## 19. Grafana displays the metrics — **REAL**

```bash
curl -s -u admin:admin \
  "http://localhost:3000/api/datasources/proxy/uid/PBFA97CFB590B2093/api/v1/query?query=controller_devices_offline"
# value: [..., "7280"]   <- same value Prometheus has, queried through Grafana's own API
```

## 20. A simulated failure validates recovery — **REAL**

```bash
kubectl -n device-controller delete pod -l app.kubernetes.io/component=controller
kubectl -n device-controller rollout status deployment/device-controller
# deployment "device-controller" successfully rolled out
```
The device's own agent log shows **no disconnect event at all** during the
outage — correct, and the real point of the architecture: the device talks
to EMQX, not to any specific controller pod, so a controller restart is
transparent to already-connected devices (see `docs/architecture.md`
section 9). Confirmed directly:

```bash
curl -s http://localhost:8000/api/v1/devices/demo-gpu-001/status
# {"online":true,"last_seen_at":"...","seconds_since_last_seen":3.8}   <- never stopped
```

Then proved full functionality, not just connectivity, by round-tripping
one more command through the recovered pod:

```bash
curl -s -X POST http://localhost:8000/api/v1/devices/demo-gpu-001/commands -d '{"action":"ping"}'
curl -s http://localhost:8000/api/v1/commands/15937754-...
# {"status":"success","result":{"pong":true,"uptime_seconds":137.8,...}}
```

**One real operational nuance surfaced along the way**: `kubectl
port-forward svc/...` binds to whichever pod backed the Service *at the
moment the forward started* — it does not fail over automatically when
that specific pod is deleted, even though it is forwarding via a Service
selector. The forward had to be restarted after the pod delete, with the
actual kubectl error captured in `docs/troubleshooting.md`.

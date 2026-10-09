# Troubleshooting (Phase 9)

Every entry below is a real failure actually hit and fixed while building and
load-testing this system — not a generic checklist. Full narratives are in
`docs/deployment.md` and `docs/load-test-report.md`; this is the
symptom-first index.

## Device communication failures

### Devices fail to connect, EMQX logs show `http_connector_do_request_failed, reason: timeout`

**Cause**: EMQX's auth/ACL webhook HTTP connection pool (`pool_size` in
`docker/emqx/emqx.conf` or the Helm chart's `emqx-configmap.yaml`) is too
small for the current connection rate. The EMQX default is 8 — far too few
for anything past a handful of simultaneous device connections.

**Diagnose**: `docker compose logs emqx | grep http_connector_do_request_failed`
(or `kubectl logs` the EMQX pod in k8s).

**Fix**: raise `pool_size` (currently 64 in both configs) and
`request_timeout`. If it recurs at a larger fleet size, raise further before
assuming anything else is wrong.

### Devices connect but commands never get acked; controller logs show `RuntimeError: ... transport closed ... handler is closed`

**Cause**: the controller took long enough to respond to a request that the
caller (EMQX's HTTP connector, with its own `request_timeout`) gave up and
closed the connection before the controller's handler finished — so when the
handler does finish, it tries to write to an already-closed transport.

**Diagnose**: this almost always means the controller itself is the
bottleneck, not EMQX or the network. Check `docker stats` / `kubectl top pod`
for the controller — if CPU is pegged, find what route is slow before
touching any timeout value.

**A specific case actually hit**: `bcrypt.checkpw` on every device
authentication, at scale, pegged the controller at 1200%+ CPU (see
`docs/load-test-report.md`). Fixed by recognizing that device credentials
are always high-entropy random tokens, never human passwords, so bcrypt's
deliberate slowness was pure cost with no corresponding security benefit —
switched to SHA-256 + constant-time comparison.

### `sqlalchemy.exc.TimeoutError: QueuePool limit of size N overflow M reached`

**Cause**: more concurrent DB-touching requests than the connection pool has
room for — a provisioning burst (many `register`/`enroll` calls at once) is
the most likely trigger, since each one holds a connection for its duration.

**Fix**: raise `CONTROLLER_DB_POOL_SIZE` / `CONTROLLER_DB_MAX_OVERFLOW` (see
`app/core/config.py` for the current defaults and the Postgres
`max_connections` trade-off for multi-replica deployments). Also check
anyio's thread-pool limiter is set to match (`app/main.py` lifespan) — it is
the very next bottleneck once the DB pool is no longer the limit, since every
route handler here is a synchronous `def`.

### A device never comes online, no errors anywhere

**Checklist, in order**:
1. `GET /api/v1/devices/{id}/status` — does the controller even think the
   device is registered/enrolled? If `status` is `pending_enrollment`, the
   device never completed enrollment — check its agent logs for the
   `enrollment_failed` event.
2. Agent logs (`journalctl -u device-agent` or the container's stdout) —
   look for `mqtt_connected` with a `reason_code` other than `Success`
   (`Not authorized` means the credential is wrong or the device was
   revoked; anything else is a broker-level rejection).
3. If the agent shows no `mqtt_connected` event at all (not even a failed
   one), it's a network-reachability problem, not an application one — check
   the device can reach `controller.mqtt_host:mqtt_port` at all (firewall,
   DNS, the device's outbound rule — see `docs/architecture.md` section C.8).

## Failed deployments

### Fresh `helm install` hangs, then fails with `failed post-install: resource Job/... not ready`

**Cause**: the migration Job (hooked as `post-install,pre-upgrade`, see
`kubernetes/helm/device-controller/templates/migration-job.yaml`) is stuck,
usually `ImagePullBackOff` because the Job's `imagePullPolicy` wasn't set
explicitly and Kubernetes defaults an untagged/`:latest` image reference to
`Always` — which then tries to pull an image that only exists locally (e.g.
loaded into `kind` but never pushed to a registry).

**Diagnose**: `kubectl get jobs -n device-controller` /
`kubectl describe pod -l job-name=... -n device-controller`.

**Fix**: confirm `imagePullPolicy` is set on every container that uses the
locally-built image (both the controller Deployment and the migration Job
need it — this was missed on the Job specifically once, see
`docs/deployment.md`).

### Controller pod `CrashLoopBackOff`, logs show `Application startup failed. Exiting.`

**Cause family**: a *blocking* call during FastAPI's `lifespan` startup
(MQTT connect, `create_all()`, anything synchronous) raised because its
target (EMQX, Postgres) was not yet reachable — a startup *race*, not a
permanent failure. This exact pattern was hit three separate times while
building Phase 5 — see `docs/deployment.md` for all three. If you add new
startup-time I/O, assume it needs the same retry/async treatment, don't
assume the dependency will always be up in time.

**Fix pattern**: never let lifespan startup code raise synchronously on a
transient connection failure. Use `connect_async()` instead of `connect()`
for MQTT (defers to the retrying background thread), and a bounded retry
loop for anything that must succeed before serving traffic (schema
creation).

### Controller pod `Running` but never becomes `Ready`, Service has zero Endpoints

**Cause**: this was a genuine deadlock, not a timing issue — `/readyz` used
to also check this pod's own MQTT connection state, but EMQX's auth webhook
calls back into the controller *through its own Service*, which only routes
to pods that have already passed readiness. The pod could never become Ready
(needed MQTT connected) because MQTT could never authenticate (EMQX's
webhook call to the not-yet-Ready pod never got routed anywhere).

**Diagnose**: `kubectl get endpoints device-controller -n device-controller`
— zero endpoints despite a `Running` pod is the tell. `kubectl exec` into
the EMQX pod and `curl` the controller's internal auth endpoint directly to
confirm it is a routing/readiness problem, not an application bug.

**Fix**: already applied — `/readyz` only checks the database now, never
this pod's own MQTT state (see `app/routers/health.py`'s docstring for the
full reasoning). If you are tempted to add another "is X connected" check to
`/readyz`, check first whether X's own health depends on routing *through*
this Service — if so, it will deadlock the same way.

### `terraform init` fails with a provider version conflict

**A specific case actually hit**: `terraform-aws-modules/eks//21.x`'s
submodules require AWS provider `>= 6.0`, while the module's own published
examples at the time still showed `~> 5.0`. `terraform init` is the ground
truth here, not the module's docs — if it fails on a version constraint,
widen the root's `required_providers` pin to what `init` actually reports
needing (see `infrastructure/terraform/versions.tf`'s comment).

### `helm upgrade --set controller.image.digest=...` fails with the migration Job stuck in `ImagePullBackOff`

**Cause**: `kind load docker-image` registers an image in containerd's local
store by its *tag*, not as a resolvable `name@digest` reference — only a
real registry (ECR) supports pulling an image by bare digest. A digest that
was never pushed anywhere simply is not pullable, by tag or otherwise, from
a `kind` node.

**Fix**: this is only a limitation of testing the digest-pinning mechanism
against a purely local `kind`-loaded image — the mechanism itself (the
Helm chart's `device-controller.image` helper rendering `repo@digest` vs.
`repo:tag`) is correct and already validated by `kubeconform` in Phase 7.
Against a real ECR-hosted image, digest references resolve normally. For a
local-only test, deploy by tag instead
(`--set controller.image.tag=<tag>`), clean up the stuck Job first:
`kubectl delete job <name> -n device-controller`.

### `kubectl port-forward svc/...` silently stops working after a pod restart

**Cause**: `port-forward` to a Service binds to whichever specific pod
backed it *at the moment the forward started* — it does not fail over
automatically if that pod is later deleted/replaced, even though it
initially resolved via the Service's selector. You'll see
`error: lost connection to pod` (check the forward's own log/stderr; it's
easy to miss if run in the background).

**Fix**: just restart the `port-forward` command — it will re-resolve to
whichever pod is currently backing the Service.

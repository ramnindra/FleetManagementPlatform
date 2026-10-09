# Operations (Phase 9)

## Monitoring

```bash
make monitoring-up    # Prometheus :9090, Grafana :3000 (anonymous viewer access on)
```

**Verified live in this repo**: brought the stack up, confirmed Prometheus's
`device-controller` scrape target reports `up`, confirmed all 5 alert rules
in `monitoring/prometheus/alerts.yml` loaded, confirmed Grafana's
file-provisioned datasource and the "Device Controller" dashboard both
appeared automatically with no manual UI steps, and confirmed the full chain
end-to-end — queried a real metric (`controller_devices_online`) through
Grafana's own datasource-proxy API and got back real data, not a mock.

### What the dashboard shows
`monitoring/grafana/dashboards/device-controller.json`: devices online/offline
(stat panels, computed fresh from the DB on every Prometheus scrape — see the
`DeviceStatusCollector` note below), fleet online %, command ack success vs.
failure rate, heartbeat rate per device, command publish/ack rate, and API
latency p50/p95 by route.

### What it does not show (yet)
Kubernetes pod health/CPU/memory (would come from kube-state-metrics/cAdvisor,
a cluster-wide add-on, not this app) and EMQX's own connection/session metrics
(EMQX has a native Prometheus endpoint; wiring it in needs EMQX dashboard API
auth not yet set up here). Both are reasonable follow-ups, not implemented —
said plainly rather than quietly dropped.

### Why `controller_devices_online` is a custom Collector, not `Gauge.set()`
A first version defined a plain `Gauge` and never actually called `.set()`
on it anywhere — a real gap only caught while building this phase (see
`app/services/metrics.py`). "Online" is derived from `last_seen_at` age, not
an increment/decrement counter, so it has to be computed, not tracked — a
custom `Collector` recomputes it from the DB fresh on every scrape. Fixing
this also surfaced a second real bug: `Registry.register()` calls a new
collector's `collect()` once immediately, before the app has necessarily
created its schema — the same startup-race family already hit twice
elsewhere in this app (see `docs/deployment.md`). `collect()` now catches
that and emits 0/0 rather than crashing.

### Alerting rules (`monitoring/prometheus/alerts.yml`)
| Alert | Fires when | What it actually means |
|---|---|---|
| `ControllerDown` | scrape target down 1m | Controller pod(s) unreachable |
| `HighCommandFailureRate` | >10% of acks are `failed` for 5m | Devices reachable but rejecting/failing commands |
| `NoCommandAcksButCommandsPublished` | commands published, zero acks, 10m | Broker→controller ack delivery broken (EMQX down, or controller's MQTT client disconnected) |
| `HighAPILatency` | p95 > 500ms on any route for 5m | Check DB pool exhaustion first — this exact failure mode is documented with real numbers in `docs/load-test-report.md` |
| `DeviceFleetMostlyOffline` | <50% of registered devices online, 5m | Check EMQX health and the controller's own MQTT connection before investigating individual devices |

## Operational commands

```bash
kubectl -n device-controller get pods
kubectl -n device-controller logs deployment/device-controller
kubectl -n device-controller describe pod <pod>
kubectl -n device-controller rollout status deployment/device-controller
kubectl -n device-controller rollout undo deployment/device-controller
helm -n device-controller list
argocd app get device-controller-prod
```

**`kubectl rollout undo` vs. a durable GitOps rollback** — these are not the
same operation, and conflating them causes real incidents:
`rollout undo` only reverts the Deployment's pod template to the previous
ReplicaSet; it does not touch `gitops/<env>/image-values.yaml`, so the very
next Argo CD reconcile (triggered by anything, not necessarily a new deploy)
silently re-applies the "bad" version from Git, undoing your `rollout undo`.
The durable fix is a `git revert` of the commit that updated
`gitops/<env>/image-values.yaml` — see `docs/cicd.md` for the full promotion
and rollback model.

## Backup and restore

- **RDS (staging/prod)**: automated backups are governed by
  `db_backup_retention_days` in `infrastructure/terraform/environments/<env>/terraform.tfvars`
  (7 days staging, 30 days prod). To restore: `aws rds restore-db-instance-to-point-in-time`
  or restore-from-snapshot into a *new* instance, point `CONTROLLER_DATABASE_URL`
  (via Secrets Manager) at it, never restore in place over the live instance.
- **Local dev (Postgres in Docker Compose)**: not backed up —
  `docker compose down -v` deletes it, by design (see `docker-compose.yml`).
  For a throwaway manual backup: `docker compose exec postgres pg_dump -U controller controller > backup.sql`.
- **Terraform state**: versioned in the S3 bucket created by
  `infrastructure/terraform/bootstrap` (`aws_s3_bucket_versioning`) — restore
  a prior state version via the S3 object version, not by hand-editing state.

## Credential rotation

- **Per-device credentials**: `POST /api/v1/devices/{id}/credentials/rotate`
  — see `docs/provisioning.md` for the full flow and its one documented gap
  (a brief offline window between rotation and re-enrollment).
- **Infrastructure secrets** (`admin_api_key`, `mqtt_webhook_shared_secret`,
  `mqtt_client_password` — all in `infrastructure/terraform/modules/app/main.tf`
  as `random_password` resources, delivered via Secrets Manager + the Helm
  chart's `ExternalSecret`): rotating these means a new `terraform apply`
  (which regenerates the `random_password` value — use
  `terraform taint module.app.random_password.admin_api_key` first, since
  Terraform otherwise has no reason to regenerate an already-applied random
  value), then either wait for the `ExternalSecret`'s `refreshInterval: 1h`
  or force it with `kubectl annotate externalsecret device-controller-secrets
  force-sync=$(date +%s) --overwrite -n device-controller`, then
  `kubectl rollout restart deployment/device-controller statefulset/device-controller-emqx`
  — neither picks up a changed Secret without a restart, since both read it
  as env vars / a rendered file at process start, not via a hot-reload path.

## Security validation checklist

Cross-checking what `docs/architecture.md` section 12 claims against what is
actually implemented, as of this phase:

| Control | Implemented | Where |
|---|---|---|
| TLS for external traffic | Partial — dev uses plaintext MQTT/HTTP by design; staging/prod Ingress is TLS via ALB | `values-staging.yaml`/`values-prod.yaml` |
| Device-specific identity + credential | Yes | `device_service.py`, SHA-256 + constant-time compare (see `docs/load-test-report.md` for why not bcrypt) |
| Credential rotation + revocation | Yes | `docs/provisioning.md` |
| API authentication | Yes (static admin key — explicitly a dev-shortcut, not OAuth/IAM) | `core/deps.py` |
| Least-privilege cloud IAM | Yes | External-Secrets IRSA role scoped to 4 specific secret ARNs, not `secretsmanager:*` |
| K8s ServiceAccount permissions | Yes — deliberately zero-permission | `controller-rbac.yaml` |
| Secret storage | Yes (dev: Helm values; staging/prod: Secrets Manager via ExternalSecret) | `controller-secret.yaml` / `controller-externalsecret.yaml` |
| Network segmentation | Yes | `networkpolicy.yaml` (not enforced by kind's default CNI — see `docs/deployment.md`) |
| Container security context | Yes — non-root, read-only rootfs, no privilege escalation, all capabilities dropped | `controller-deployment.yaml` |
| Audit logging | Partial | Structured JSON logs cover every state-changing action (register/enroll/rotate/revoke/command), but there is no per-admin identity to attribute them to, since the admin API key is a single shared secret, not per-user credentials |

The "Partial" rows are the honest state, not a gap list to pretend doesn't
exist — a real production hardening pass would replace the shared admin API
key with per-admin credentials (so audit logs have a real actor) and move
EMQX↔controller auth to mTLS (already flagged in `docs/architecture.md`).

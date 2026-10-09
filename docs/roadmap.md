# Implementation Roadmap

## Repository layout
```
cloud-device-controller/
├── controller/            Go service: API, store/migrations, MQTT client, business logic, tests
├── device-agent/          Python agent: MQTT client, command allowlist, heartbeat loop, tests
├── simulator/             Spins up N simulated agents for load testing
├── provisioning/          Inventory files, device config templates, bulk enrollment scripts
├── infrastructure/terraform/   AWS infra (VPC, EKS, RDS, ECR, IAM, Secrets Manager)
├── kubernetes/helm/       Helm chart for controller + EMQX
├── kubernetes/argocd/     Argo CD Application manifests
├── gitops/{dev,staging,prod}/  Per-env values/image-digest files Argo CD watches
├── .github/workflows/     CI/CD pipelines
├── monitoring/            Prometheus rules, Grafana dashboards
├── docs/                  architecture, deployment, provisioning, cicd, operations, troubleshooting
├── docker-compose.yml
├── Makefile
└── README.md
```

## Phases, dependencies, exit criteria

| # | Phase | Depends on | Exit criterion |
|---|---|---|---|
| 1 | Requirements & architecture | — | This document + architecture.md complete (DONE) |
| 2 | Minimum working application | 1 | One simulated device heartbeats, receives a command, acks it, end-to-end over MQTT, verified locally |
| 3 | Device provisioning | 2 | New device onboards via inventory file + enrollment token, no controller code change |
| 4 | Local containerized environment | 2,3 | `make local-up && make simulate` works on a clean checkout |
| 5 | Kubernetes (local kind) | 4 | Helm install succeeds in kind; HPA/PDB/probes present and correct |
| 6 | AWS infrastructure (Terraform) | 5 | `terraform apply` reproducibly creates VPC/EKS/RDS/ECR/Secrets |
| 7 | CI/CD + GitOps | 6 | PR → test → build → ECR → Argo CD → dev/staging/prod promotion works end-to-end |
| 8 | Scale testing | 7 (or 4 for local-only scale test) | 1,000-device simulation run, metrics recorded in docs/load-test-report.md |
| 9 | Production hardening | 8 | Documented runbooks: backup/restore, credential rotation, incident response |
| 10 | Final end-to-end demo | 9 | Every step in your 20-step demo list has a command/log/screenshot |

## Status
- **Phase 1: COMPLETE** — see `docs/architecture.md`.
- **Phase 2: COMPLETE** — controller, device agent, and simulator built and verified end-to-end
  against a real local stack (`docker compose up`): register → enroll → MQTT connect → heartbeat →
  online status → command → device execution → ack → result retrieval. 25 controller tests + 10
  agent tests passing. A 50-device `load_test.py` run and an Alembic up/down/up round trip against
  real Postgres were also verified.
- **Phase 3: COMPLETE** — `provisioning/scripts/bulk_provision.py` registers devices from
  `provisioning/inventory/devices.example.yaml` and renders per-device enrollment bundles
  (`docs/provisioning.md`). Credential rotation and revocation endpoints added
  (`POST /devices/{id}/credentials/rotate`, `POST /devices/{id}/revoke`), 32 controller tests
  passing. Verified live: provisioned 4 devices from the inventory file with zero controller code
  changes, idempotent rerun correctly skipped all 4, started a real agent from a rendered bundle
  (self-enrolled + connected), verified onboarding polling correctly distinguished the 1 online
  device from 3 not-yet-started ones, and exercised rotate/revoke against the live stack.
- **Phase 4: COMPLETE** — `docker-compose.yml` already covered this (Postgres + EMQX + controller,
  `make local-up`/`make test`/`make simulate`); no further work needed.
- **Phase 5: COMPLETE** — `kubernetes/helm/device-controller/` (Helm chart: Deployment, EMQX +
  Postgres StatefulSets, HPA, PDB, NetworkPolicy, RBAC, Ingress, ExternalSecret for
  staging/prod, Alembic migration Job). Verified live on a local `kind` cluster: installed,
  registered+enrolled a device, ran the real agent through port-forwards end-to-end
  (heartbeat -> online -> command -> ack), then `helm upgrade` and confirmed the device
  record survived. **Found and fixed 3 real bugs that only Kubernetes's networking/readiness
  model exposed** (blocking MQTT connect crashing the app, same issue in the dev create_all
  fallback, and a genuine readyz/Service startup deadlock) — see `docs/deployment.md` for
  detail. Known gaps are documented there too (EMQX single-node only, a narrow first-install
  migration-ordering race).
- **Phase 6: COMPLETE (code only — not applied)** — `infrastructure/terraform/`: a `bootstrap`
  root module (S3 + DynamoDB for remote state, solving the chicken-and-egg problem honestly) plus
  the main root module (VPC and EKS via the `terraform-aws-modules` registry modules, a local
  `modules/app` for RDS/ECR/Secrets Manager/the External-Secrets IRSA role), with
  `environments/{dev,staging,prod}/terraform.tfvars`. Validated with `terraform validate` and
  `terraform plan` against a local backend for all three environments — full resource graph builds
  correctly; only stops at "no AWS credentials" in this environment, which is expected. **No
  `terraform apply` was run — no real AWS infrastructure exists.** A real version-constraint bug
  was found this way: `terraform-aws-modules/eks//21.x` actually requires AWS provider >= 6.0 in
  its submodules despite the module's own examples showing `~> 5.0` at the time of writing — fixed
  by actually running `terraform init`, not by trusting the docs. `docs/deployment.md` has the full
  bootstrap/apply/destroy sequence and a cost table.
- **Phase 7: COMPLETE (not run in real GitHub Actions)** — 7 workflows in `.github/workflows/`
  (lint, test incl. a real Compose E2E smoke test, terraform validate, helm lint + kubeconform,
  build/scan/push-to-ECR via OIDC + auto-deploy to dev, smoke-test-dev→promote-staging→E2E-test,
  manual-approval→promote-prod), 3 Argo CD `Application` manifests (`kubernetes/argocd/`), and
  `gitops/{dev,staging,prod}/image-values.yaml` as the sync targets. `docs/cicd.md` covers the
  monorepo decision, why GitOps reconciliation never conflicts with CI (data flows one way: CI →
  Git → Argo CD → cluster, never back), required GitHub repo/environment/secrets setup, and the
  two-level rollback story (`kubectl rollout undo` vs. a Git revert). Validated with `actionlint`
  (0 warnings on all 7 workflows after fixing real shellcheck nits) and `kubeconform` (all 3 Argo
  CD manifests schema-valid) — but **no workflow has run inside real GitHub Actions**, since this
  environment has no GitHub remote configured. The reusable `.github/scripts/smoke_test.sh` was
  written and actually run end-to-end against the local Compose stack before being wired into any
  workflow. Also fixed a real bug found by running `kubeconform` against the Helm chart's rendered
  output: `_helpers.tpl`'s label templates produced duplicate `app.kubernetes.io/name` YAML keys
  wherever combined with a selector-labels include — invalid per strict YAML even though `kubectl`
  tolerated it silently.
- **Phase 8: COMPLETE at revised scope (50 devices, not 1,000)** — see `docs/load-test-report.md`
  for the full story. Validated 100% onboarding/command success with zero failures at 10/50/100
  devices. Found and fixed 3 real bugs chasing the original 1,000-device target: DB connection
  pool exhaustion (`controller/app/core/database.py`, pool size now configurable), EMQX's
  auth-webhook HTTP pool being far too small (`pool_size=8` default, raised to 64 in both
  `docker/emqx/emqx.conf` and the Helm chart), and bcrypt being the wrong tool for hashing
  already-high-entropy random tokens (measured 1200%+ controller CPU under load; switched to
  SHA-256 + constant-time comparison, `app/core/security.py`). After all three fixes, 1,000-device
  runs still plateaued around 30% connection success with zero corresponding server-side errors —
  two more simulator-side bugs were found and fixed along the way (a connect-timeout zombie-client
  leak, and `_on_connect` not checking `reason_code`), and two hypotheses (client scheduling delay,
  connection rate) were tested, with the first ruled out and the second left unresolved when the
  project owner descoped the target to 50 devices for now. The leading unconfirmed hypothesis
  (Docker Desktop's local networking under a same-machine 1,000-connection burst) and the
  recommended next steps if 1,000 devices is revisited are both in the report — honestly labeled as
  unconfirmed, not presented as a diagnosis.
- **Phase 9: COMPLETE** — `monitoring/prometheus/{prometheus.yml,alerts.yml}`,
  `monitoring/grafana/` (datasource + dashboard auto-provisioning), `docker-compose.monitoring.yml`
  (`make monitoring-up`). Verified live: brought the stack up, confirmed the scrape target is
  `up`, all 5 alert rules loaded, Grafana's datasource and dashboard both auto-provisioned with
  zero manual UI steps, and queried a real metric through Grafana's own datasource-proxy API.
  Found and fixed two more real bugs doing this: the `controller_devices_online` gauge existed but
  nothing ever called `.set()` on it (replaced with a custom Collector computing it fresh from the
  DB on every scrape), and that Collector's registration eagerly calls `collect()` once at app
  startup — before the schema necessarily exists — the same startup-race family hit twice already
  in Phase 5, now guarded the same way. Added an HTTP request-latency histogram (didn't exist
  before — a real gap against "API latency" monitoring), labeled by route *template* to avoid
  unbounded cardinality, verified via real route names appearing in `/metrics`. `docs/operations.md`
  (monitoring, operational commands, backup/restore, credential rotation, a security-validation
  checklist that reports "Partial" honestly where it is partial) and `docs/troubleshooting.md`
  (built entirely from real incidents hit in this repo, not generic boilerplate) round out the
  phase.
- **Phase 10: COMPLETE** — `docs/final-demo.md` walks all 20 steps from the spec's final
  demonstration, every one actually run, not just described. Steps 1-7 (dev change -> PR -> CI ->
  merge -> build -> push to ECR -> Argo CD deploy) touch GitHub Actions, ECR, and Argo CD, none of
  which exist in this environment (no GitHub remote, no AWS credentials, no Argo CD install) — each
  is honestly substituted with its real local-mechanism equivalent (a real git branch/merge, a real
  local Docker build with a real content digest, `helm upgrade` standing in for an Argo CD sync) and
  labeled as such, never presented as the real thing. Steps 8-20 (provision -> connect -> heartbeat
  -> online -> command -> execute -> ack -> store -> retrieve -> Prometheus -> Grafana -> simulated
  failure -> recovery) run against the real controller, real device-agent, real EMQX, and real
  Prometheus/Grafana with zero substitution. The version bump used as the "developer change" for
  step 1 is real and is what step 7's deployed pod reports back via its own API
  (`GET /openapi.json` -> `"version": "0.2.0"`) — not just an image tag.

  **Found one more real limitation doing this**: `kind load docker-image` only makes an image
  resolvable by tag in containerd's local store, not by a bare digest reference — only a real
  registry (ECR) supports that. A `helm upgrade --set controller.image.digest=...` attempt
  correctly failed rather than silently deploying the wrong thing (the pre-upgrade migration Job
  hit `ImagePullBackOff`); the digest-pinning *mechanism* itself was already validated structurally
  in Phase 7 via `kubeconform`, this is purely a local-testing-without-a-registry gap. Also hit and
  documented a real `kubectl port-forward` nuance: it binds to whichever pod backed a Service when
  the forward started and does not fail over when that pod is deleted. Both are in
  `docs/troubleshooting.md`.

  The step-20 failure simulation (deleting the running controller pod) showed the architecture
  working as designed: the device's own agent log shows no disconnect event at all during the
  outage, because the device talks to EMQX, not to any specific controller pod — confirmed directly
  via the device's `online` status and `last_seen_at` never lapsing, then proved full functionality
  (not just connectivity) by round-tripping one more command through the recovered pod.

## All 10 phases complete.

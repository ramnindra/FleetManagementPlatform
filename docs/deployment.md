# Deployment (Phases 4-6)

## Local, no Kubernetes (Phase 4)

```bash
make setup && make test && make local-up && make simulate
```

`make local-up` runs `docker-compose.yml`: Postgres + EMQX + controller. The
controller uses `Base.metadata.create_all()` on startup when
`CONTROLLER_ENVIRONMENT=development` (set in `docker-compose.yml`) — this is
a dev-only convenience; staging/prod use the `controller migrate` Job described
below instead (see `controller/app/main.py`).

## Local Kubernetes via kind (Phase 5)

```bash
kind create cluster --name device-controller
docker build -t cloud-device-controller-controller:latest ./controller
kind load docker-image cloud-device-controller-controller:latest --name device-controller

cd kubernetes/helm/device-controller
helm install device-controller . -f values-dev.yaml --create-namespace --namespace device-controller

kubectl -n device-controller get pods
kubectl -n device-controller port-forward svc/device-controller 8000:8000
curl http://localhost:8000/healthz
```

To redeploy after a code change: rebuild the image, `kind load docker-image`
again, then `kubectl -n device-controller rollout restart deployment/device-controller`
(a `kind load` does not itself trigger a rollout — the Deployment spec is
unchanged, so Kubernetes has no reason to replace running pods).

**Verified in this repo**: a full install was run against a real local kind
cluster — registered a device, enrolled it, ran the real `device-agent`
through `kubectl port-forward` against the in-cluster EMQX and controller,
confirmed `online: true` and a `get_status` command round-tripped
successfully, then ran `helm upgrade` (exercising the pre-upgrade migration
hook) and confirmed the device record survived untouched.

### Three real bugs found and fixed while getting this working

Kubernetes surfaces race conditions and coupling that Docker Compose's
simpler networking model hides. All three were found by actually deploying,
not by review:

1. **Blocking MQTT connect crashed the app.** `ControllerMqttClient.start()`
   called `paho.mqtt.Client.connect()`, which does a synchronous DNS lookup +
   TCP connect and raises on failure. On a fresh deploy, the controller pod
   can easily start before EMQX's Service DNS is resolvable, and that raised
   exception propagated out of the FastAPI lifespan, crashing the whole app
   (`CrashLoopBackOff`). Fixed by switching to `connect_async()`, which defers
   the first connection attempt to the background network thread — the same
   thread that already retries a *later* dropped connection via
   `reconnect_delay_set()`. The identical bug existed in `device-agent`'s MQTT
   client for the same reason (a device booting before its network/controller
   is reachable) and was fixed the same way.

2. **The dev-only `create_all()` had the same problem**, one layer down:
   `Base.metadata.create_all(bind=engine)` opens a blocking DB connection and
   raised if Postgres was not yet accepting connections on a fresh pod. Fixed
   with a bounded retry loop (`_create_all_with_retry`, 10 attempts / 2s
   apart) — this one specifically needs to block until it succeeds (the app
   cannot serve correctly without a schema), so retrying in place rather than
   backgrounding it is correct here, unlike the MQTT case.

3. **A genuine startup deadlock**: `/readyz` originally also checked
   `mqtt_client.is_connected()`. In Kubernetes, a Service only routes traffic
   to pods that have already passed their readiness probe. EMQX's
   `/internal/mqtt/auth` webhook calls back into the controller *through that
   same Service* — so the controller pod could never become Ready (it needed
   MQTT connected) because MQTT could never authenticate (EMQX's webhook call
   to the not-yet-Ready pod never got routed anywhere). This doesn't happen
   in Docker Compose, which doesn't gate container-to-container DNS routing on
   healthcheck status the way a Kubernetes Service does. Fixed by removing the
   MQTT check from `/readyz` entirely — correctly: the REST API and the
   `/internal/mqtt/*` webhooks only need the database, and do not depend on
   this specific pod's own MQTT client being connected at this specific
   moment.

### Known gap: migration-Job vs. first-install pod race

The migration Job is hooked as `post-install,pre-upgrade`, not
`pre-install,pre-upgrade` — deliberately, because a `pre-install` hook fires
before *any* other release resource exists, including the Secret the Job
needs for `CONTROLLER_DATABASE_URL`. The trade-off: on a brand-new install
(never on a later upgrade), the controller Deployment's pods are created at
the same time as the post-install migration Job, not strictly after it. In
this repo's dev path that's masked by the `create_all()` fallback
(`CONTROLLER_ENVIRONMENT=development`); in staging/prod — where that fallback
is intentionally absent — a pod could briefly serve a request against a
not-yet-migrated schema on first install only. Noted here rather than hidden;
the correct hardening fix is a readiness check that compares the DB's current
migration against head, tracked in `docs/roadmap.md` Phase 9.

### Also known and documented elsewhere

- EMQX stays at `replicaCount: 1` in every values file — this chart does not
  implement DNS-based cluster discovery, so more replicas would silently be
  independent brokers with split device sessions, not a cluster (see
  `values-staging.yaml`).
- The EMQX↔controller webhook secret is substituted into `emqx.conf` by an
  `initContainer` reading the same Kubernetes Secret the controller uses
  (whether that Secret came from plain Helm values or an `ExternalSecret`) —
  baking the value directly into the ConfigMap via Helm templating would have
  silently used the wrong (dev-default) value in staging/prod, since Helm
  cannot see what an `ExternalSecret` resolves to at render time.

## Values files

- `values.yaml` — base defaults.
- `values-dev.yaml` — local kind/minikube: 1 replica, no autoscaling, images
  loaded locally via `kind load docker-image`.
- `values-staging.yaml` / `values-prod.yaml` — EKS: ECR image digests (set by
  CI, Phase 7), autoscaling on, Ingress via ALB, `postgres.enabled: false`
  (external RDS, Phase 6), secrets from AWS Secrets Manager via
  `ExternalSecret` (requires the External Secrets Operator already installed
  in-cluster — not installed by this chart).

## AWS infrastructure via Terraform (Phase 6)

**This section requires a real AWS account and will incur real cost.** Every
command below is given for completeness and was validated with
`terraform validate` / `terraform plan` against a local backend — `plan`
correctly got through building the entire resource graph (all variables,
`random_password` resources, module wiring) and only stopped at "no AWS
credentials," which is expected in this environment. **`terraform apply` was
NOT run** — no AWS infrastructure was actually created, and nothing here
claims otherwise.

### One-time per account/region: bootstrap remote state

Terraform's S3 backend needs a bucket and DynamoDB lock table to exist before
`terraform init` can even point at them — `infrastructure/terraform/bootstrap`
is a separate, tiny root module (its own local state, deliberately never
migrated to S3) that creates those, plus the one ECR repository every
environment shares. ECR lives here rather than in the per-environment root
module deliberately: dev/staging/prod are three separate `terraform apply`
runs, and CI builds exactly one image per merge to main and promotes that
same immutable digest through all three (see Phase 7) — there must be
exactly one repository, not one per environment.

```bash
cd infrastructure/terraform/bootstrap
terraform init
terraform apply                      # review the plan; creates 1 S3 bucket + 1 DynamoDB table + 1 ECR repo
terraform output backend_hcl_snippet # paste this into environments/<env>/backend.hcl
```

Create `environments/dev/backend.hcl` (and staging/prod) from that output,
e.g.:

```hcl
bucket         = "device-controller-tfstate-<account-id>"
dynamodb_table = "device-controller-tfstate-lock"
region         = "us-east-1"
encrypt        = true
key            = "device-controller/dev/terraform.tfstate"
```

### Per environment: initialize, plan, apply

```bash
cd infrastructure/terraform
terraform init -backend-config=environments/dev/backend.hcl
terraform plan  -var-file=environments/dev/terraform.tfvars
terraform apply -var-file=environments/dev/terraform.tfvars
```

### Inspect outputs

```bash
terraform output                       # cluster name/endpoint, ECR repo URL, RDS endpoint, secrets prefix
terraform output external_secrets_irsa_role_arn
```

### Configure kubectl

```bash
$(terraform output -raw configure_kubectl)   # runs: aws eks update-kubeconfig --region ... --name ...
kubectl get nodes
```

### Platform prerequisites not created by this Terraform

Two cluster-wide add-ons are assumed to already be installed (via their own
Helm charts, or `eks-blueprints-addons`) before `kubernetes/helm/device-controller`
is deployed with `values-staging.yaml`/`values-prod.yaml`:

- **External Secrets Operator** — its ServiceAccount (namespace/name set by
  `var.external_secrets_namespace` / `var.external_secrets_service_account`,
  default `external-secrets`/`external-secrets`) must be annotated with
  `eks.amazonaws.com/role-arn = <terraform output external_secrets_irsa_role_arn>`,
  and a `ClusterSecretStore` named to match
  `controller.externalSecrets.secretStoreRef` in the Helm values must exist,
  pointing at AWS Secrets Manager in this account/region.
- **AWS Load Balancer Controller** — needed for the `alb` Ingress class used
  by `controller.ingress.className: "alb"`. Deliberately not provisioned by
  this Terraform (it needs its own IRSA role + a large AWS-maintained IAM
  policy document that is a platform concern, not an app concern) — see the
  official install docs for the IAM policy JSON and Helm chart.

### Destroying dev infrastructure

```bash
cd infrastructure/terraform
terraform destroy -var-file=environments/dev/terraform.tfvars
```

`environments/dev/terraform.tfvars` sets `db_multi_az = false`,
`db_backup_retention_days = 1`, and the RDS instance has
`skip_final_snapshot = true` only when `environment == "dev"` — so a dev
`destroy` is fast and doesn't leave a final RDS snapshot behind. **Never run
this against staging/prod var-files** — those have `deletion_protection = true`
on the database and will refuse to destroy it without first disabling that
protection by hand, which is deliberate friction.

### What incurs recurring cost

| Resource | Cost driver | Dev default |
|---|---|---|
| EKS cluster | ~$0.10/hr control plane, always-on | 1 cluster |
| EKS worker nodes | EC2 instance-hours | 1x `t3.medium` |
| NAT Gateway | hourly + per-GB data processed | 1 shared (dev); 1-per-AZ in staging/prod |
| RDS | instance-hours + storage | 1x `db.t3.micro`, 20GB, single-AZ |
| EMQX/controller pods | covered by the EKS node cost above | — |
| S3 (tfstate) / DynamoDB (lock) / ECR / Secrets Manager | negligible at this scale | — |

The NAT Gateway and EKS control plane are the two costs that run up fastest
if a dev cluster is left up — both billed hourly regardless of load.

## Operational commands

```bash
kubectl -n device-controller get pods
kubectl -n device-controller logs deployment/device-controller
kubectl -n device-controller rollout status deployment/device-controller
kubectl -n device-controller rollout undo deployment/device-controller
helm -n device-controller history device-controller
helm -n device-controller rollback device-controller <revision>
```

`kubectl rollout undo` reverts the Deployment's pod template to the previous
ReplicaSet immediately, but does not touch Helm's own release history or
`values.yaml` — the next `helm upgrade` from Git would silently re-apply the
"bad" version. `helm rollback` reverts the whole release (and is what GitOps
promotion/rollback in Phase 7 actually drives) — see `docs/operations.md`
(Phase 9) for the full distinction.

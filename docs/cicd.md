# CI/CD and GitOps (Phase 7)

**What was actually run and verified in this repo:** `.github/scripts/smoke_test.sh`
was written and run for real against the local Docker Compose stack (passes
end-to-end; also verified it fails loudly on a bad credential). `actionlint`
and `kubeconform` were run against every workflow and Argo CD manifest and
pass clean. **No workflow has executed inside real GitHub Actions** — this
repo has no GitHub remote configured in this environment, so nothing here
claims to have been triggered by a real push, build, or deploy.

## Why 7 workflow files instead of 10 separate chained ones

The spec's conceptual pipeline lists 10 steps. Splitting each into its own
workflow *file* that triggers the next via cross-workflow events
(`workflow_run`) is a common pattern, but it is also fragile — passing an
image digest between separately-triggered workflow runs needs artifact
upload/download or repository-dispatch, and a failed trigger fails silently
with no `needs:` dependency to show it. For one engineer operating this
pipeline, **jobs within fewer files, sequenced with `needs:`,** are both more
reliable and easier to read top-to-bottom:

| Spec step | Covered by |
|---|---|
| 1. Lint / static analysis | `pr-validate.yml` |
| 2. Unit + integration tests | `test.yml` (`controller-tests`, `agent-tests` jobs) |
| 3. Build Docker image | `build-scan-push.yml` |
| 4. Container vulnerability scan | `build-scan-push.yml` (Trivy, fails on CRITICAL) |
| 5. Push to ECR | `build-scan-push.yml` (OIDC, immutable `:sha` tag) |
| 6. Deploy to development | `build-scan-push.yml` (commits `gitops/dev/image-values.yaml`) |
| 7. Staging promotion + verification | `promote-staging.yml` (smoke-test dev → promote → E2E-test staging) |
| 8. Manual production approval + promotion | `promote-prod.yml` (GitHub Environment gate) |
| 9. Infrastructure validation | `terraform-validate.yml` |
| 10. Helm lint + manifest validation | `helm-lint.yml` |

`test.yml` also runs a `compose-e2e` job that brings up the real
`docker-compose.yml` stack and runs `.github/scripts/smoke_test.sh` against
it at PR time — catching a broken device flow before merge, not just broken
unit tests.

## One repository, not two

Application code, the Helm chart, and the GitOps `gitops/<env>/` values all
live in this one repo. The alternative — a separate "config" repo Argo CD
watches, with CI in the app repo pushing to it — mainly buys you the ability
to grant different people write access to "what's deployed" vs. "the code,"
which matters for larger teams with a dedicated ops group. For this project's
scale (one engineer, one small team), that split adds a second repo to keep
in sync for no real access-control benefit, so the trade-off favors a
monorepo. If a team using this as a starting point grows to the point where
"who can change what's running in prod" needs to be a different set of
people from "who can merge code," that is the point to split `gitops/` out.

## Why GitOps reconciliation never conflicts here

Argo CD's `syncPolicy.automated` makes it the only thing that ever writes to
the cluster from the `kubernetes/helm/device-controller` chart + `gitops/`
values — it never writes anything back to Git. CI (`build-scan-push.yml`,
`promote-staging.yml`, `promote-prod.yml`) is the only thing that ever writes
to Git. Data flows one way: **CI → Git → Argo CD → cluster**, never cluster →
Argo CD → Git. There is nothing for the two to conflict over. The one real
race is two CI runs both trying to `git push` to the same `gitops/<env>/`
path at once — handled by the `concurrency:` group on each deploy workflow
(queued, not cancelled, so no build is silently dropped), not by anything
GitOps-specific.

`syncPolicy.automated.selfHeal: true` means if someone manually
`kubectl edit`s a resource Argo CD manages, Argo CD reverts it on the next
reconcile loop — intentional: the Git state is the only source of truth, by
design, for everything these three Application manifests manage.

## Immutable images, never `:latest` in production

`build-scan-push.yml` tags images by commit SHA, never `:latest`, and ECR's
`image_tag_mutability = IMMUTABLE` (set in `infrastructure/terraform/bootstrap`)
makes re-pushing the same tag a hard registry-level error, not just a CI
convention. The Helm chart's `controller.image.digest` value (see
`values.yaml` and the `device-controller.image` helper template) takes an
AWS-returned `sha256:...` digest and renders `repository@digest` instead of
`repository:tag` — that digest, not a tag, is what `gitops/<env>/image-values.yaml`
carries through dev → staging → prod. The same bytes that passed the dev
smoke test and the staging E2E test are what reaches production; nothing is
ever rebuilt between environments.

## Required GitHub repository configuration

None of this is expressible in the workflow YAML itself — it is one-time
repo setup:

- **Environments** (Settings → Environments): create `dev`, `staging`,
  `production`. Only `production` needs "Required reviewers" configured —
  that single setting is the entire manual-approval gate `promote-prod.yml`
  depends on.
- **Repository/Environment variables** (`vars.*`, not secret):
  `DEV_API_ENDPOINT`, `DEV_MQTT_HOST`, `STAGING_API_ENDPOINT`,
  `STAGING_MQTT_HOST`, `PROD_API_ENDPOINT`, `AWS_GITHUB_ACTIONS_ROLE_ARN`
  (from `terraform output github_actions_role_arn` in
  `infrastructure/terraform/bootstrap`).
- **Secrets** (`secrets.*`): `DEV_ADMIN_API_KEY`, `STAGING_ADMIN_API_KEY` —
  the admin API key for each environment's controller (from Secrets Manager;
  never the same value as the dev-only default baked into
  `docker-compose.yml`). No AWS access key/secret pair is ever stored here —
  OIDC federation (`aws-actions/configure-aws-credentials` +
  `AWS_GITHUB_ACTIONS_ROLE_ARN`) replaces it entirely.
- **Branch protection** (Settings → Branches → `main`): require
  `pr-validate.yml` and `test.yml` to pass before merge; require at least one
  review; do not allow force-pushes to `main`.

## Promotion and rollback, in practice

**Promotion** is "the same digest moves forward," enforced by code (every
promotion step reads the previous environment's `gitops/<env>/image-values.yaml`
rather than accepting a digest as free-form input) — there is no code path
that lets staging or prod deploy an image dev never ran.

**Rollback** has two levels, and they are not the same operation:

- `kubectl rollout undo deployment/device-controller -n device-controller` —
  immediate, but only reverts the running pods to the previous ReplicaSet. It
  does **not** touch `gitops/<env>/image-values.yaml` — the next time
  anything touches that Argo CD Application (even an unrelated sync), the
  "bad" digest in Git wins again, silently undoing your `rollout undo`.
- `git revert` the commit that updated `gitops/<env>/image-values.yaml`
  (or `helm rollback` applied by hand before Argo CD overwrites it,
  equivalently) — the durable fix, because it changes what Git says should be
  running, which is the only thing Argo CD ever looks at.

For a real incident: `kubectl rollout undo` buys immediate relief; the Git
revert is what actually closes the incident.

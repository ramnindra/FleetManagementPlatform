# Device Provisioning (Phase 3)

## Bootstrap trust model

A device is never trusted just because it claims a `device_id`. The chain is:

1. An **operator** (via `bulk-provision` or a direct admin API call) registers
   a device, authenticated with `CONTROLLER_ADMIN_API_KEY`. This creates a
   `pending_enrollment` device record and a one-time, short-lived
   (`CONTROLLER_ENROLLMENT_TOKEN_TTL_SECONDS`, default 1h) enrollment token —
   returned once, stored server-side only as a bcrypt hash.
2. That token is delivered to the physical device out-of-band (see "Delivery
   method" below) — the controller never pushes it over the network to a
   device it hasn't verified yet.
3. The device's first network contact is `POST /api/v1/devices/{id}/enroll`
   with that token. Only on success does the controller issue a long-lived
   device credential (also stored only as a bcrypt hash) and flip the device
   to `active`.
4. From then on, the device authenticates to MQTT with that credential via
   the `/internal/mqtt/auth` webhook (see `docs/architecture.md`).

A device that never received a valid token cannot self-register — there is no
"create me" MQTT topic or API call that bypasses step 1. This is what
"secure first-time registration" means in practice here.

## Devices → inventory → cloud infra → Kubernetes: three different things

It's worth being explicit about what provisions what, since the words overlap:

| Layer | What it creates | Tool |
|---|---|---|
| **Physical device provisioning** | A device record in the controller + an enrollment bundle delivered to real hardware | `provisioning/cmd/bulk-provision` (this doc) |
| **Cloud infrastructure provisioning** | VPC, EKS, RDS, ECR, IAM, Secrets Manager | Terraform (`infrastructure/terraform/`, Phase 6) |
| **Kubernetes resource provisioning** | Deployments, Services, HPAs, etc. for the controller app itself | Helm (`kubernetes/helm/`, Phase 5) |

Kubernetes cannot provision a physical switch or GPU server, and Terraform
doesn't know a device exists — `bulk-provision` only ever talks to the
controller's REST API, nothing cloud- or cluster-specific.

## Delivery method: enrollment bundles

Four options were weighed — Ansible, cloud-init, image-based provisioning, and
enrollment bundles (a directory per device containing a ready-to-copy
`config.yaml` + instructions):

| Method | Works for switches/bare-metal GPU nodes? | Works without a config-mgmt control plane already in place? | Implemented here |
|---|---|---|---|
| Enrollment bundle (chosen) | Yes — just needs `scp`/USB/any file transfer | Yes | **Yes** |
| cloud-init | No — it's cloud-VM/instance-boot specific, most switches and bare-metal GPU boxes don't run it | N/A | No |
| Ansible | Yes, but requires SSH + an inventory + Ansible already set up on every target | No — circular: you'd need the devices reachable and trusted first | No |
| Image-based (baked into firmware/OS image) | Yes, but means re-imaging for every device, heavyweight for this project's scale | Yes | No |

Enrollment bundles are the only option that works uniformly across all three
device types this project targets (switch, GPU node, generic edge) without
assuming a pre-existing fleet-management system — which is exactly the
chicken-and-egg problem this project is solving. Ansible/cloud-init remain
reasonable *follow-on* delivery mechanisms once a base fleet exists (e.g., an
Ansible playbook that just does the `scp` + `systemctl enable --now` steps
below) — not implemented here, but compatible with it.

## Running it

```bash
cd provisioning
go run ./cmd/bulk-provision \
  --inventory inventory/devices.example.yaml \
  --api-endpoint http://localhost:8000 \
  --admin-api-key dev-admin-key \
  --mqtt-host localhost \
  --output-dir output \
  --verify
```

This registers every device in the inventory that isn't already registered
(idempotent — safe to rerun after adding new entries), renders
`provisioning/output/<device_id>/{config.yaml,README.txt}`, and with
`--verify`, polls `/status` to report how many actually came online.

**Verified in this repo**: a real run against the local Docker Compose stack
provisioned 4 devices from `devices.example.yaml`, a real `device-agent`
process was started from one rendered bundle (`gpu-node-001`) and
self-enrolled + connected, and `--verify`-equivalent polling correctly
reported 1/4 online (the one actually started) and listed the other 3 as
offline — not fabricated numbers.

## Delivering the bundle to a device

```bash
scp -r provisioning/output/gpu-node-001/ user@device-host:/tmp/bundle
ssh user@device-host 'sudo mkdir -p /etc/device-agent && \
  sudo cp /tmp/bundle/config.yaml /etc/device-agent/config.yaml && \
  sudo chmod 600 /etc/device-agent/config.yaml'
# then install device-agent/ + device-agent/systemd/device-agent.service and:
ssh user@device-host 'sudo systemctl enable --now device-agent'
```

## Credential rotation and revocation

- **Rotate** (e.g., suspected credential leak, routine hygiene):
  `POST /api/v1/devices/{id}/credentials/rotate` (admin). This immediately
  invalidates the current credential (the device is rejected at the MQTT auth
  webhook) and re-opens enrollment with a fresh one-time token — deliver that
  token to the device the same way as initial enrollment, and the running
  agent's existing enroll logic in `device-agent/cmd/device-agent/main.go` picks it up
  on its next restart.

  *Known simplification*: there's a brief window where the device is offline
  between rotation and re-enrollment, because the old and new credentials
  never overlap. A production hardening step would have the agent proactively
  request rotation *before* expiry and swap credentials without disconnecting
  — not implemented here to keep the state machine in `device_service.py`
  simple (one `credential_hash` column, not two).

- **Revoke** (decommissioned/compromised hardware):
  `POST /api/v1/devices/{id}/revoke` (admin). Permanent in this reference
  implementation — the `device_id` cannot be re-registered or re-enrolled
  afterward, matching common real-world practice of never recycling an
  identity for decommissioned hardware.

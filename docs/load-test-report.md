# Load Test Report (Phase 8)

## Scope decision

The original target was 1,000 concurrent devices. Partway through this
phase, after finding and fixing three real bottlenecks (below), the
1,000-device runs still plateaued at ~30-33% connection success on this
test environment — a single developer laptop running the whole stack
(Postgres + EMQX + controller + a 1,000-thread simulator, all on one
machine) through Docker Desktop's virtualized networking. The project
owner made the call: **validate cleanly at 50 devices now, revisit 1,000 as
a later scale-up** rather than keep burning time chasing what increasingly
looked like a test-environment artifact rather than an application bug (see
"What was ruled out" below). That is the right call for where this project
is — the architecture target (1,000 devices, `docs/architecture.md`) is
unchanged; this just means it has not been *validated* at that scale yet.

## Final validated numbers (10 / 50 / 100 devices)

All three runs below are from the same code, back-to-back, against the
local Docker Compose stack (`docker-compose.yml`), `--admin-api-key
dev-admin-key`, default `heartbeat-interval` scaled down for faster test
cycles.

| Devices | Onboarding success | Onboarding time | Command success | p50 ack latency | p95 ack latency | Forced-reconnect time |
|---|---|---|---|---|---|---|
| 10  | 10/10 (100%)   | 0.33s | 5/5 (100%)   | 47ms | 52ms | 2.9s |
| 50  | 50/50 (100%)   | 2.58s | 20/20 (100%) | 52ms | 66ms | 4.6s |
| 100 | 100/100 (100%) | 3.26s | 30/30 (100%) | 53ms | 60ms | 4.1s |

Zero provisioning failures, zero unexpected reconnects, zero connect
failures at any of these three scales. Raw JSON/Markdown reports for each
run are written by `simulator/load_test.py --report-dir` (gitignored —
regenerate with the command below rather than trusting stale numbers).

```bash
cd simulator
python load_test.py --count 50 --provision-workers 20 --steady-state-seconds 40 \
  --sample-commands 20 --reconnect-sample 10 --heartbeat-interval 15 \
  --api-endpoint http://localhost:8000 --mqtt-host localhost --admin-api-key dev-admin-key \
  --report-dir ./results
```

## Initial SLOs (proposed, then checked against the data above)

| SLO | Target | Observed (≤100 devices) |
|---|---|---|
| Onboarding success rate | ≥99% | 100% |
| Command ack p95 latency | <500ms | ≤66ms |
| Unplanned reconnects during steady state | ~0 | 0 |

These are explicitly scoped to ≤100 devices, matching what was actually
validated — not extrapolated to 1,000.

## Three real bugs found and fixed chasing 1,000 devices

Each of these is a genuine fix that is now part of the system, independent
of the scope decision above — they would matter at 100 devices too under a
big enough provisioning/connection burst, just less visibly.

1. **DB connection pool exhaustion.** `controller/app/core/database.py` had
   `pool_size=10, max_overflow=20` (30 total) hardcoded. A 1,000-device
   provisioning burst (100 concurrent register+enroll calls, each holding a
   connection for its duration) exhausted it outright:
   `sqlalchemy.exc.TimeoutError: QueuePool limit of size 10 overflow 20
   reached`. Fixed: pool size is now `CONTROLLER_DB_POOL_SIZE` /
   `CONTROLLER_DB_MAX_OVERFLOW` (defaults 20/60, documented trade-off against
   Postgres's `max_connections` for multi-replica deployments in
   `app/core/config.py`). Also raised anyio's default thread-pool limiter
   (default 40) to match — it would have been the very next bottleneck,
   since every route handler here is a synchronous `def`.

2. **EMQX auth-webhook pool too small.** `docker/emqx/emqx.conf` (and the
   Helm chart's equivalent ConfigMap) had `pool_size = 8` for both the
   authentication and authorization HTTP sources — the EMQX default.
   Confirmed via EMQX's own logs: `http_connector_do_request_failed, reason:
   timeout, connector: "emqx_authn_http:2"`, repeated hundreds of times
   under a 1,000-device connection burst. Raised to `pool_size = 64` in both
   places; `request_timeout` raised from 5s to 10s alongside it.

3. **bcrypt was the wrong tool for this threat model.**
   `app/core/security.py` used `bcrypt` to hash enrollment tokens and device
   credentials — both are always `secrets.token_urlsafe(32)` output (256
   bits of entropy), never human-chosen passwords. bcrypt's deliberate
   slowness exists to make brute-forcing a *low-entropy* secret expensive;
   applied to an already-unguessable random token it adds no real security
   margin while costing real CPU per check. Under the 1,000-device
   connection burst the controller container measured **1200%+ CPU** (`docker
   stats`), and `bcrypt.checkpw` was the dominant cost in the auth path hit
   by every single device connection. Switched to SHA-256 +
   `hmac.compare_digest` (constant-time comparison, same threat model,
   correct for high-entropy inputs) — the controller test suite itself
   dropped from ~16s to under 1s as an independent confirmation of how much
   CPU bcrypt was burning. See the docstring on `hash_secret()` for the full
   reasoning. `bcrypt` was removed from `controller/requirements.txt`.

## What was ruled out (and why 1,000 devices is parked, not abandoned)

After fixing all three bugs above, 1,000-device runs still only reached
~30-33% connection success, with **zero** corresponding errors in EMQX or
controller logs — no auth timeouts, no overload warnings, controller CPU
back to idle. Two hypotheses were tested directly and ruled out:

- **Not a client-side scheduling delay.** Tripling the per-device
  `on_connect` wait from 10s to 30s (`simulator/load_test.py
  --connect-wait-seconds`) changed the success rate by less than 1
  percentage point. If devices were just slow to get serviced, more time
  should have helped substantially; it didn't.
- **Connection *rate* was being tested** (lower concurrency, 20 workers
  instead of 100, to spread the connection burst over more wall-clock time)
  when the project scope was changed to 50 devices and this run was
  stopped before reaching a conclusion — left as the open thread for
  whoever picks 1,000 devices back up.

What this does point to: devices that fail simply never receive **any**
CONNACK — not a rejection (a bug was fixed in the simulator's own
`_on_connect` that would have wrongly counted a rejected connection as
successful; it was not what was happening here, confirmed by zero matching
log lines on the server side) — just silence. With both applications idle
and error-free, the most likely remaining explanation is this specific test
environment: one laptop running Postgres + EMQX + controller + a
1,000-OS-thread Python simulator, all behind Docker Desktop's virtualized
network, for a connection burst Docker Desktop's NAT/port-forwarding layer
was never asked to handle at this volume before. This is **not confirmed**
— it is the leading hypothesis, honestly labeled as unconfirmed, not
presented as a diagnosis.

**Before resuming 1,000-device validation**, the recommended next steps,
in order: (1) finish the connection-rate test that was in progress when
scope changed, (2) run the simulator from a separate machine (or several)
against the local stack rather than co-located with it, (3) if both of
those still plateau, validate against the real EKS deployment
(`infrastructure/terraform`, Phase 6) instead of a laptop — a 1,000-device
fleet hitting a single developer machine's Docker networking was never the
realistic target topology anyway.

## Two more bugs found in the simulator itself (not the system under test)

- A connect-timeout path left the underlying MQTT client running in the
  background indefinitely retrying instead of being cleaned up — at scale,
  hundreds of these zombie clients kept consuming EMQX connection slots and
  confusing later metrics. Fixed: `SimulatedDevice.connect()` now explicitly
  stops and disconnects the client on timeout.
- `_on_connect` set the "connected" event on *any* CONNACK, including a
  rejected one — would have silently counted an auth failure as a success.
  Fixed to check `reason_code`.

Neither of these affects the controller/agent/EMQX system under test; both
are listed here because they affected the *trustworthiness* of earlier
numbers in this same investigation, and because the fixes are now permanent
parts of `simulator/common.py`.

---
title: "ADR 0011: Phase 7 — Reliability, Stability & Chaos"
description: Circuit breakers, bulkheads, graceful shutdown, backup/restore, and chaos engineering for production hardening.
---

# ADR 0011: Phase 7 — Reliability, Stability & Chaos

**Status:** Accepted  
**Date:** 2026-06-02  
**Author:** UBAG Platform Team

---

## Status

Accepted (2026-06-02).

---

## Context

UBAG Phases 0–6 delivered a functionally complete platform: a Go gateway, a Python worker, NATS-based job bus, a Rust sidecar, WASM plugin host, and a full OTel/Prometheus observability stack. However, none of the following production-hardening layers existed:

- **Circuit breakers** — downstream faults (adapter hosts, webhook targets, Postgres) propagate unbounded to callers.
- **Bulkhead admission quotas** — a single misbehaving tenant or target can exhaust the entire tab pool.
- **Graceful worker drain** — SIGTERM during active job processing drops in-flight work without requeue.
- **Backup/restore pipeline** — no tested path to recover from data loss or corruption.
- **Chaos validation** — no automated evidence that the system satisfies its steady-state hypothesis after fault injection.

Phase 7 addresses all five gaps as a production-hardening release, introducing no new functional capabilities but making existing capabilities resilient enough for customer-facing deployments.

---

## Decision 1 — Circuit Breaker Model

**Decision:** Three-state (closed → open → half-open) circuit breakers are keyed by `(kind, target)` pairs and managed by a `resilience.Registry`. Cooldown durations are computed via `retrypolicy.Policy.NextDelay` to reuse the existing jitter logic and avoid a second timer abstraction.

Breakers are deployed at two integration points:

1. **`executor.Dispatcher` middleware** — when a breaker is open, the dispatcher returns HTTP `503` with a `UBAG-QUEUE-BREAKER-OPEN-001` error code and a `Retry-After` header set to the cooldown deadline. Callers can surface this to end-users without interpreting opaque 5xx responses.

2. **`webhooks.HTTPSender` / `DeliveryWorker`** — a host-level breaker guards outbound webhook delivery. On breaker-open, `MarkRetry` is called with the cooldown delay so the job is re-enqueued at the correct time rather than immediately retried.

**Rationale:** Keying by `(kind, target)` isolates faults: a failing adapter host does not trip the breaker for healthy hosts. Reusing `retrypolicy.Policy.NextDelay` for jitter ensures cooldown intervals obey the same backoff curve as retry delays, preventing thundering-herd recovery.

**Consequences:**
- Breakers introduce per-target state in the gateway process. This state is **in-memory and non-persistent** across restarts; a fresh gateway instance starts with all breakers closed.
- The `Retry-After` header value is advisory; clients that ignore it will receive immediate 503s until the breaker half-opens.

---

## Decision 2 — Bulkhead Admission

**Decision:** A Python `BulkheadRegistry` enforces per-tenant and per-target tab ceilings. Admission is checked inside `LiveOrchestrator.lease()` before any tab is allocated from the pool. Rejection returns a backpressure signal without mutating fleet state.

The ordering invariant is: `compute_recovery()` is called **before** `pool.complete()`. This ensures the recovery plan is computed from the pre-completion state, matching the semantic contract of the existing orchestrator.

**Rationale:** Enforcing admission at `lease()` time (rather than at job submission) means the limit is enforced at the point of resource consumption, not the point of request arrival. This correctly handles bursty arrival patterns where many requests are accepted but only a bounded number can be active simultaneously.

**Consequences:**
- Each `lease()` call acquires a lock on the registry entry. Measured overhead is negligible for O(hundreds) of concurrent tabs; at O(tens of thousands) a sharded structure would be needed.
- Bulkhead limits are configured per deployment; the defaults are intentionally permissive to avoid breaking existing workflows on upgrade.

---

## Decision 3 — Graceful Worker Shutdown

**Decision:** A `GracefulDrainer` implements a four-phase drain protocol:

1. **Stop accepting** — the worker stops pulling new jobs from the NATS consumer.
2. **Drain in-flight** — waits for all active goroutines/threads to complete or reach a safe yield point.
3. **Requeue remaining** — any jobs that could not complete are pushed back to the outbox via a registered requeue callback.
4. **Report** — logs a structured drain summary (jobs completed, requeued, timed-out).

A SIGTERM/SIGINT handler spawns a daemon drain thread that invokes the drainer, allowing the main process to exit cleanly once draining completes.

Requeue callbacks decouple the drainer from the outbox implementation, mirroring the gateway's `signal.NotifyContext` + `http.Server.Shutdown(10s)` pattern.

**Rationale:** Daemon thread (not a blocking call in the signal handler) avoids signal-handler re-entrancy. Callback-based requeue means the drainer can be unit-tested without a live NATS connection.

**Consequences:**
- Jobs that exceed the drain timeout are requeued. If the outbox is unavailable at drain time, those jobs are lost. An operator runbook should document the expected behavior.
- The four-phase log output is machine-parseable (structured JSON) to support alerting on abnormal drain patterns.

---

## Decision 4 — Backup/Restore

**Decision:** The backup/restore pipeline is CLI-first (invoked via `ubag backup` / `ubag restore`) and designed for deterministic testability without a live infrastructure stack.

**SQLite backup procedure:**
1. Issue `PRAGMA wal_checkpoint(TRUNCATE)` and assert `busy == 0` (no readers blocking checkpoint).
2. Copy the database file.
3. Compute SHA-256 of the copy and write a `manifest.json`.

**Postgres backup procedure:**
1. Run `pg_dump --format=custom`.
2. Pass the database password via the `PGPASSWORD` environment variable (not on the command line, to avoid leaking credentials in process listings).

**Restore procedure:**
1. Pre-restore integrity check via `manifest.Verify()` — SHA-256 of the backup file must match the manifest.
2. Restore the backup.
3. Post-restore SHA-256 comparison to confirm byte-for-byte fidelity.
4. For SQLite: run `PRAGMA integrity_check` over all rows.
5. Run `ubag migrate` (idempotent, transactional DDL) to apply any pending migrations.

**MinIO integration:** Backup archives are uploaded to MinIO using `minio-go/v7` with AWS Signature V4. The download path uses the same client for pre-restore retrieval.

**Rationale:** CLI-first makes the backup/restore path independently testable without Docker Compose. The manifest + post-restore hash check provides end-to-end integrity assurance. `PGPASSWORD` over command-line flag is a standard security practice (`pg_dump` documents this).

**Consequences:**
- `pg_dump` / `pg_restore` must be available in the runtime environment. The production Docker image includes `postgresql-client`.
- SQLite `PRAGMA wal_checkpoint(TRUNCATE)` with `busy == 0` assertion will fail if readers hold open transactions during backup. The operator should schedule backups during low-traffic windows or coordinate with the application.

---

## Decision 5 — WAL Archiving and Point-in-Time Recovery (PITR)

**Decision:** An opt-in `backup` Docker Compose profile provisions two services:

- **`postgres-wal-archive`** — runs `pg_basebackup --wal-method=stream` every 5 minutes, uploading WAL segments to MinIO.
- **`backup-cron`** — runs `pg_dump` hourly for logical backups.

This yields:
- **RPO:** 5 minutes (WAL segment interval).
- **RTO:** 30 minutes (restore base backup + replay WAL + run migrations).

**Rationale:** WAL archiving is opt-in (profile-gated) to avoid burdening development and staging environments. The 5-minute interval is a balanced trade-off between MinIO write amplification and recovery granularity.

**Consequences:**
- PITR requires MinIO to be available at recovery time. If MinIO is unavailable, recovery falls back to the most recent `pg_dump` logical backup.
- The `backup` profile does not affect the `default` or `observability` profiles; existing `docker compose up` invocations are unaffected.

---

## Decision 6 — Chaos Engineering Harness

**Decision:** The chaos harness evaluates a steady-state hypothesis before and after each experiment:

- **Hypothesis:** rolling job-success-rate ≥ configured threshold **AND** `/v1/ready` returns HTTP 200.

**Four experiments:**
1. **Kill worker process** — sends SIGKILL to the worker container; verifies job-success-rate recovers within SLO window.
2. **5% NATS message drop** — injects a toxiproxy `noop` toxic at 5% on the NATS port; verifies the worker redelivery path.
3. **+500ms Postgres latency** — injects a toxiproxy `latency` toxic; verifies query timeout handling and breaker behavior.
4. **Malformed adapter output** — sends a job with a known-bad adapter payload; verifies the worker error path without crashing.

**CI integration:** Per-PR CI runs **schema validation only** (no live faults, no running stack required). The weekly soak profile (`chaos-soak` workflow job) runs the full suite against a live stack.

**Rationale:** Schema-only CI allows the experiment definitions to be validated on every PR with zero infrastructure cost. The live-fault soak is bounded to weekly to avoid burning CI minutes on a flaky integration harness.

**Consequences:**
- The chaos harness is integration-mode only for live experiments; it requires a running stack with toxiproxy. Local developers cannot run full chaos experiments without `docker compose --profile chaos up`.
- The schema validation in CI catches malformed experiment JSON before it reaches the soak harness, preventing false-negative soak results caused by invalid experiment definitions.

---

## Consequences

Summary of cross-cutting consequences for Phase 7:

1. **Circuit breakers** add per-target in-memory state to the gateway. Non-persistent across restarts; operators should not rely on breaker state surviving a gateway restart.
2. **Bulkhead admission** adds a lock per `lease()` call. Negligible overhead for O(hundreds) concurrent tabs.
3. **Backup/restore** depends on `pg_dump`/`pg_restore` being present in the runtime environment. Docker images must include `postgresql-client`.
4. **WAL archiving** is opt-in via the `backup` Compose profile; does not affect default or observability profiles.
5. **Chaos harness** is schema-validation-only in CI. Full experiment execution requires a running stack with toxiproxy and is run weekly via the `chaos-soak` workflow.
6. Phase 7 introduces no changes to the public REST API, job schema, plugin ABI, or SDK interfaces. All changes are internal reliability infrastructure.

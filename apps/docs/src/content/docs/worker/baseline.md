---
title: Browser Worker Baseline
description: Milestone 0 baseline for the UBAG browser worker fleet.
---

# Browser Worker Baseline

This document turns the worker-owned parts of `UBAG_World_Class_Blueprint_v2.md` into a docs-first implementation contract. It covers the Python worker process, browser runtime, queue ownership boundary, session handoff, artifact capture, safe automation rules, and drift signals emitted by adapter execution.

## Source blueprint anchors

- Section 3.1: Python 3.12, uvloop, Playwright, and Patchright are the worker runtime baseline.
- Section 3.4: browser automation uses Chromium for Testing, Patchright, realistic fingerprints, optional proxy hooks, and manual CAPTCHA handling.
- Section 12: sessions are pooled, warmed, persistent where required, and manually repairable through login flows.
- Section 13: the worker owns adapter execution, selector fallback, recording and replay, and per-session resource governance.
- Section 18: logs, metrics, traces, synthetic checks, and SLO signals must cross gateway, worker, and browser boundaries.
- Section 21.5: screenshots, recordings, and artifacts land in object storage or a local content-addressed filesystem in edge mode.

## Responsibilities

The browser worker accepts assigned jobs, leases a compatible browser session, runs the selected adapter, streams intermediate events when supported, captures artifacts, normalizes adapter output, and returns a typed result or a named failure.

The worker must not become a second gateway. It does not authenticate client apps, enforce public API versioning, own tenant billing, or decide job admission. It does enforce local resource limits and must fail closed when an assigned job would exceed target, tenant, session, memory, or artifact policy.

## Execution model

1. Receive an assigned job from the orchestrator with `job_id`, `tenant_id`, `target`, `adapter_version`, priority lane, idempotency key, command payload, artifact policy, timeout budget, and trace context.
2. Resolve the adapter manifest and verify that the requested command is supported by the adapter capability contract.
3. Lease a warm session from the matching pool, or create one if the pool budget allows it.
4. Run preflight checks: target health, login state, session quarantine status, fingerprint consistency, and adapter selector baseline version.
5. Execute the adapter in a bounded browser context with cooperative cancellation and hard-kill fallback.
6. Emit progress, token, artifact, drift, and heartbeat events with the original trace context.
7. Normalize output, validate declared schemas, persist artifacts, acknowledge the queue message, and release or recycle the session.

## Worker process boundaries

| Boundary | Milestone 0 contract |
|---|---|
| Runtime | Python 3.12 async process with uvloop; Playwright-compatible automation using Patchright where stealth is required. |
| Browser binary | Pinned Chromium for Testing installed by the worker image or edge bundle. |
| Queue input | Assigned jobs only; admission control remains in the gateway and orchestrator. |
| Storage output | Artifact metadata plus object references; large binary artifacts are never returned inline by default. |
| Observability | JSON logs, Prometheus metrics, OpenTelemetry spans, and structured event records. |
| Cancellation | Cooperative adapter cancellation first; browser context termination after grace expires. |

## Baseline invariants

- A job has exactly one active worker lease at a time.
- A browser context is assigned to at most one active job at a time.
- A persistent profile belongs to one tenant, one target, and one account binding.
- Adapter code can request browser actions only through the worker adapter runtime.
- Every worker-visible failure maps to a stable `UBAG-WORKER-*`, `UBAG-SESSION-*`, `UBAG-ARTIFACT-*`, or `UBAG-ADAPTER-*` code.
- Artifact capture must respect redaction, retention, and tenant residency policy before bytes are persisted.
- Worker shutdown drains in-flight jobs up to a bounded grace period and then returns unacknowledged work to the queue.

## MVP deliverables

| Deliverable | Acceptance gate |
|---|---|
| Worker bootstrap | Starts with config validation, exposes health and metrics, and can load the mock adapter. |
| Session pool | Can create, warm, lease, release, recycle, and quarantine browser sessions. |
| Adapter runtime | Executes the contract in `docs/src/content/docs/adapters/contract.md` against the mock target. |
| Artifact capture | Produces screenshots, DOM snapshots, HAR metadata, and trace references under a deterministic naming scheme. |
| Manual login handoff | Moves a session to manual action, exposes a noVNC-compatible viewer lease, and resumes after operator release. |
| Drift signal | Emits a structured drift event when selectors fall back or DOM baselines exceed threshold. |

## Open decisions

- Whether the Milestone 1 worker package is Python-only or exposes a thin gRPC control surface.
- Whether Patchright is mandatory for every target or selected per adapter manifest.
- The first object-storage implementation for local development: filesystem-only or MinIO by default.
- The default retention window for screenshots, HAR files, and recordings in development.

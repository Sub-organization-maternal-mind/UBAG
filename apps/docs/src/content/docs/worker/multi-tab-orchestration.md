---
title: Multi-Tab Orchestration And Concurrency
description: Tab-parallel execution, multi-provider browser contexts, channel-tab pools, AIMD adaptive concurrency, race safety, and fair scheduling.
---

# Multi-Tab Orchestration And Concurrency

UBAG runs many user-owned provider sessions at once without losing the per-account ownership boundary. The worker organizes work into a strict three-level hierarchy and adapts its own pressure on each target so automation stays slow, polite, and ToS-safe.

This page covers blueprint sections §12.6–§12.13.

## Topology hierarchy

```
Browser instance
  └── Provider context   (one isolated storage profile per provider + identity)
        └── Channel tab   (one in-flight conversation / job slot)
```

- **Browser instance** — an engine process (Chromium, Firefox, or WebKit) that hosts one or more isolated provider contexts.
- **Provider context** — an isolated browser context bound to a single provider and a single user-owned identity. Storage state never crosses contexts. The dashboard exposes only a boolean `has_storage_state` indicator, never a storage-state URI.
- **Channel tab** — a single tab dedicated to one job. Tabs are the unit of concurrency and the unit of failure isolation.

## Tab lifecycle (§12.11)

Each channel tab moves through an explicit lifecycle. The Browser Topology dashboard panel renders each state as a badge.

| State | Meaning |
|---|---|
| `warming` | Tab is opening, restoring user-owned storage state, and reaching a ready surface. |
| `ready` | Tab is idle and available to accept a job. |
| `busy` | Tab is actively running a job step. |
| `draining` | Tab is finishing its current job and will not accept new work. |
| `quarantined` | Tab hit a manual-action, drift, or safety signal and is isolated pending operator review. |

A quarantined tab never silently resumes. It waits for an operator or for an explicit policy-driven recycle.

## Concurrency model (§12.6, §12.8)

Concurrency is **tab-parallel**, not request-parallel. The worker opens a bounded pool of channel tabs per provider context and dispatches one job per tab. This keeps per-account behavior close to how a human uses multiple tabs, instead of hammering an endpoint.

- Each provider context owns a channel-tab pool with a configurable floor and ceiling.
- A job is admitted only when a `ready` tab exists or the pool may grow under its current ceiling.
- Tabs are reused across jobs after a clean drain, preserving the warmed, user-owned session.

## Multi-provider browser (§12.7)

A single browser instance can host contexts for multiple providers at once (for example ChatGPT, Claude, Gemini). Each provider context remains fully isolated:

- separate storage state and cookies,
- separate identity binding,
- separate concurrency ceiling,
- separate drift and manual-action accounting.

## Adaptive concurrency — AIMD (§12.9)

Per provider **and** per identity, the worker maintains an Additive-Increase / Multiplicative-Decrease (AIMD) ceiling on concurrent tabs. The Concurrency dashboard panel shows the current cap, the min/max bounds, in-flight count, and the reason for the last change.

- **Additive increase** — after a streak of clean, successful job completions the cap rises by one, up to the configured maximum.
- **Multiplicative decrease** — the cap is cut hard on any of these ToS-safety signals:
  - a CAPTCHA or challenge wall,
  - a provider slow-down / rate banner,
  - an HTTP 429 or equivalent throttling response,
  - repeated timeouts or surface instability.

After a cut the context enters a cooldown before it may grow again. The bias is always toward backing off rather than pushing harder.

## Shared-context race safety (§12.10)

Tabs inside the same provider context may share warmed storage state, so the worker serializes context-mutating operations:

- storage-state writes, login refreshes, and identity rebinds take a per-context lock,
- read/submit job steps run in parallel across tabs but never overlap a storage-state write,
- a tab that needs a context-level change requests it explicitly and waits, rather than racing other tabs.

This prevents two tabs from corrupting a single user-owned session.

## Failure isolation (§12.12)

Failure is contained at the smallest possible scope:

- a tab-level error quarantines only that tab,
- a context-level error (for example an expired login) drains and recycles only that provider context,
- an instance-level fault recycles one browser instance while peers keep running.

A manual-action or drift signal moves the affected tab to `quarantined` and emits the corresponding event instead of retrying blindly.

## Scheduling and fairness (§12.13)

The scheduler shares finite tab capacity fairly:

- jobs are dispatched per provider context in a fair, round-robin-style order so no single tenant or identity starves the others,
- priority and per-tenant quotas bound how much of a context's ceiling one caller may hold,
- when a context is at its AIMD ceiling, additional jobs queue rather than forcing new tabs open.

## Observability

The Operator Dashboard surfaces this topology read-only:

- **Browser** panel — instance → context → tab tree with state badges and a boolean storage-state indicator.
- **Concurrency** panel — per provider/identity AIMD caps, bounds, in-flight, and last-change reason.

These panels are presentation-only and never expose credentials, cookies, or storage-state URIs.

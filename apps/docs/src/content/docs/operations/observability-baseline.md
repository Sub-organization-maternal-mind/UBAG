---
title: Observability Baseline
description: Milestone 0 observability contract for UBAG docs, dashboard, and operator workflows.
---

# Observability Baseline

Milestone 0 observability defines what the product must explain, emit, and preserve before production behavior is trusted. The baseline is intentionally source-driven: no dashboard number should appear unless its source and freshness are known.

## Goals

- Make operator status explainable from logs, events, traces, and human runbook notes.
- Separate product analytics from system health.
- Preserve enough evidence to debug failed releases and failed jobs.
- Avoid vanity metrics until the underlying data contract is implemented.

## Signal Classes

Use four signal classes across dashboard and docs:

- Product events: user or operator actions such as submit, approve, archive, cancel, retry, or export.
- System events: service lifecycle, queue state, scheduled job result, deployment, and integration sync.
- Audit events: permission changes, destructive actions, data export, manual override, and release approval.
- Experience events: navigation failures, client errors, slow route reports, and failed media loads.

Each event must declare an owner, source system, timestamp, environment, and correlation identifier when available.

## Required Context

Every emitted event or log line should answer:

- What happened?
- Who or what initiated it?
- Which environment was affected?
- Which resource was touched?
- Was the outcome success, failure, skipped, or partial?
- What should an operator inspect next?

Do not log secrets, payment credentials, authentication tokens, private keys, raw card data, or unnecessary personal data.

## Dashboard Observability Panels

The first dashboard observability panels should be compact and operational:

- Release lane: current release candidate, approval state, deployment state, rollback link.
- Jobs: latest scheduled jobs, status, duration, retry count, and owner.
- Integrations: AI provider targets, browser session backends, object storage, queue, webhook, and observability connection state.
- Incidents: open incidents, severity, owner, current action, next review time.
- Client health: route errors, hydration or rendering failures, and failed asset loads.
- Evidence: links to test runs, screenshots, audits, and release notes.

If a panel has no live source, show `Not connected` with the intended source contract. Do not replace missing sources with sample numbers.

## Event Naming

Use a stable, lower-case event pattern:

```text
domain.resource.action.outcome
```

Examples:

```text
dashboard.release.approve.success
dashboard.release.approve.failure
jobs.submit.requested
jobs.submit.blocked
operations.job.retry.requested
operations.incident.note.added
```

Names should describe behavior, not UI labels. If the UI copy changes, the event name should remain stable.

## Log Severity

- `debug`: local development or deep investigation details.
- `info`: expected lifecycle or operator action.
- `warn`: recoverable issue, retry, degraded source, or partial sync.
- `error`: failed workflow needing operator or automated recovery.
- `fatal`: service cannot continue safely.

Use `warn` for a missing optional source and `error` for a missing required source.

## Trace Boundaries

Trace the workflows that cross service or integration boundaries:

- Login and permission resolution.
- Job submission and idempotency reservation.
- Browser session assignment and manual-login takeover.
- Adapter execution and output extraction.
- Webhook delivery and replay.
- Artifact capture and retention.
- Release approval and deployment.

Trace names should match the workflow, not a route file name.

## Evidence Retention

For each release candidate, preserve:

- Test command or workflow identifier.
- Commit or artifact identifier when available.
- Environment.
- Result.
- Timestamp.
- Operator or automation owner.
- Link to raw logs, screenshot, report, or generated artifact.

If the project has no artifact store yet, the release note must state where evidence is temporarily kept.

## Milestone 0 Acceptance

Observability is ready for implementation when:

- Signal classes and event naming are documented.
- Missing data behavior is explicit.
- Logs and events have privacy boundaries.
- Release evidence requirements are defined.
- Operator runbook steps reference observable signals rather than guesswork.

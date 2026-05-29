---
title: Operator Runbook
description: Milestone 0 operator runbook for UBAG incident, release, and recovery workflows.
---

# Operator Runbook

This runbook defines the first operational response model for UBAG. It uses the current v0 command surface and identifies external activation inputs only where a live provider account, Docker engine, or deployment host is required.

## Operator Principles

- Preserve customer trust before preserving release speed.
- Prefer visible, reversible actions.
- Record the evidence used for each decision.
- Escalate when ownership or data source is unclear.
- Do not mask missing data with invented dashboard values.

## Daily Open

At the start of an operating window:

1. Confirm the active environment and release lane.
2. Check open incidents and unresolved release blockers.
3. Review target adapter, queue, storage, webhook, and browser-session readiness.
4. Review scheduled jobs for failures or retries.
5. Confirm no manual override is active without an owner.
6. Record handoff notes for any active risk.

## Incident Intake

Create an incident when:

- Job submission, browser session assignment, manual login, adapter execution, webhook delivery, or release promotion fails.
- Operators cannot determine source freshness.
- A release introduces user-visible regression.
- A data export, permission change, or destructive action is suspicious.
- Customer-impacting content or pricing appears incorrect.

Minimum incident fields:

- Title.
- Severity.
- Environment.
- First observed time.
- Reporter.
- Owner.
- Affected workflow.
- Current user, operator, or automation impact.
- Evidence links.
- Next action.

## Severity Guide

- `SEV1`: security, credential, data integrity, or production job execution risk.
- `SEV2`: major operator workflow or release path blocked.
- `SEV3`: degraded source, retryable job failure, or contained customer impact.
- `SEV4`: docs, copy, cosmetic, or non-blocking issue.

Severity can be raised or lowered as evidence changes. Record the reason.

## Triage Flow

1. Identify the affected workflow.
2. Confirm the environment.
3. Check the latest release or configuration change.
4. Inspect dashboard status and observability evidence.
5. Reproduce with the smallest safe path.
6. Decide: rollback, disable, retry, hotfix, or monitor.
7. Record the decision and next review time.

If reproduction requires user or provider-session data, use approved masked or test data only.

## Common Recovery Actions

### Release Regression

- Freeze new promotion of the affected release.
- Compare current release evidence with the previous accepted release.
- Roll back when the failure is production-impacting and reversible.
- Keep the incident open until verification confirms recovery.

### Failed Job

- Determine whether the job is required or optional.
- Check last successful run and current retry state.
- Retry once only when the failure mode is understood.
- Escalate repeated failures to the owning implementation area.

### Missing Source

- Mark the dashboard panel `Not connected` or `Blocked`.
- Do not substitute placeholder values.
- Identify source owner and expected recovery path.
- Add a release blocker if the source is required for the release lane.

### Permission or Audit Concern

- Preserve audit evidence.
- Pause the affected account, target, or action path if user, credential, or business risk exists.
- Escalate to the accountable owner.
- Do not delete or rewrite audit records.

## Handoff Notes

Each handoff should include:

- Current status.
- Active owner.
- Evidence reviewed.
- Actions taken.
- Open risks.
- Next decision point.

Use concise language. A new operator should be able to continue without replaying chat history.

## Command Slots

Current local commands:

```powershell
Check service health: cmd /c pnpm ops:health
Run v0 smoke and contract tests: cmd /c pnpm test:v0
Run aggregate checks: cmd /c pnpm check
Render small Compose config safely: .\deploy\small\small.ps1 -Action config
Render local secret-backed config only with approval: .\deploy\small\small.ps1 -Action config -AllowSecretConfigOutput
Run small profile smoke: .\deploy\small\small.ps1 -Action smoke
Run NATS small profile: .\deploy\small\small.ps1 -Action up -Profile queue
Enable webhook worker after Postgres migration: set UBAG_GATEWAY_STORE=postgres, UBAG_WEBHOOK_OUTBOX=postgres, UBAG_WEBHOOK_WORKER_ENABLED=true, and UBAG_WEBHOOK_SECRET
Open release evidence: PROGRESS.md and IMPLEMENTATION_COVERAGE.md
```

## Milestone 0 Acceptance

The runbook is ready when:

- Daily open, incident intake, triage, recovery, and handoff are documented.
- Severity definitions are explicit.
- Missing data behavior is tied to dashboard state language.
- Release and observability docs define the evidence the operator should inspect.

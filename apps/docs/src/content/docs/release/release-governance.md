---
title: Release Promotion Governance
description: Milestone 0 release governance for UBAG promotion, evidence, approval, and rollback.
---

# Release Promotion Governance

Release governance defines how UBAG moves from local work to a promoted release. Milestone 0 records the decision model before automation and deployment tooling are finalized.

## Release States

- `Draft`: work exists but is not ready for review.
- `Candidate`: implementation owner believes the work is ready for verification.
- `Blocked`: a required gate failed or evidence is missing.
- `Approved`: the release has required evidence and owner approval.
- `Promoted`: the release has been deployed or published to the target environment.
- `Rolled back`: the release was reverted or disabled after promotion.
- `Archived`: retained for audit and historical reference.

Every state change needs an owner, timestamp, environment, and evidence link when available.

## Governance Roles

- Implementation owner: owns the code or content change.
- Verification owner: confirms tests, accessibility, and visual gates.
- Operations owner: confirms runbook, monitoring, and rollback readiness.
- Release approver: authorizes promotion.

One person may hold multiple roles in early milestones, but the release note must make that explicit.

## Release Checklist

A release candidate is not eligible for approval until:

- Scope is documented.
- Changed paths are listed.
- Required tests or manual checks are recorded.
- Known gaps are listed.
- Observability expectations are documented.
- Operator runbook impact is reviewed.
- Rollback or disable path is known.
- No fabricated metrics, testimonials, logos, or claims were introduced.

## Evidence Packet

Each release should preserve:

- Release summary.
- Environment target.
- Commit, artifact, or content revision identifier when available.
- Test results.
- Visual or responsive verification notes for docs and dashboard UI.
- Accessibility verification notes.
- Observability verification notes.
- Rollback plan.
- Approval decision.

If the repo has no formal release artifact yet, store the evidence in the release note and link to external logs or screenshots when they exist.

## Approval Rules

Approval requires:

- No open `SEV1` or `SEV2` issue tied to the release path.
- Required evidence packet is complete.
- Blockers are either resolved or explicitly accepted by the release approver.
- The operator runbook covers any new manual recovery path.

Approval does not imply production deployment unless the release action explicitly says so.

## Rollback Rules

Rollback or disable should be considered when:

- Job submission, browser login, adapter execution, webhook delivery, or release publishing is broken.
- A release creates data integrity risk.
- Observability cannot confirm safe operation.
- The operator cannot recover with documented steps.

Rollback notes must include what changed, why it changed, who approved it, and what verification confirmed recovery.

## Freeze Conditions

Pause promotion when:

- Required tests are failing.
- Release evidence is missing.
- The affected workflow has an unresolved incident.
- The release includes a security, privacy, credential, provider-session, or data integrity uncertainty.
- Ownership is unclear.

The release can resume only after the freeze reason has an owner and documented resolution.

## Milestone 0 Acceptance

Release governance is ready when:

- Release states and roles are documented.
- Evidence packet requirements are clear.
- Approval and rollback rules are explicit.
- The runbook, testing baseline, and observability baseline use the same state and severity language.

---
title: Privacy Modes
description: Standard, HIPAA, and GDPR mode boundaries for UBAG.
---

# Privacy Modes

Status: Milestone 0 docs-first baseline.

Privacy modes define which data classes, controls, and operating commitments are allowed for a tenant or workflow.

## Mode Summary

| Mode | Milestone 0 status | Intended use |
| --- | --- | --- |
| Standard | Active default | General UBAG use without regulated-data commitments. |
| HIPAA | Future gated mode | Workflows that may involve protected health information after required controls and agreements exist. |
| GDPR | Future gated mode | Workflows that require EU/UK personal-data governance after required controls and processes exist. |

## Standard Mode Requirements

Standard mode requires:

- Data minimization by default.
- Tenant-scoped authorization.
- Audit events for sensitive actions.
- Secrets kept out of source control and logs.
- User consent for credentialed browser-login sessions.
- No collection of regulated candidate data unless a feature explicitly gates and approves it.

## HIPAA Mode Readiness

HIPAA mode remains disabled until UBAG has:

- Defined PHI data classes and blocked accidental PHI collection paths.
- Signed business associate agreements where required.
- Access controls with minimum necessary enforcement.
- Audit controls for PHI access and disclosure.
- Breach response procedures and evidence retention.
- Workforce/admin access procedures.
- Vendor and subprocessors review for PHI handling.

## GDPR Mode Readiness

GDPR mode remains disabled until UBAG has:

- Lawful basis mapping by processing purpose.
- Data subject request intake, verification, fulfillment, and audit trails.
- Retention and deletion policies per data class.
- Processor/subprocessor inventory.
- Cross-border transfer review where applicable.
- Consent and withdrawal controls where consent is the lawful basis.
- Privacy notice and customer-facing terms aligned to the shipped product.

## Mode Changes

Mode changes must be:

- Requested by an authorized actor.
- Evaluated against readiness checks.
- Logged with actor, tenant, old mode, new mode, reason, and result.
- Reversible only when data handling rules allow it.
- Blocked when the target mode has unmet controls.


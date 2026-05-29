---
title: Compliance Baseline
description: Milestone 0 compliance posture for UBAG.
---

# Compliance Baseline

Status: Milestone 0 docs-first baseline.

UBAG starts in Standard privacy mode. HIPAA and GDPR modes are future product modes that require additional implementation, legal review, operational evidence, and customer-facing commitments before they can be enabled or marketed.

## Baseline Claims

- UBAG has a Standard privacy baseline.
- UBAG does not claim HIPAA compliance in Milestone 0.
- UBAG does not claim GDPR compliance in Milestone 0.
- Future compliance modes must be explicit, gated, documented, tested, and auditable.

## Standard Mode

Standard mode supports general product development and non-regulated use cases. It still requires:

- Authentication and authorization for protected data.
- Tenant-scoped access controls.
- Secrets management.
- Audit logging for sensitive actions.
- Safe browser-login controls.
- Data minimization in logs, analytics, and support workflows.

## Future Modes

Future HIPAA and GDPR modes must not reuse Standard mode by label only. Each mode needs its own readiness checklist, evidence, owner, customer terms, incident workflow, and operational controls.

## Documentation Rule

Docs, UI copy, sales material, and implementation comments must not imply regulated compliance unless the corresponding mode is shipped, enabled, and approved.


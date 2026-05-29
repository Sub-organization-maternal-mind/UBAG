---
title: Compliance Modes
description: Standard, HIPAA-lite, GDPR-lite, and enterprise compliance scope.
---

## Standard

Standard is the default. It includes tenant isolation, basic audit, scoped credentials, rate limits, signed webhooks, and configurable retention.

## HIPAA-lite

Planned for v1/v2. It disables semantic cache for PHI templates, redacts logs, restricts recordings and DOM snapshots, enforces retention, and records access audit trails.

## GDPR-lite

Planned for v1/v2. It adds subject export and erase workflows, data residency tags, retention enforcement, and audit evidence.

## Enterprise

Enterprise adds SSO/SAML/SCIM, mTLS, SIEM export, immutable audit evidence, access reviews, and multi-region residency enforcement.

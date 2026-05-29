---
title: Security Baseline
description: Milestone 0 security baseline for UBAG.
---

# Security Baseline

Status: Milestone 0 docs-first baseline.

This page defines the minimum security posture for UBAG before feature implementation begins. It is a product engineering baseline, not a compliance certification or external audit report.

## Defaults

- Privacy mode defaults to `Standard`.
- HIPAA and GDPR modes are future gated modes. They must not be represented as active or compliant until their controls, evidence, contracts, and operational procedures are implemented and reviewed.
- Access is denied by default. Every protected action needs an authenticated actor, an authorized purpose, and an auditable outcome.
- Secrets must stay out of source control, documentation examples, analytics payloads, browser automation traces, and support tickets.
- Audit logging is required for security-sensitive actions, administrative changes, data access, and safe browser-login flows.

## Data Classes

| Class | Examples | Baseline handling |
| --- | --- | --- |
| Public | Public product pages, public documentation | Safe for unauthenticated access after content review. |
| Internal | Roadmaps, internal runbooks, non-secret configuration | Authenticated team access only. |
| User confidential | Account profile, private workspace data, uploaded content | Tenant-scoped access, minimum necessary logging, no disclosure to unrelated users. |
| Regulated candidate | Health, payment, government ID, or special-category personal data | Treat as blocked or explicitly gated until a future compliance mode approves collection. |
| Secret | API keys, tokens, credentials, session cookies, private keys | Managed only through approved secret storage and rotation paths. |

## Control Areas

- Authorization: RBAC handles coarse access. ABAC handles tenant, resource, purpose, environment, and privacy-mode constraints.
- Audit: Security events must be structured, tamper-evident enough for investigation, and free of secret values.
- Secrets: Secret values must be injected at runtime and rotated through documented ownership paths.
- Safe browser-login controls: Browser automation must use explicit user consent, isolated sessions, and narrowly scoped authenticated actions.
- Compliance: Standard mode is the only Milestone 0 default. HIPAA and GDPR modes are design targets until separately enabled.

## Milestone 0 Acceptance

- Security and compliance docs exist before implementation work depends on them.
- Engineers can identify the current privacy mode and understand that `Standard` is the only active default.
- Sensitive actions have named audit events before code adds them.
- Authorization design separates roles from contextual policy checks.
- Browser-login flows have documented safety boundaries before any credentialed automation is built.


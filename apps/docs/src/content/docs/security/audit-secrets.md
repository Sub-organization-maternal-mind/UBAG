---
title: Audit and Secrets
description: Audit logging and secrets handling baseline for UBAG.
---

# Audit and Secrets

Status: Milestone 0 docs-first baseline.

Audit logs and secret controls are paired because incident response depends on trustworthy records, and those records must not leak credentials or sensitive payloads.

## Audit Principles

- Log who did what, to which resource, under which tenant, for what purpose, and with what result.
- Use stable event names and structured fields.
- Never log passwords, API keys, refresh tokens, session cookies, private keys, one-time codes, or full secret-adjacent headers.
- Avoid logging full user confidential payloads. Prefer identifiers, data class, counts, hashes, and policy decision metadata.
- Preserve enough context for investigation without turning audit logs into a shadow data store.

## Required Event Families

| Family | Examples |
| --- | --- |
| Authentication | Login success/failure, logout, MFA challenge, session revocation. |
| Authorization | Privileged allow/deny, policy changes, role assignment changes. |
| Data access | Sensitive read, export, deletion, bulk operation, support access. |
| Secrets | Secret reference created, rotated, disabled, failed resolution. |
| Browser login | Consent granted, session started, authenticated action attempted, session cleared. |
| Compliance mode | Privacy mode proposed, enabled, disabled, or rejected. |

## Minimum Fields

- Event name.
- Timestamp in UTC.
- Actor ID and actor type.
- Tenant or workspace ID when applicable.
- Resource type and resource ID when applicable.
- Action and result.
- Data class when applicable.
- Privacy mode.
- Request correlation ID.
- Source system or integration.
- Reason or ticket reference for support and administrative actions.

## Secret Handling

- Store secret values only in approved runtime secret stores.
- Reference secrets by stable names or IDs, never by value.
- Rotate secrets on owner change, suspected exposure, vendor compromise, and scheduled rotation windows.
- Scope service credentials to the smallest tenant, environment, action set, and lifetime that works.
- Block documentation examples that look like real credentials.
- Treat browser session cookies and OAuth refresh tokens as secrets.

## Review Rhythm

- Audit event schemas are reviewed before implementation.
- New privileged actions require an audit event entry.
- Secret owners must be named before production use.
- Rotation and revocation procedures must be tested before declaring a future regulated mode ready.


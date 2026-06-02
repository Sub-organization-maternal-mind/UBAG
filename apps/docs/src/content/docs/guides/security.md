---
title: Security Guide
description: Security model, controls, and best practices for UBAG deployments.
---

This guide summarizes the UBAG security model and the controls available to operators
and application developers.

## Security model summary

| Layer | Control |
|-------|---------|
| Transport | TLS 1.3 on all external connections; NATS mTLS for internal |
| Authentication | `app_secret` (bcrypt-hashed); SSO/OIDC + MFA for humans |
| Authorization | RBAC (role-level) + ABAC (resource-level policies) |
| Audit | Hash-chained audit log at `/v1/audit` |
| Artifacts | Encrypted at rest in Garage S3 |
| Secrets | Never stored in plaintext; bcrypt-hashed; rotatable |

See [Security Model](/security/model) for the detailed technical specification.

## Authentication best practices

- Treat `UBAG_APP_SECRET` as a high-privilege credential — store in a secrets manager (Vault, AWS SSM, GCP Secret Manager)
- Rotate secrets on a schedule (90 days) or immediately after a suspected compromise: [Rotate a Secret](/cookbook/04-rotate-secret)
- Use separate app secrets per environment (dev/staging/prod)
- Enable SSO for human access — service accounts use API keys only

## Authorization

UBAG enforces RBAC on all API endpoints:

| Role | Can do |
|------|--------|
| `admin` | All operations |
| `editor` | Create/cancel jobs, manage webhooks, view audit |
| `viewer` | Read-only: list jobs, view artifacts |

ABAC policies can restrict access to specific resources (e.g., "editor can only cancel jobs they created").

See [RBAC and ABAC](/security/rbac-abac).

## MFA

Require MFA for all human users: [Configure MFA](/cookbook/10-configure-mfa).

## Audit log

Every mutation is recorded. The audit log is hash-chained to detect tampering:

```bash
ubag-cli audit verify --gateway http://localhost:8081 --token $UBAG_APP_SECRET
```

See [Audit and Secrets](/security/audit-secrets).

## SIEM integration

Forward audit events to your SIEM: [Configure SIEM](/cookbook/32-configure-siem).

## Threat model

See [Threat Model](/security/threat-model) for the STRIDE analysis and trust boundaries.

## Compliance

- [HIPAA Mapping](/compliance/hipaa)
- [GDPR Mapping](/compliance/gdpr)
- [SOC2 Mapping](/compliance/soc2)

## Vulnerability reporting

Report security vulnerabilities to `security@ubag.io`. Do not open public GitHub issues for vulnerabilities.
PGP key available at `https://ubag.io/.well-known/security.txt`.

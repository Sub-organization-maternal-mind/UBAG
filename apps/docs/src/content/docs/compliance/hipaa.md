---
title: HIPAA Compliance Mapping
description: How UBAG Phase controls map to HIPAA Security Rule requirements for operators handling PHI.
---

UBAG is not itself a covered entity or business associate. However, operators who use
UBAG to automate interactions with healthcare portals or handle PHI through browser
automation must ensure their deployment satisfies HIPAA requirements.

This mapping shows which UBAG features correspond to HIPAA Security Rule safeguards.

## Administrative Safeguards

| HIPAA Control | UBAG Feature | Reference |
|--------------|-------------|-----------|
| Security Management Process (§164.308(a)(1)) | Risk assessment via Threat Model; STRIDE analysis | [Threat Model](/security/threat-model) |
| Workforce Training (§164.308(a)(5)) | Operator Guide, Security Guide | [Guides](/guides/security) |
| Access Management (§164.308(a)(3)) | RBAC roles (admin/editor/viewer); SCIM provisioning | [RBAC and ABAC](/security/rbac-abac) |
| Incident Response (§164.308(a)(6)) | Runbook, alert procedures | [Runbook](/operations/runbook) |
| Contingency Plan (§164.308(a)(7)) | Backup/restore runbook; DR RTO/RPO targets | [Disaster Recovery](/operations/disaster-recovery) |
| Audit Controls (§164.308(a)(1)(ii)(D)) | Hash-chained audit log; SIEM export | [Audit and Secrets](/security/audit-secrets) |

## Physical Safeguards

UBAG is a software platform; physical safeguards are the responsibility of the infrastructure
provider (cloud or on-premise). Operators should ensure:

- Servers running UBAG workers are in access-controlled facilities
- Disk encryption is enabled on all nodes (Garage S3 encrypts artifacts at rest)

## Technical Safeguards

| HIPAA Control | UBAG Feature | Reference |
|--------------|-------------|-----------|
| Access Control (§164.312(a)(1)) | RBAC/ABAC; unique `app_secret` per application | [RBAC and ABAC](/security/rbac-abac) |
| Audit Controls (§164.312(b)) | Hash-chained audit log; export to SIEM | [Audit Export](/security/audit-export-merkle) |
| Integrity Controls (§164.312(c)) | Artifact hash verification; NATS payload hashing | [Security Model](/security/model) |
| Person Authentication (§164.312(d)) | SSO/OIDC + MFA (TOTP, WebAuthn) | [SSO Sessions](/security/sso-sessions) |
| Transmission Security (§164.312(e)) | TLS 1.3 (external); NATS mTLS (internal) | [Security Model](/security/model) |

## Encryption at rest

Browser session artifacts (screenshots, DOM captures, HAR files) that may contain PHI
are stored in Garage S3 with server-side encryption. Configure encryption keys via:

```toml
[storage]
encryption = "aes256"
kms_key_id = "${KMS_KEY_ID}"  # AWS KMS or compatible
```

## Minimum necessary access

Implement ABAC policies to restrict artifact access to the specific jobs and users
that require it. Do not grant blanket `admin` access for service accounts.

## Business Associate Agreement

If you process PHI using UBAG on behalf of a covered entity, you may need a BAA
with your infrastructure providers (cloud, database, storage). UBAG itself does not
store or process PHI — artifacts are controlled entirely by the operator.

## Operator checklist

- [ ] MFA enabled for all human users accessing the dashboard
- [ ] Audit log forwarded to SIEM and retained for 6+ years
- [ ] App secrets rotated on schedule (≤90 days)
- [ ] Artifact encryption enabled in storage configuration
- [ ] Backup and restore tested against DR runbook
- [ ] Incident response plan includes HIPAA breach notification procedure

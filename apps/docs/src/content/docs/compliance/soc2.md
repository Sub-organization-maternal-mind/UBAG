---
title: SOC 2 Compliance Mapping
description: How UBAG controls address the SOC 2 Trust Services Criteria (Security, Availability, Confidentiality).
---

This page maps UBAG platform controls to the AICPA SOC 2 Trust Services Criteria (TSC).
Organizations pursuing SOC 2 Type II certification for a system that includes UBAG should
use this as a starting point for their controls narrative.

## CC6 — Logical and Physical Access Controls

| Criteria | UBAG Control |
|----------|-------------|
| CC6.1 — Logical access restricted to authorized users | RBAC (admin/editor/viewer); `app_secret` per application; SSO/OIDC |
| CC6.2 — Prior to issuing credentials, register and authorize users | SCIM provisioning from IdP; SSO-gated dashboard login |
| CC6.3 — Remove access when no longer required | SCIM de-provisioning (DELETE `/scim/v2/Users/{id}`); immediate session invalidation |
| CC6.6 — Logical access security measures (MFA) | TOTP + WebAuthn MFA enforced by policy |
| CC6.7 — Restrict transmission of confidential information | TLS 1.3 (external); NATS mTLS (internal); pre-signed artifact URLs (15-min TTL) |
| CC6.8 — Prevent/detect unauthorized software | WASM plugin sandbox; adapter conformance checks; dependency scanning (Dependabot) |

## CC7 — System Operations

| Criteria | UBAG Control |
|----------|-------------|
| CC7.1 — Detect and monitor for anomalies | Prometheus metrics + Grafana alerts; manual-action alert queue |
| CC7.2 — Monitor system components | `/v1/health` endpoint; per-component health checks; K8s liveness/readiness probes |
| CC7.3 — Evaluate security events | SIEM integration; audit log export; hash-chain tamper detection |
| CC7.4 — Respond to identified security incidents | Incident response runbook; on-call rotation |
| CC7.5 — Identify and remediate vulnerabilities | Dependabot; `cargo audit`; `npm audit` in CI |

## CC9 — Risk Mitigation

| Criteria | UBAG Control |
|----------|-------------|
| CC9.1 — Risk assessment | STRIDE threat model; residual risk register |
| CC9.2 — Monitor changes to risk environment | Adapter drift detection; schema-breaking change CI checks |

## A1 — Availability

| Criteria | UBAG Control |
|----------|-------------|
| A1.1 — Capacity planning | Worker pool auto-scaling; NATS backpressure; rate limits prevent overload |
| A1.2 — Environmental protections | Cloud-managed infrastructure; multi-AZ (standard tier); multi-region (enterprise tier) |
| A1.3 — Recovery and backup | Daily backups (small); WAL streaming (standard); pgactive sync (enterprise); tested DR runbook |

## C1 — Confidentiality

| Criteria | UBAG Control |
|----------|-------------|
| C1.1 — Identify confidential information | Job tags support `classification` metadata; artifact types are explicitly typed |
| C1.2 — Protect confidential information from deletion or disclosure | Artifact encryption at rest; RBAC gates artifact access; pre-signed URL TTL |

## PI1 — Processing Integrity

| Criteria | UBAG Control |
|----------|-------------|
| PI1.1 — Process inputs completely | Job contract enforced: all required fields validated before dispatch |
| PI1.2 — Process outputs completely | Artifact hash verification; job lifecycle state machine enforces completion |
| PI1.4 — Inputs and outputs are accurate | Idempotency keys prevent duplicate processing; hash-chained audit log |

## Evidence artifacts for SOC 2 audit

| Artifact | How to obtain |
|---------|--------------|
| Access log (who logged in when) | `/v1/audit?action=user.login` |
| User list + roles | `/scim/v2/Users` export |
| MFA enrollment records | `/v1/audit?action=user.mfa_enrolled` |
| Secret rotation log | `/v1/audit?action=secret.rotated` |
| Vulnerability scan results | CI pipeline SARIF output |
| Backup test records | Disaster recovery runbook execution log |

## Operator checklist

- [ ] SSO + MFA enforced for all dashboard users
- [ ] SCIM provisioning connected to IdP
- [ ] Audit log exported to SIEM and retained ≥12 months
- [ ] Vulnerability scanning in CI (`cargo audit`, `npm audit`, Dependabot)
- [ ] DR runbook tested and results documented
- [ ] Incident response procedure documented and tested
- [ ] Change management process documented (Conventional Commits, PR reviews, CI gates)

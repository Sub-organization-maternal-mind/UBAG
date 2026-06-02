---
title: Threat Model
description: STRIDE threat model for the UBAG gateway, worker, and sidecar trust boundaries.
---

This document describes the trust boundaries, assets, and STRIDE threat analysis for the UBAG platform.

## Assets

| Asset | Sensitivity | Location |
|-------|------------|---------|
| `app_secret` | Critical | Postgres (bcrypt hash only) |
| Browser session credentials | High | Worker memory + Garage S3 (encrypted) |
| Job inputs/outputs | Medium–High | Postgres + Garage S3 |
| Audit log | High | Postgres (hash-chained) |
| NATS credentials | High | Worker/gateway env |
| Admin SSO tokens | High | Gateway session store |

## Trust Boundaries

| Component | Trusts | Does Not Trust |
|-----------|--------|----------------|
| Gateway | App secrets (verified), Postgres, NATS (mTLS) | Worker (treated as untrusted network peer) |
| Worker | Gateway job contracts (via NATS mTLS) | Target sites (scraped content treated as hostile) |
| Sidecar | Worker (local IPC only) | Network |
| Dashboard | Gateway REST API (Bearer token) | Direct DB access |
| Plugin (WASM) | Explicit capability grants only | Everything else (sandboxed) |

## STRIDE Analysis

### Spoofing

| Threat | Mitigation |
|--------|-----------|
| Forged `app_secret` to submit jobs | Secrets hashed with bcrypt; constant-time comparison; rotate via `/v1/apps/{id}/rotate-secret` |
| Worker impersonation | Workers authenticate with mTLS certificates (CN = `worker_id`); gateway rejects unknown certs |
| SSO token replay | Nonce claim verified in OIDC callback (fix: Task 2.4); short-lived ID tokens; session binding |
| SCIM token theft | SCIM tokens scoped to provisioning only; revocable; separate from app secrets |

### Tampering

| Threat | Mitigation |
|--------|-----------|
| Job payload modified in transit (gateway ↔ worker) | NATS mTLS; message-level payload hashing |
| Artifact modified after upload | Garage S3 object hashes stored in Postgres; verified on download |
| Audit log tampered | Hash-chained entries: each entry includes SHA-256 of previous entry; chain verified by `ubag-cli audit verify` |
| Config tampered at rest | Config managed via GitOps; Helm values tracked in version control |

### Repudiation

| Threat | Mitigation |
|--------|-----------|
| Actor denies creating a job | Every mutation written to hash-chained audit log with `actor`, `action`, `resource`, timestamp |
| Actor denies rotating a secret | `secret.rotated` audit event includes actor and timestamp |
| Worker denies completing a job | Worker sends signed `CompleteJob` gRPC call; gateway records receipt |

### Information Disclosure

| Threat | Mitigation |
|--------|-----------|
| Browser session credentials leaked | Sessions are ephemeral; credentials stored in encrypted Garage S3 with short TTL |
| Job inputs exposed to unauthorized actor | RBAC enforces `viewer` ≥ read access; ABAC for resource-level isolation |
| Artifacts downloaded by unauthorized actor | Artifact URLs are pre-signed with 15-minute TTL; access gated by job ownership |
| Audit log read by unauthorized actor | `viewer` role cannot access audit log; `admin`/`editor` only |
| NATS subject disclosure | Subjects are namespaced per deployment (`ubag.<deploy-id>.*`); workers cannot subscribe to other deployments |

### Denial of Service

| Threat | Mitigation |
|--------|-----------|
| Job flood overwhelming gateway | Per-actor rate limits (`/v1/rate-limits`); configurable per-app limits |
| NATS consumer lag causing memory exhaustion | Backpressure via NATS consumer max-pending; dead-letter queue |
| Worker browser OOM | Per-worker memory limit enforced by cgroups; browser crash triggers job failure, not worker crash |
| Large artifact DoS | Max artifact size enforced at upload; pre-signed URL TTL limits amplification |

### Elevation of Privilege

| Threat | Mitigation |
|--------|-----------|
| Viewer token used to cancel jobs | RBAC enforced on all mutation endpoints; `viewer` cannot call POST/DELETE/PATCH |
| Adapter code executing arbitrary shell commands | Adapters run in isolated worker process; no shell access from Playwright context |
| Plugin exceeding sandbox | WASM sandbox: no filesystem, no network, memory-limited (16 MB), CPU-limited (50 ms/hook) |
| SSO → admin escalation via group claim | Group claim mapping is explicit and operator-configured; no auto-promotion |

## Attack Surface

| Entry point | Authentication | Rate limited |
|------------|---------------|-------------|
| `/v1/*` REST API | Bearer token (app_secret) | Yes |
| `/v1/sso/*` OIDC flow | IdP-managed | Yes |
| `/scim/v2/*` SCIM | SCIM bearer token | Yes |
| Dashboard (SPA) | SSO session cookie (HttpOnly, Secure, SameSite=Strict) | Yes |
| NATS (internal) | mTLS certificate | No (trusted network) |
| gRPC (worker ↔ gateway) | mTLS certificate | No (trusted network) |
| Postgres (internal) | Password (private network) | No (trusted network) |

## Residual risks

- Browser fingerprinting by target sites can identify automation — mitigated by browser stealth mode but not eliminated
- Zero-day vulnerabilities in Chromium/Playwright — mitigated by regular browser updates (weekly in CI)
- Supply chain attacks on npm/cargo dependencies — mitigated by lockfiles, Dependabot, and SBOM generation

## References

- [Security Model](/security/model)
- [RBAC and ABAC](/security/rbac-abac)
- [Audit and Secrets](/security/audit-secrets)
- [SSO Sessions and Logout](/security/sso-sessions)

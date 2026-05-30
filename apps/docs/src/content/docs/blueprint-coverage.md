---
title: Blueprint Coverage
description: Map from the UBAG world-class blueprint to milestone documentation and implementation phases.
---

## Coverage policy

Every major blueprint section is represented in the docs site and mapped to implemented, contracted, or externally activated repo evidence. Implementation is staged into Milestone 0, v0, v1, and v2.

## Milestone map

| Feature family | First documented | First implemented | Notes |
| --- | --- | --- | --- |
| Product vision and principles | M0 | M0 | Planning baseline. |
| API gateway and contracts | M0 | v0 | REST, SSE, idempotency, stable errors first. |
| WebSocket stream | M0 | v0 | `/v1/stream` accepts WebSocket upgrade, validates the WebSocket key, and keeps the stream open with heartbeat frames. |
| gRPC | M0 | v1 | Protobuf service contracts track REST lifecycle operations. |
| Browser worker and adapter SDK | M0 | v0 | Mock runtime, generic manifests, provider safe-mode stubs, and manual-session events. |
| AI provider adapters | M0 | v1 | DeepSeek, ChatGPT, Claude, Gemini, Mistral, Perplexity, generic chat/form, mock. |
| Manual login and noVNC | M0 | v0-v1 | User-owned accounts and audited operator actions. |
| SQLite edge profile | M0 | v0 contracted | SQLite/localfs contracts and migrations exist; runtime gateway persistence currently uses memory or opt-in Postgres/MinIO. |
| Small compose profile | M0 | v0 | Postgres, MinIO, Caddy ingress, Dragonfly, Grafana/Prometheus, optional NATS profile. |
| Security and audit | M0 | v0-v1 | App-secret/idempotency, RBAC/ABAC contracts, audit, webhook signing, rate-limit contract. |
| SDKs | M0 | v0-v2 | TypeScript, Python, Go first; all 11 by v2. |
| Dashboard | M0 | v0-v1 | Static NAJM-styled operator prototype now; live gateway-wired admin dashboard remains v1. |
| Plugins and marketplace | M0 | v2 | WASM capability model. |
| Enterprise and multi-region | M0 | v2 | SSO/SCIM, mTLS, HA, SIEM, DR. |

## v2.1 observability and concurrency surfaces

| Blueprint section | Documentation | Conformance | Notes |
| --- | --- | --- | --- |
| §12.6–§12.13 Multi-tab orchestration and concurrency | `worker/multi-tab-orchestration` | `multi_tab_topology`, `adaptive_concurrency` coverage; `browser.*`, `concurrency.list.ok` scenarios | Browser → context → tab hierarchy, tab lifecycle, tab-parallel concurrency, AIMD ceilings, race safety, failure isolation, fair scheduling. |
| §13.10–§13.12 Cross-engine and remote grids | `worker/cross-engine-grids` | `cross_engine` coverage | Pluggable Chromium (CDP) / Firefox & WebKit (BiDi), remote browser grids, engine-portable selectors. |
| Manual-action alerts | `operations/manual-action-alerts` | `manual_action_alerts` coverage; `alerts.list.ok`, `alerts.config.ok`, `alerts.acknowledge.ok`, `alerts.resolve.ok` | Human-solved CAPTCHA/login via noVNC, SMTP routing (no password exposed), acknowledge/resolve lifecycle. |
| §11.6 Audit export and Merkle chain | `security/audit-export-merkle` | `audit_export_chain` coverage; `audit.export.chain-valid` | Hash-chained, tamper-evident audit export with `chain_valid`. |
| SSO sessions and logout | `security/sso-sessions` | `sso_session` coverage; `sso.logout.ok` | SSO-minted revocable sessions; immediate logout/revocation. |
| §22 Enterprise Postgres persistence | `data/postgres-persistence` | `postgres_persistence` coverage | Revised schema on PostgreSQL with edge SQLite parity and storage-state redaction. |

These surfaces are exposed read-only in the Operator Dashboard (Browser, Concurrency, and Alerts panels) and never expose credentials, cookies, storage-state URIs, or SMTP secrets.

See `PROGRESS.md` for the detailed feature-by-feature mapping.

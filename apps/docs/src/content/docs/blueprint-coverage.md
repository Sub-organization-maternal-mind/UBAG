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

See `PROGRESS.md` for the detailed feature-by-feature mapping.

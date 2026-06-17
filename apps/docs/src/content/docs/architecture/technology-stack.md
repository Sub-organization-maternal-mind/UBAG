---
title: Technology Stack
description: Technology choices for UBAG and the phase where each becomes active.
---

| Layer | Choice | Phase |
| --- | --- | --- |
| Gateway/control plane | Go, net/http | v0 |
| Browser worker | Python 3.10+ standard-library safe-mode runner; Playwright/Patchright browser runtime attaches after manual session hardening | v0-v1 |
| Sidecar | TypeScript/Node loopback proxy package | v0 |
| CLI | TypeScript/Node command package | v0 |
| Docs | Astro Starlight | M0 |
| Dashboard | SvelteKit gateway-wired operator dashboard with Hallmark/NAJM CSS system | v0-v1 |
| Edge database | SQLite WAL runtime stores and localfs artifact mode | v0-v1 |
| Small database | Postgres 16 gateway stores | v0 opt-in, v1 hardening |
| Queue | local file-spool and NATS JetStream | v0 opt-in, v1 hardening |
| Blob storage | in-memory default, MinIO opt-in, localfs runtime mode | v0 opt-in, v1 hardening |
| Observability | Prometheus/Grafana scaffolding plus metric/event/log/probe contracts; Loki/Tempo/profiling later | v0 contracts, v1 hardening |
| SDK contract generation | OpenAPI, Protobuf, JSON Schema, generated TypeScript/JavaScript and Go contract manifests, conformance fixtures | v0-v2 |

## Constraint

All required runtime dependencies must be open-source and self-hostable.

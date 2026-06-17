---
title: Roadmap
description: Staged delivery plan from docs baseline to enterprise ecosystem.
---

## Milestone 0: Docs-first baseline

- PRD, progress ledger, ADRs, docs site, and coverage check.
- Architecture and public contract docs.
- Docs-first gate completed before product service code was introduced.

## v0: Edge MVP

- REST jobs API, SSE, app-secret auth, idempotency, stable errors.
- SQLite/localfs edge runtime, memory/Postgres gateway stores, MinIO/localfs artifacts, and small-profile deployment scaffolding.
- Mock target, generic adapter manifests, adapter SDK contract, and provider-safe manual-session stubs for DeepSeek, ChatGPT, Claude, Gemini, Mistral, and Perplexity.
- Basic worker safe-mode dispatch and manual login handoff events.
- CLI, sidecar, gateway-wired dashboard, guarded WebSocket baseline, built-in template catalog/application/rendering, workflow/cache foundations, opt-in small-profile Postgres, NATS, MinIO, signed webhook outbox, observability, and TypeScript/JavaScript plus Go SDK wave.

## v1: Production OSS platform

- Production hardening for the implemented Docker Compose small profile, Postgres, MinIO, optional NATS, signed webhooks, observability, and TS/Go SDK wave.
- Live user-owned provider adapters, expanded workflow/template/cache behavior, normalization, and semantic cache hardening.
- Production deployment activation, live runtime smoke, and release governance evidence.
- Compliance-mode activation after legal review and deployed control evidence.

## v2: Enterprise and ecosystem

- WASM plugins and adapter marketplace.
- Helm, Terraform, installers, GitOps.
- SSO/SAML/SCIM, mTLS, multi-region, SIEM export, mobile monitor, DR.

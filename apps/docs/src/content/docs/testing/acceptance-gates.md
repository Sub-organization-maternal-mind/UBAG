---
title: Acceptance Gates
description: Build, behavior, performance, and review gates by phase.
---

## Milestone 0

- `pnpm install` succeeds.
- `pnpm check:blueprint` succeeds.
- `pnpm docs:build` succeeds.
- Docs site renders locally.
- Docs-first baseline was completed before product service code was introduced.

## v0

- `pnpm test:schema` succeeds.
- `pnpm test:docs` succeeds.
- `pnpm test:worker` succeeds for the Python worker harness and adapter registry.
- `pnpm test:v0` succeeds as the chained schema, edge-store, security, worker, SDK, conformance, observability, CLI, dashboard, deployment, docs, and gateway gate.
- Local edge job succeeds from SDK/CLI.
- Idempotent replay returns same job/result.
- Mock target and generic adapter pass contract tests.
- First real adapter path passes repo-side manifest, safe-mode, and manual-login contract tests; live account execution requires user-owned credentials and provider access.

## v1

- Gateway p99 under 100 ms excluding browser work.
- Warm browser job p50 target under 15 seconds where provider allows.
- Signed webhook delivery eventually succeeds or DLQs with operator evidence.
- SDK conformance passes for first wave.

## v2

- All SDKs pass conformance.
- Helm/Terraform deployments pass smoke tests.
- Multi-region failover proves target RPO/RTO.
- Release artifacts include signatures, SBOM, and provenance.

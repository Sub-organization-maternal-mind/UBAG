---
title: Acceptance Gates
description: Build and lint gates by phase. Automated tests were removed on 2026-10-08.
---

Automated tests are not a gate any more. Acceptance is the compile, typecheck and
lint set below.

## Every change

- `pnpm install --frozen-lockfile` succeeds.
- `pnpm lint` succeeds (OpenAPI, JSON Schema, proto, blueprint coverage, REST contracts, SDK freshness).
- `pnpm typecheck` succeeds.
- `gofmt -l apps/gateway` prints nothing, and `go vet ./...` succeeds in `apps/gateway`.
- `ruff check apps/worker adapters` succeeds.

## Before a deploy to main

- `pnpm dashboard:build` succeeds.
- `pnpm docs:build` succeeds.
- `docker build -f deploy/small/gateway.Dockerfile .` succeeds (the CI gateway-image workflow does this).

## Deferred behaviour checks

Behaviour is verified live after deploy. Problems found in production are fixed by hand.

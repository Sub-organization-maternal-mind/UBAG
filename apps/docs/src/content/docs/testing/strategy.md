---
title: Testing Strategy
description: Language checking and linting only. Tests were removed on 2026-10-08.
---

## Current state

Tests were removed on 2026-10-08. The last commit that still contains them is
tagged `pre-strip-tests`. Nothing in the repository runs a test suite now.

What still runs:

- Typecheck: `cmd /c pnpm typecheck` (TypeScript packages, dashboard and mobile Svelte check).
- Lint and contract gates: `cmd /c pnpm lint` (OpenAPI, JSON Schema, proto, blueprint coverage, REST contracts, SDK freshness).
- Go: `gofmt`, `go vet` and `go build` for the gateway, the operator and the Go SDK.
- Worker: `ruff check apps/worker adapters`.
- Compile and build: dashboard build, docs build (`astro check`), Rust sidecar `cargo check`.
- Secret scan: gitleaks in CI.

Production issues are found live and fixed by hand. A deploy to `main` runs the
gateway image build and the dashboard build, then deploys; no test step gates it.

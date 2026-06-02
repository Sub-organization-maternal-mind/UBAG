---
title: Contributor Guide
description: How to contribute code, docs, adapters, and plugins to the UBAG project.
---

Welcome! Contributions to UBAG — code, documentation, adapters, plugins, and bug reports — are all valued.

## Getting started

```bash
git clone https://github.com/ubag/ubag
cd ubag
pnpm install
```

## Prerequisites

| Tool | Version |
|------|---------|
| Node.js | 20+ |
| pnpm | 9+ |
| Go | 1.22+ |
| Rust | 1.78+ (for WASM plugins) |
| Docker | 24+ |

## Project structure

See [Repository Structure](/architecture/repository-structure) for the full layout.

Key directories:

```
apps/gateway/       — Rust gateway binary
apps/worker/        — TypeScript worker
apps/docs/          — Astro + Starlight docs
packages/openapi/   — OpenAPI 3.1 spec (source of truth)
packages/proto/     — protobuf definitions
packages/sdk/       — TypeScript SDK
tools/              — CI validation scripts
adapters/           — AI provider adapters
```

## Workflow

1. Open an issue or pick one from the backlog
2. Create a branch: `git checkout -b feat/my-feature`
3. Make changes — docs-first if adding a feature (ADR-0006)
4. Run tests: `pnpm test`
5. Run contract checks: `pnpm check:contracts`
6. Open a PR — CI must pass before merge

## Code style

- TypeScript: ESLint + Prettier (auto-applied via pre-commit hook)
- Rust: `cargo fmt` + `cargo clippy`
- Go: `gofmt` + `golangci-lint`

## Docs

Documentation lives in `apps/docs/src/content/docs/`. All new features must include
a docs update (ADR-0006: docs-first workflow).

Build and preview the docs:

```bash
cd apps/docs
pnpm dev
```

## Testing

```bash
pnpm test              # all tests
pnpm test:unit         # unit only
pnpm test:integration  # integration (requires Docker)
pnpm check:contracts   # OpenAPI spec vs. implementation
pnpm check:cookbook    # cookbook recipe validation
```

See [Testing Strategy](/testing/strategy) for the test pyramid.

## Commit messages

Follow Conventional Commits: `feat(scope): description`.
Examples: `feat(gateway): add region-affinity job field`, `fix(sso): verify nonce claim`.

## ADRs

Significant decisions require an ADR in `apps/docs/src/content/docs/adrs/`.
See [ADR-0006](/adrs/0006-docs-first-workflow) for the process.

## Code of conduct

Be respectful. See `CODE_OF_CONDUCT.md` in the repository root.

---
title: "ADR-0014: SvelteKit Dashboard Rewrite and Phase 10 Quality Gates"
description: Decision record for the SvelteKit adapter-static dashboard and Phase 10 testing/docs/governance gates.
---

# ADR-0014: SvelteKit Dashboard Rewrite and Phase 10 Quality Gates

**Status**: ACCEPTED
**Date**: 2026-06-02
**Deciders**: UBAG Maintainers

## Context

Phase 10 closes two open items from the UBAG v2.1 blueprint:
1. The §24.2 dashboard rewrite (deferred from Phase 5)
2. The §32/§34/§35 testing, docs, and governance scaffolding

## Decisions

### 1. Dashboard: SvelteKit 2 + adapter-static + Skeleton UI + Tailwind

**Decision**: Rewrite the vanilla JS/HTML/CSS dashboard in SvelteKit 2 + Svelte 5 (runes) + `@sveltejs/adapter-static` + Skeleton UI + Tailwind CSS.

**Rationale**:
- `adapter-static` produces a zero-runtime static build that drops into the existing Caddy `/dashboard/*` serve path unchanged
- Svelte 5 runes provide modern reactivity without the overhead of React/Vue
- Skeleton UI + Tailwind + the existing `tokens.css` oklch palette preserves the "Hallmark" brand identity
- Mirrors the `apps/mobile` tooling (Svelte 5 + Vite + TypeScript) for consistency

**Rejected alternatives**:
- React/Next.js: no SSR needed; heavier runtime
- Plain SvelteKit with SSR: requires a server runtime, breaking the static Caddy path

### 2. Coverage gate: ≥80%

**Decision**: Go gateway coverage gated at 80% in CI; fail below.

**Rationale**: 80% is achievable with the existing test suite and provides a meaningful signal without incentivizing coverage-gaming.

### 3. Load regression gate: 20%

**Decision**: Load test baselines published in `tests/load/baselines.json`; CI fails on >20% regression vs baseline.

**Rationale**: 20% allows for infrastructure variance while catching real regressions.

### 4. License split: AGPL-3.0 server + Apache-2.0 clients

**Decision**: Gateway and worker use AGPL-3.0; all client libraries (SDKs, sidecar, dashboard, CLI) use Apache-2.0.

**Rationale**: AGPL ensures modifications to the server are open-sourced; Apache-2.0 on clients enables broad adoption without copyleft concerns.

### 5. DCO over CLA

**Decision**: Require DCO `Signed-off-by:` on all commits instead of a Contributor License Agreement.

**Rationale**: DCO is lighter-weight and developer-friendly; sufficient for a project of this scale.

## Consequences

- The SvelteKit dashboard is built in CI as part of `node-suite`
- Playwright visual regression runs against all 17 §24.2 pages
- The `make test-all` umbrella covers unit + coverage gate + conformance 250+ + observability + dashboard
- Separate scheduled workflows handle load, E2E, and chaos testing

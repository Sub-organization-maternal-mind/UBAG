---
rfc: 0001
title: Strict-Fidelity v2.1 Blueprint Implementation
status: IMPLEMENTED
created: 2026-01-15
authors:
  - UBAG Maintainers <maintainers@ubag.dev>
---

# RFC-0001: Strict-Fidelity v2.1 Blueprint Implementation

## Summary

Adopt a "strict-fidelity" implementation strategy for the UBAG v2.1 blueprint: every §-numbered item is treated as a hard requirement, not a guideline. Deviation requires a documented decision record.

## Motivation

Earlier implementations (v1.x) made informal decisions to defer or simplify blueprint items, leading to significant drift between the spec and the implementation. The Phase 8 ADR (0012) formalized the problem; this RFC captures the governance decision.

## Design

### Implementation gates

- Every phase has an explicit acceptance checklist in `apps/docs/`
- CI runs blueprint coverage checks (`pnpm check:blueprint`)
- Deferred items require an ADR explaining why

### Phase sequencing

Phases 0–9 complete the v2.1 blueprint. Phase 10 (this release) adds the testing, docs, and governance scaffolding to make the project releasable and contributable.

## Drawbacks

Strict fidelity increases implementation time. Some blueprint items are speculative and their value is unclear until implemented.

## Alternatives

- **Best-effort fidelity**: implement the spirit, not the letter. Rejected because it reproduces the v1.x drift problem.
- **Feature flags**: gate unfinished items. Rejected because it creates a permanent maintenance burden.

## Status

IMPLEMENTED as of Phase 10 completion.

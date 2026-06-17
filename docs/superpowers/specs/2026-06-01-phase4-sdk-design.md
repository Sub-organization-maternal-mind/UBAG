# Superseded: Phase 4 SDK Design

**Date:** 2026-06-17

This earlier Phase 4 SDK design has been superseded by the TS + Go SDK-only product direction.

## Current SDK Scope

UBAG now treats only these SDKs as first-class active deliverables:

- TypeScript/JavaScript: `@ubag/sdk`
- Go: `github.com/ubag/ubag-go`

The previous Rust SDK scope, along with broader non-TS/Go SDK expansion, is not active. The prior detailed design remains recoverable from Git history if the product direction changes later.

## Active Contract Policy

Generated SDK contract manifests are maintained and checked only for:

- `packages/sdk-typescript/src/generated/contract-manifest.ts`
- `packages/sdk-go/generated_contract_manifest.go`

The canonical generator is `tools/make-sdks/generate-manifest.mjs`; use `--check` for freshness validation without mutating files.

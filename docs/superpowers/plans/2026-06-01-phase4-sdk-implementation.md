# Superseded: Phase 4 SDK Implementation Plan

**Date:** 2026-06-17

This previous SDK implementation plan has been superseded by the TS + Go SDK-only completion policy.

## Current Implementation Target

Only two SDK technologies are active:

- TypeScript/JavaScript: `@ubag/sdk`
- Go: `github.com/ubag/ubag-go`

The prior Rust SDK tasks and all broader non-TS/Go SDK tasks are intentionally removed from the active plan. Git history is the archive for those details.

## Active Validation

The active SDK validation path is:

```powershell
cmd /c pnpm check:sdk-freshness
cmd /c pnpm test:sdk:typescript
cmd /c pnpm test:sdk:go
cmd /c pnpm test:sdk
```

`test:sdk` must remain scoped to generated manifest freshness plus the TypeScript/JavaScript and Go test suites.

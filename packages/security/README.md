# @ubag/security

Security and compliance contract scaffolding for UBAG.

This package is intentionally gateway-adjacent. It provides shared TypeScript helpers and typed contracts for app-secret bearer credentials, device tokens, RBAC/ABAC decisions, rate-limit decisions, audit events, and webhook HMAC signing.

## Scope

- No runtime dependencies.
- No bundled secrets or environment lookup.
- Uses Node `crypto` primitives for hashing, HMAC, random bytes, and timing-safe comparisons.
- Exposes contracts that gateway, SDK, worker, and conformance code can adopt later.

## Commands

```powershell
cmd /c pnpm --filter @ubag/security typecheck
cmd /c pnpm --filter @ubag/security test
cmd /c pnpm --filter @ubag/security validate
```

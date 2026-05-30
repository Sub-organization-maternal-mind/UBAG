---
title: SSO Sessions And Logout
description: SSO-minted operator sessions, session validation, and explicit logout/revocation.
---

# SSO Sessions And Logout

UBAG supports operator sessions minted through single sign-on (SSO) so that dashboard and control-plane access is tied to an organization's identity provider, with explicit logout and revocation.

## SSO-minted sessions

When an operator authenticates through the configured SSO identity provider, the gateway mints a UBAG session that carries:

- the operator's identity and tenant binding,
- the roles/claims used for RBAC and ABAC decisions,
- an expiry, after which the session must be re-established,
- a server-side session reference that can be revoked independently of token expiry.

The session reference — not raw provider credentials — is what UBAG validates on each request. UBAG never stores the identity provider's password or long-lived secret.

## Session validation

Every authenticated request is checked against the active session:

- the session must exist, be unexpired, and not be revoked,
- the bound tenant and roles drive authorization,
- a missing, expired, or revoked session is rejected before any job or admin action runs.

## Logout and revocation

Logout is explicit and immediate via `POST /v1/sso/logout`:

| Endpoint | Purpose |
|---|---|
| `POST /v1/sso/logout` | Revoke the caller's current SSO-minted session. |

After logout:

- the server-side session reference is invalidated immediately,
- subsequent requests with the old session are rejected even if the token's expiry has not passed,
- a corresponding audit event is recorded.

This gives operators a hard "sign out everywhere this session was used" control rather than waiting for token expiry.

## Safety stance

- UBAG validates a revocable server-side session, not just a self-contained token, so logout takes effect at once.
- SSO claims feed RBAC/ABAC; they do not bypass per-account ownership or consent rules on jobs.
- No identity-provider secret or password is persisted or returned.

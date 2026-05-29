---
title: Safe Browser-Login Controls
description: Security baseline for browser sessions, user consent, and credentialed automation.
---

# Safe Browser-Login Controls

Status: Milestone 0 docs-first baseline.

Safe browser-login controls apply when UBAG opens, drives, records, or relies on an authenticated browser session. The goal is to let users authorize narrow actions without UBAG becoming an uncontrolled credential handler.

## Allowed Baseline

- Use Standard privacy mode by default.
- Require explicit user consent before starting a credentialed browser session.
- Use isolated browser profiles or session containers per user, tenant, and purpose.
- Keep session lifetime short and clear session state when the approved task ends.
- Show the user which domain, account, and action scope are being requested before authentication.
- Record audit events for consent, session start, sensitive action attempts, and session end.

## Prohibited Baseline

- Do not collect, store, replay, or display user passwords.
- Do not store session cookies, refresh tokens, or one-time codes in source control, logs, screenshots, prompts, transcripts, analytics, or support artifacts.
- Do not bypass MFA, CAPTCHA, rate limits, paywalls, robots restrictions, or provider terms.
- Do not continue authenticated automation after consent expires or the stated purpose is complete.
- Do not use one user's session to access another tenant or unrelated account.
- Do not run hidden credentialed sessions without a clear user-visible reason and audit trail.

## Consent Requirements

Consent should capture:

- User and tenant.
- Target domain or service.
- Account identity when visible.
- Purpose and allowed action set.
- Start time, expiry, and revocation path.
- Data classes expected to be viewed or processed.
- Whether screenshots, downloads, or extracted records may be produced.

## Session Controls

- Prefer ephemeral sessions for one-time work.
- Use durable sessions only when the product explicitly needs them and the user can revoke them.
- Separate production, staging, and local sessions.
- Clear cookies, local storage, downloads, and captured artifacts when the session scope ends.
- Redact screenshots and traces before retention if they may contain confidential data.

## Audit Events

Required event names for implementation planning:

- `browser_login.consent_granted`
- `browser_login.consent_revoked`
- `browser_login.session_started`
- `browser_login.authenticated_action_attempted`
- `browser_login.session_cleared`
- `browser_login.policy_denied`


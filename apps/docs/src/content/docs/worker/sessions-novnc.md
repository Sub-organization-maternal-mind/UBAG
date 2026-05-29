---
title: Sessions And noVNC
description: Session lifecycle, manual login, noVNC viewer leases, and quarantine rules.
---

# Sessions And noVNC

Browser sessions are the worker's scarce resource. They hold browser context, persistent profile state, target account state, fingerprint choices, and operator handoff status. The Milestone 0 contract is to make session state explicit before code exists, so adapters cannot smuggle account, login, or viewer behavior into target-specific scripts.

## Lifecycle

```text
created
  -> warming
  -> ready
  -> leased
  -> releasing
  -> ready

leased
  -> manual_action_required
  -> ready

leased
  -> quarantined
  -> recycled
  -> terminated
```

## Session states

| State | Meaning | Allowed next states |
|---|---|---|
| `created` | Metadata row or local edge record exists, but no browser context is ready. | `warming`, `terminated` |
| `warming` | Worker launches browser context, applies fingerprint, preloads target origin, and runs adapter health checks. | `ready`, `quarantined`, `terminated` |
| `ready` | Session can be leased for a compatible job. | `leased`, `recycled`, `terminated` |
| `leased` | One job owns the browser context. | `releasing`, `manual_action_required`, `quarantined`, `terminated` |
| `manual_action_required` | Automation is paused for login, CAPTCHA, 2FA, consent, or user approval. | `ready`, `quarantined`, `terminated` |
| `releasing` | Worker is cleaning context state and deciding whether to keep or recycle the profile. | `ready`, `recycled`, `quarantined`, `terminated` |
| `quarantined` | Session is unsafe for normal automation until reviewed. | `recycled`, `terminated` |
| `recycled` | Persistent profile is being cleaned and re-warmed. | `warming`, `terminated` |
| `terminated` | Browser process and context are gone. | none |

## Pool keys

A session pool is keyed by the minimum state required for safe reuse:

```yaml
tenant_id: tenant_123
target: deepseek_web
adapter_family: deepseek_web
account_binding_id: acct_456
profile_persistence: persistent
region: local
fingerprint_policy: realistic_default
proxy_policy: none
```

The pool key prevents one tenant, account, target, or fingerprint family from leaking into another. The worker must reject reuse if any key field differs.

## noVNC viewer leases

The noVNC bridge is an operator handoff path, not a general remote desktop feature.

1. Adapter or worker detects `manual_action_required`.
2. Worker pauses the job and freezes automation input.
3. Worker requests a short-lived viewer lease from the control plane.
4. Operator opens the viewer, completes the requested action, and releases the session.
5. Worker reruns `ensure_logged_in` or the adapter-specific post-action check.
6. Session returns to `leased` for the paused job or `ready` if no job remains.

Viewer leases must be scoped to one session, one operator identity, one purpose, and one expiration. A viewer lease never exposes raw credentials through logs or artifacts.

## Manual action reasons

| Reason | Required handling |
|---|---|
| `login_required` | Operator signs in to a user-owned account. Store only the resulting browser profile state allowed by policy. |
| `captcha_required` | Operator solves in-browser. Do not integrate paid solver behavior by default. |
| `two_factor_required` | Operator completes the challenge outside worker logs and artifacts. |
| `terms_or_consent_required` | Operator must explicitly accept or decline; automation must not accept terms silently. |
| `target_policy_change` | Quarantine if the target changed automation-relevant rules or account constraints. |

## Concurrency limits

| Profile | Session target |
|---|---|
| `edge` | Up to 1-3 browser sessions. |
| `small` | Up to 10-30 browser sessions across 1-3 workers. |
| `standard` | Autoscaled by worker quota, target quota, and queue lag. |
| `enterprise` | Autoscaled with regional placement and tenant isolation. |

Per-worker active sessions must be bounded by memory, CPU, target, and tenant limits. The blueprint target is 50 idle sessions or 15 active sessions per worker for benchmark planning, not an unconditional default.

## Quarantine triggers

- Login state becomes ambiguous or belongs to the wrong account binding.
- Target presents CAPTCHA, 2FA, or consent loops repeatedly.
- Adapter selector fallback reaches the ML or vision fallback path.
- DOM drift score crosses the configured adapter threshold.
- Browser process crashes during sensitive account state transitions.
- Memory, DOM node count, or artifact size exceeds policy.
- Operator closes a viewer lease without resolving the manual action.

## Required events

| Event | Emitted when |
|---|---|
| `session.created` | Session record is allocated. |
| `session.warmed` | Browser context passes health checks. |
| `session.leased` | Job takes exclusive ownership. |
| `session.manual_action_required` | Worker pauses for operator handoff. |
| `session.viewer_lease_created` | noVNC lease is issued. |
| `session.viewer_lease_released` | Operator returns control. |
| `session.quarantined` | Worker blocks reuse pending review. |
| `session.recycled` | Profile cleanup starts. |
| `session.terminated` | Context and process are gone. |

## Milestone 0 acceptance

- Session state names and transitions are represented in implementation tickets.
- Manual action events include job, tenant, target, session, adapter version, reason, and trace IDs.
- noVNC leases are documented as short-lived, scoped, audited, and operator-initiated.
- Quarantined sessions cannot be leased by normal jobs.

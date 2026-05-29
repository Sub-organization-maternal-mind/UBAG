---
title: Safe User-Owned Automation
description: Rules for user-owned browser automation, consent, target respect, and bounded effects.
---

# Safe User-Owned Automation

UBAG automates browser sessions on behalf of a user, tenant, or app that owns the account or has explicit permission to operate it. The worker must make that ownership boundary enforceable in code and visible in audit records.

## Core rules

- Automate only accounts, sessions, targets, and workflows the requesting tenant is authorized to use.
- Do not collect, infer, export, or persist credentials beyond the approved browser profile or OS keychain flow.
- Do not bypass user consent, payment walls, access controls, account bans, or target safety controls.
- Do not silently accept new terms, privacy prompts, destructive confirmations, purchases, or account changes.
- Do not run unbounded loops against a target. Every job has timeout, retry, rate, and resource limits.
- Do not hide manual intervention. CAPTCHA, 2FA, consent, and policy-change screens move the session to manual action.

## Ownership fields

Every worker-assigned job must carry ownership and consent context.

```json
{
  "tenant_id": "tenant_123",
  "app_id": "app_456",
  "actor_id": "user_789",
  "target": "chatgpt_web",
  "account_binding_id": "acct_abc",
  "automation_scope": ["submit_prompt", "read_response"],
  "consent_ref": "consent_2026_05_22_001",
  "data_classification": "internal",
  "manual_action_policy": "operator_required"
}
```

The worker rejects jobs missing required ownership fields unless the deployment profile explicitly runs in single-user edge mode.

The gateway also applies the same safety stance before job storage or dispatch. Create-job payloads are rejected if they contain credential, cookie, token, API key, private-key, browser storage/session, client-supplied noVNC URL, MFA/TOTP, or CAPTCHA-solving material.

## Safe command classes

| Class | Examples | Default posture |
|---|---|---|
| Read-only | Open page, extract response, capture screenshot. | Allowed within target and account scope. |
| Content submission | Submit prompt, upload allowed file, continue conversation. | Allowed when template, target, and data class are permitted. |
| Account mutation | Change profile, accept terms, connect integration. | Manual approval required. |
| Financial or irreversible | Purchase, subscribe, delete account, send external message. | Blocked unless a later explicit policy enables it. |
| Security-sensitive | Password reset, MFA enrollment, token export. | Blocked for general automation. |

## Manual approval checkpoints

The worker pauses and emits `session.manual_action_required` when it sees:

- Login, CAPTCHA, 2FA, passkey, or consent prompts.
- New target terms or policy prompts.
- File permission prompts or local filesystem pickers.
- Destructive action confirmations.
- Requests to change account settings, identity, billing, or connected apps.
- Adapter uncertainty above the configured confidence threshold.

## Rate and behavior limits

| Limit | Why it exists |
|---|---|
| Per-target rate budget | Avoid hammering AI web targets and custom portals. |
| Per-tenant worker quota | Prevent one tenant from exhausting shared workers. |
| Per-account session cap | Avoid parallel use of the same target account in unsafe ways. |
| Per-job timeout | Keep queues recoverable and prevent stuck browser contexts. |
| Retry budget | Avoid infinite replays against fragile target states. |
| Artifact byte budget | Prevent sensitive or huge data dumps. |

## Audit trail

Every automated browser job must create audit records for:

- Job assignment and worker lease.
- Account binding used.
- Adapter name and version.
- Manual action prompts and operator handoff.
- Artifact capture and redaction outcomes.
- Drift fallback and selector uncertainty.
- Any blocked action and the rule that blocked it.

Audit records should contain stable IDs and event facts, not prompt bodies or credentials.

## Adapter obligations

Adapters must expose intent before action. For example, an adapter that clicks a button should identify the semantic action as `submit_prompt`, `download_result`, or `open_new_conversation`, not only `click(selector)`.

Adapters must return uncertainty instead of guessing when a target UI no longer matches the declared contract. The worker should treat uncertainty as a safety signal and prefer manual action, drift detection, or quarantine over continuing with low-confidence clicks.

## Milestone 0 acceptance

- Worker tickets include ownership fields in the assigned job envelope.
- Adapter tickets include semantic action names for browser operations.
- Manual-action and blocked-action events are part of the event taxonomy.
- No default path accepts terms, solves CAPTCHA, changes accounts, or performs irreversible actions without user or operator intervention.

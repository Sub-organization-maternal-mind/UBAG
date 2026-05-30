---
title: Manual-Action Alerts
description: Human-in-the-loop alerting for CAPTCHA and manual-login challenges, SMTP routing, and the acknowledge/resolve lifecycle.
---

# Manual-Action Alerts

UBAG never solves CAPTCHAs and never logs in on a user's behalf without a human. When a provider surface presents a challenge that only a person should clear, the worker pauses the affected tab and raises a **manual-action alert** so a human operator can take over the live, user-owned session.

## Why human-in-the-loop

A CAPTCHA, a 2FA prompt, or a fresh login screen is a deliberate human check. Automating past it would defeat the target's safety control and break the ownership boundary. UBAG's stance is explicit:

- the machine **detects** the challenge,
- the machine **alerts** a human,
- the **human** clears it in the live session (via noVNC takeover),
- the machine resumes only after the human resolves it.

This keeps automation ToS-safe and keeps a real, authorized person in control of account-sensitive moments.

## Alert lifecycle

When the worker detects a challenge it emits `session.manual_action_required` and creates an alert.

| Status | Meaning |
|---|---|
| `open` | A human action is required and nobody has picked it up yet. |
| `acknowledged` | An operator has claimed the alert and is working it. |
| `resolved` | The human cleared the challenge; the tab may resume. |

The Operator Dashboard **Alerts** panel shows the open queue with **Acknowledge** and **Resolve** actions. The dashboard only records that a human acted — it never performs the challenge itself.

## Alert kinds

| Kind | Trigger | Human action |
|---|---|---|
| `captcha` | A CAPTCHA / challenge wall appears mid-job. | Solve the CAPTCHA in the live session. UBAG never auto-solves. |
| `manual_login` | A login, passkey, or 2FA prompt appears. | Complete the user-owned login / 2FA in the live session. |

## SMTP routing

Alerts are delivered to operators by email and mirrored in the dashboard queue. SMTP is configured through environment variables:

| Variable | Purpose |
|---|---|
| `UBAG_ALERT_SINK` | Alert delivery sink (for example `smtp` plus the dashboard queue). |
| `UBAG_ALERT_SMTP_HOST` | SMTP server host. |
| `UBAG_ALERT_SMTP_PORT` | SMTP server port. |
| `UBAG_ALERT_SMTP_USERNAME` | SMTP username. |
| `UBAG_ALERT_SMTP_PASSWORD` | SMTP password — **secret**, never rendered in the dashboard or API responses. |
| `UBAG_ALERT_FROM` | From address for alert email. |
| `UBAG_ALERT_RECIPIENT` | Default operator recipient (for example `mindreader420123@gmail.com`). |

The `GET /v1/alerts/config` endpoint and the dashboard expose only non-secret status — sink type, a `smtp_configured` yes/no flag, the default recipient, and the resolution policy. The SMTP password is never returned.

## API surface

| Endpoint | Purpose |
|---|---|
| `GET /v1/alerts` | List manual-action alerts and their status. |
| `GET /v1/alerts/config` | Read non-secret alert routing config (no password). |
| `POST /v1/alerts/{id}/acknowledge` | Mark an alert as being handled by a human. |
| `POST /v1/alerts/{id}/resolve` | Mark the human action complete so the tab may resume. |

## Redaction guarantees

- The alert config never includes the SMTP password or any credential.
- Resolution is always **human-solved** via a noVNC takeover — there is no automated CAPTCHA bypass path.
- The dashboard shows a `smtp_configured` flag, not the SMTP secret.

---
title: "ADR 0005: User-Owned Browser Automation"
description: Web AI provider access uses manual login and audited user-owned accounts.
---

## Status

Accepted.

## Decision

Provider adapters use user-owned accounts with manual login through live session access. UBAG does not ship a CAPTCHA solver or scrape credentials.

## Consequences

Automation remains operator-audited and safer, but provider setup requires explicit account login.

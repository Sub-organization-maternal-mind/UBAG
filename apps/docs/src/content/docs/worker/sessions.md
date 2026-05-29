---
title: Browser Sessions
description: Session pools, manual login, noVNC, and resource governance.
---

## Pool key

Session pools are keyed by:

```text
tenant_id + target + profile_class
```

## Session states

Idle, ready, in use, awaiting login, quarantined, recycling, and terminated.

## Manual login

When a provider account needs login or re-login, the session enters `awaiting_login`. An authorized operator opens a short-lived live viewer token, completes login, records an audit reason, and returns the session to the pool.

## Resource limits

Workers enforce maximum RSS, contexts, pages, lifetime, jobs per session, and graceful drain before restart.

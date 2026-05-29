---
title: Drift Detection
description: Detection and recovery when target websites change.
---

## Signals

- Selector failures.
- ML/vision fallback usage.
- DOM structural diff from baseline.
- Synthetic monitor failures.
- Output length or duration anomalies.
- Session quarantine spikes.

## Response

- Emit drift event.
- Quarantine affected sessions.
- Trip adapter breaker when thresholds are crossed.
- Auto-rollback to previous healthy adapter version when available.
- Alert operators with the last successful and failed artifacts.

## Repair loop

Patch selectors, run adapter test against mock and live canary, promote by canary percentage, then roll forward when metrics are healthy.

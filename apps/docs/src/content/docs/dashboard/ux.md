---
title: Dashboard UX
description: NAJM-styled operator dashboard information architecture.
---

## Design system

Dashboard and docs surfaces inherit `design.md`: warm cream paper, ink text, terracotta actions, saffron and marine accents, geometric display type, tactile patterns, compact operational UI, and no fabricated metrics.

## Information architecture

- Overview: live job stream, queue depth, adapter drift, error rate, worker/session capacity, alerts.
- Ops: jobs, failed jobs, DLQ, browser sessions, workflows.
- Config: apps, devices, targets, adapters, templates, webhooks, cache.
- Org/admin: users, roles, quotas, audit log, settings.
- Observability: Grafana, logs, traces, worker shell links.

## States

Every interactive component needs default, hover, focus-visible, active, disabled, loading, error, and success states. Critical workflows include empty, skeleton, partial failure, permission denied, stale data, offline, destructive confirmation, and optimistic rollback states.

## Responsive gates

Check 320, 375, 414, 768, and desktop widths. No horizontal scroll. Use `overflow-x: clip` on `html` and `body`.

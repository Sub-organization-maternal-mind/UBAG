---
title: Dashboard UX
description: NAJM-styled operator dashboard information architecture.
---

> **Dashboard v2 (SvelteKit)**: The dashboard was rewritten in SvelteKit 2 + Svelte 5 + Tailwind + Skeleton UI as part of §24.2. Build with `npm run build` in `apps/dashboard/`; the static output in `dist/` is served by Caddy at `/dashboard/`.

## Design system

Dashboard and docs surfaces inherit `design.md`: warm cream paper, ink text, terracotta actions, saffron and marine accents, geometric display type, tactile patterns, compact operational UI, and no fabricated metrics.

## Information architecture

- Overview: live job stream, queue depth, adapter drift, error rate, worker/session capacity, alerts.
- Ops: jobs, failed jobs, DLQ, browser sessions, workflows.
- Config: apps, devices, targets, adapters, templates, webhooks, cache.
- Org/admin: users, roles, quotas, audit log, settings.
- Observability: Grafana, logs, traces, worker shell links.

## Queue reasons and shared fleet

The existing Overview, Jobs, Browser Sessions and Quotas pages show why work waits and where it runs. There is no new route or nav entry.

- Overview: a *Waiting jobs by reason* card, and a *Shared Fleet* section (eligible and total nodes, workload slots in use, nodes under pressure).
- Jobs: a *Queue Reason* column, the reason, its meaning and *Waiting Since* in the drawer, and a *Held by placement reason* card for operators.
- Browser Sessions: a *Placement* table (node label, region, state, last heartbeat, slots, pressure, provider-session readiness).
- Quotas: *Assigned Capacity* bars per node.

Fleet panels render only when `GET /v1/fleet/nodes` or `/v1/fleet/summary` answers `200`. A `501`, `404`, `403`, failure or unexpected body hides them; a value the gateway did not report shows an em dash, never an invented figure. Coarse queue reasons for a tenant's own jobs do not depend on the fleet routes.

## States

Every interactive component needs default, hover, focus-visible, active, disabled, loading, error, and success states. Critical workflows include empty, skeleton, partial failure, permission denied, stale data, offline, destructive confirmation, and optimistic rollback states.

## Responsive gates

Check 320, 375, 414, 768, and desktop widths. No horizontal scroll. Use `overflow-x: clip` on `html` and `body`.

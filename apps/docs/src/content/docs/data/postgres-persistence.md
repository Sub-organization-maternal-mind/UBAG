---
title: Enterprise Postgres Persistence
description: Enterprise-grade persistence on PostgreSQL, the revised database schema, and the edge SQLite profile.
---

# Enterprise Postgres Persistence

UBAG runs the same control-plane contracts on two persistence profiles: a single-file SQLite store for edge / single-user deployments, and PostgreSQL for enterprise, multi-tenant, durable operation. This page covers blueprint section §22 (revised database schema).

## When to use Postgres

| Profile | Store | Use case |
|---|---|---|
| Edge / single-user | SQLite | Local, single-operator, small-footprint deployments. |
| Enterprise | PostgreSQL | Multi-tenant, concurrent operators, durability, backups, HA. |

The job, target, session, alert, and audit contracts are identical across both. Switching profiles does not change the API.

## Revised schema (§22)

The enterprise schema persists the full control-plane state, including the v2.1 observability surfaces:

- **Tenancy and identity** — tenants, apps, actors, account bindings.
- **Jobs** — job records, lifecycle state, idempotency keys, results.
- **Targets and templates** — provider targets and reusable job templates.
- **Browser topology** — browser instances, provider contexts, and channel tabs with their lifecycle state and a boolean `has_storage_state` flag (never a storage-state URI).
- **Concurrency** — per provider/identity AIMD ceilings, bounds, and last-change reasons.
- **Manual-action alerts** — alert records, kind, status, and acknowledge/resolve history.
- **Audit** — hash-chained audit entries supporting tamper-evident export and `chain_valid` verification.
- **Sessions** — SSO-minted, revocable operator sessions.

Migrations for both providers live under the repository `migrations/` directory (`migrations/postgres/` and `migrations/sqlite/`).

## Concurrency and durability

PostgreSQL gives the enterprise profile what the edge profile cannot:

- safe concurrent writes from multiple gateway/worker instances,
- transactional integrity for race-sensitive operations (for example storage-state binds and AIMD cap updates),
- durable storage with standard backup, point-in-time recovery, and replication tooling,
- row-level isolation suitable for strict multi-tenant separation.

## Redaction in storage

Persistence follows the same redaction rules as the API and dashboard:

- credentials, cookies, tokens, and storage-state URIs are never stored as exportable secrets,
- browser context/tab rows store only a boolean storage-state indicator,
- alert routing stores SMTP status, not the SMTP password,
- audit payloads store redacted event metadata plus integrity hashes.

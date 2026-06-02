# pgactive Bidirectional Replication — UBAG Multi-Region

## Overview

UBAG ships with **uni-directional logical replication** as the default
(postgres-a → postgres-b, one writer, one read replica). This document describes
the optional **pgactive** bidirectional replication mode for deployments that
need multi-master writes across regions.

| Feature | Uni-directional logical replication | pgactive bidirectional |
|---|---|---|
| Direction | One primary → replicas | Both nodes can accept writes |
| Conflict risk | None (single writer) | Low with write-fencing; LWW fallback |
| Operational complexity | Low | Medium |
| Recommended for | Most UBAG deployments | Active-active, cross-region writes only |

---

## What pgactive Provides

`pgactive` (Bi-Directional Group Replication) is a PostgreSQL extension that
replicates changes between two or more nodes in both directions simultaneously.
Unlike streaming replication or standard logical replication, every node can
accept INSERT/UPDATE/DELETE. Changes are propagated asynchronously over logical
replication slots.

Key capabilities:
- Bidirectional replication (multi-master)
- Built-in conflict detection with pluggable resolution
- Compatible with standard PostgreSQL (via extension; requires WAL level = logical)
- Per-row conflict resolution using timestamps or custom handlers

---

## Write-Fencing Policy (UBAG Default)

UBAG avoids most pgactive conflicts by enforcing **home-region write fencing**:
all writes for a given tenant are routed to that tenant's `home_region`; all
other regions serve **reads only** for that tenant.

This is controlled by the `UBAG_POSTGRES_WRITE_REGION` environment variable:

```env
# WRITE_FENCE — set to the region identifier that owns writes for this node.
# The gateway reads this value and rejects write requests that arrive at a
# non-home-region node for the requesting tenant.
#
# Values: region-a | region-b | <custom>
# Leave unset to disable write-fencing (not recommended with pgactive).
UBAG_POSTGRES_WRITE_REGION=region-a
```

When `UBAG_POSTGRES_WRITE_REGION` is set, the gateway (future gate):
1. Inspects the `X-UBAG-Home-Region` header (or tenant metadata).
2. If the request's `home_region` != `UBAG_POSTGRES_WRITE_REGION`, the gateway
   returns `HTTP 307 Temporary Redirect` pointing to the home-region endpoint,
   or `HTTP 409 Conflict` for non-idempotent writes.

This eliminates the overwhelming majority of pgactive conflicts in practice.

---

## Conflict Resolution Stance

For the small number of writes that do race across regions (e.g., during a
failover window), UBAG relies on **pgactive's default last-write-wins (LWW)**
resolution by wall-clock timestamp:

- The row with the latest `updated_at` / `xact_commit_time` wins.
- Conflicting deletes are logged and the delete wins over an older update.
- Application code must treat `updated_at` as authoritative; blind overwrites
  are discouraged.

No custom conflict handlers are required because `home_region` write-fencing
prevents concurrent writes to the same row under normal operation.

---

## Enabling pgactive

pgactive is **not** available in stock `postgres:16` images. You need either:
- A pgactive-enabled image (e.g., a custom build from the pgactive source tree)
- An Aurora PostgreSQL-compatible image that ships with pgactive pre-installed
- The EDB Postgres Advanced Server distribution (includes pgactive)

### docker-compose override

Use `docker-compose.pgactive.yml` (sibling file) as an override to the
standard `docker-compose.multiregion.yml`:

```bash
docker compose \
  -f deploy/multi-region/docker-compose.multiregion.yml \
  -f deploy/multi-region/postgres/docker-compose.pgactive.yml \
  up -d
```

### Initialising the pgactive group

After both postgres nodes start, run the following SQL **once** from postgres-a:

```sql
-- On postgres-a: create the pgactive group
SELECT pgactive.pgactive_create_group(
  node_name    := 'region-a',
  node_dsn     := 'host=postgres-a port=5432 dbname=ubag user=ubag password=changeme'
);

-- Then join postgres-b into the group (run from postgres-b)
SELECT pgactive.pgactive_join_group(
  node_name    := 'region-b',
  node_dsn     := 'host=postgres-b port=5432 dbname=ubag user=ubag password=changeme',
  join_using_dsn := 'host=postgres-a port=5432 dbname=ubag user=ubag password=changeme'
);
```

### Verifying replication

```sql
-- Check node status on either node
SELECT node_name, node_status FROM pgactive.pgactive_show_nodes();
-- Expected: both nodes in 'r' (ready) state
```

---

## References

- pgactive GitHub: https://github.com/pgactive/pgactive
- NATS multi-region config: `deploy/multi-region/nats/`
- GeoDNS / routing: `deploy/multi-region/geodns/`
- Docker Compose override: `deploy/multi-region/postgres/docker-compose.pgactive.yml`

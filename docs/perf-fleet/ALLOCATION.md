# Perf-fleet cross-area allocation table

Single source for numbers/paths/flags claimed by multiple slices. Every contract slice cites this file. Re-check the highest number on `origin/feat/perf-fleet` at merge time and renumber on conflict (update this table in the same PR).

## ADR numbers (docs/adr/)

Highest on origin/main: 0005. Four planned ADRs each originally claimed 0006.

| No. | File | Slice |
|---|---|---|
| 0006 | helper-nodes-consume-manager-grants | P0.3 |
| 0007 | attempt-lease-contract | P0.3 |
| 0008 | rust-relay-adoption-gate | P0.3 |
| 0009+ | free (helper trust plane, lease-then-place note: take next free) | later |

## Migrations

origin/main postgres highest: 0021 (0019-0021 live: voice sessions, voice instance global, admission tokens). sqlite highest: 0013 (tracked separately; sqlite may fail closed for helper-plane features).

| Postgres | SQLite | Purpose (dependency order) |
|---|---|---|
| 0022_gateway_job_attempts | 0014_gateway_job_attempts | attempts |
| 0023_helper_nodes (landed, P4.4) | n/a (SQLite edge profile refuses helper mode; 0015 stays unused) | nodes + registry |
| 0024_helper_profiles | 0016_helper_profiles | profiles |
| 0025_voice_lease_generation | n/a | voice lease generation |
| 0026+ | 0017+ | free; P1.6 tenant-keyed topology upsert reserves one if schema impact exists |

## Paths, routes, flags

- Helper proto: `packages/proto/proto/ubag/helper/v1`.
- Routes: `/v1/fleet/*` guarded by scope `fleet:read`; internal node routes under `internal/nodes`.
- Flag ladder: `UBAG_HELPER_NODES` < `UBAG_HELPER_PLANE` < `UBAG_HELPER_DISPATCH` < `UBAG_HELPER_VOICE` (each requires the previous).
- Streaming flag pair: `UBAG_WORKER_STREAM_EVENTS` (Python emits) + `UBAG_WORKER_STREAM_INGEST` (Go ingests). Other flags: see BINDING.md section 3 rule 5.

## Phase map

P0 reconcile + measure (inert); P1 hardening/correctness; P2 streaming + SSE; P3 worker pool/attempts; P4 helper plane; P5 helper voice; P6 read-time queue_reason/contracts; P7 Rust gate + publication; P8 consolidation (P8.1 merges slice shards into PROGRESS.md).

# Perf-fleet cross-area allocation table

Single source for numbers/paths/flags claimed by multiple slices. Every contract slice cites this file. Re-check the highest number on `origin/feat/perf-fleet` at merge time and renumber on conflict (update this table in the same PR).

## ADR numbers (docs/adr/)

Highest on origin/main: 0005. Four planned ADRs each originally claimed 0006.

| No. | File | Slice |
|---|---|---|
| 0006 | helper-nodes-consume-manager-grants | P0.3 |
| 0007 | attempt-lease-contract | P0.3 |
| 0008 | rust-relay-adoption-gate | P0.3 |
| 0009 | helper-grant-shrink-drain-ramp-telemetry (landed, P4.21) | P4.21 |
| 0010 | helper-trust-plane (landed, P4.5) | P4.5 |
| 0011 | lease-then-place-with-delayed-retry (landed, P3.6) | P3.6 |
| 0012 | voice-excludes-browser-jobs-on-a-shared-browser (landed, P5.5) | P5.5 |
| 0013 | helper-service-lease-log-and-primary-identity (landed, P4.11) | P4.11 |
| 0014 | remote-attempt-lifecycle-on-the-primary (landed, P4.14) | P4.14 |
| 0015 | attempt-reconcile-ledger-first-never-resubmit (landed, P4.18) | P4.18 |
| 0016 | eligibility-aware-placement-after-the-lease (landed, P4.17; renumbered from 0015, which P4.18 took first) | P4.17 |
| 0017 | helper-voice-media-on-the-helpers-udp-range-placed-by-profile (landed, P5.9) | P5.9 |
| 0018 | primary-negotiates-helper-voice-with-stateless-fenced-control (landed, P5.11) | P5.11 |
| 0019+ | free: take next free | later |

## Migrations

origin/main postgres highest: 0021 (0019-0021 live: voice sessions, voice instance global, admission tokens). sqlite highest: 0013 (tracked separately; sqlite may fail closed for helper-plane features).

| Postgres | SQLite | Purpose (dependency order) |
|---|---|---|
| 0022_gateway_job_attempts (landed, P4.2) | n/a (sqlite fails closed with UBAG_EXECUTOR_ATTEMPTS on; 0014 stays unused) | attempts; the only attempts table, the node store references it |
| 0023_helper_nodes (landed, P4.4) | n/a (SQLite edge profile refuses helper mode; 0015 stays unused) | nodes + registry |
| 0024_helper_profiles (landed, P4.16) | n/a (no SQLite profile registry: the edge profile refuses helper mode; 0016 stays unused, the SQLite conversations store adds `node_id`/`profile_ref` itself in `Ready`) | profile_ref registry + conversation node affinity columns |
| 0025_voice_lease_generation (landed, P5.8) | n/a (the voice SQLite store adds the columns itself in `Ready`; no repo SQLite file) | voice lease generation |
| 0026+ | 0017+ | free; P1.6 tenant-keyed topology upsert reserves one if schema impact exists |

## Paths, routes, flags

- Helper proto: `packages/proto/proto/ubag/helper/v1`.
- Routes: `/v1/fleet/*` guarded by scope `fleet:read`; internal node routes under `internal/nodes`.
- Flag ladder: `UBAG_HELPER_NODES` < `UBAG_HELPER_PLANE` < `UBAG_HELPER_DISPATCH` < `UBAG_HELPER_VOICE` (each requires the previous).
- Attempt ledger flag: `UBAG_EXECUTOR_ATTEMPTS` (default off; on requires the postgres or memory store, sqlite refuses to start).
- Queue lease flag: `UBAG_EXECUTOR_LEASE_TTL_MS` (0/unset = legacy no-expiry; otherwise 30000..900000; file-spool lease expiry + NATS ack wait; see ADR-0007 addendum).
- Streaming flag pair: `UBAG_WORKER_STREAM_EVENTS` (Python emits) + `UBAG_WORKER_STREAM_INGEST` (Go ingests). Other flags: see BINDING.md section 3 rule 5.
- Primary-side dispatch (P4.14), behind `UBAG_HELPER_DISPATCH` (default off; needs `UBAG_HELPER_PLANE`, `UBAG_EXECUTOR_ATTEMPTS` and a ledger store): `UBAG_HELPER_CA_FILE` (shared with the plane: the manager CA bundle that also verifies helper certificates), `UBAG_HELPER_CLIENT_CERT_FILE` / `UBAG_HELPER_CLIENT_KEY_FILE` (the primary's dial certificate, URI SAN `spiffe://ubag/primary/<id>`; distinct from the plane listener's `UBAG_HELPER_TLS_*`), `UBAG_HELPER_WORKLOAD_VERSION` (the helper workload this primary dispatches to), `UBAG_ADAPTERS_DIR` (existing; its registry digest must equal the helper's). The dialer is `internal/helperclient`; the runner, picker interface and cancel registry are `executor/remoterunner.go`, `helperpicker.go`, `cancelregistry.go`. Placement (P4.17, ADR-0016) adds no flag of its own: with `UBAG_HELPER_DISPATCH` on, `serve` wires `executor.FleetPicker` (over `nodes.Placer` and the tenant `helperauth.ProfileStore`) instead of `executor.NoHelperPicker`, starts the capacity prober (`nodes.Prober`, every 15 s, same dial identity as dispatch), makes held leases wait off the worker (at most 64) and sizes the consumer pool from the helper capacity (local pool plus the granted workload limits, at most 512). The grant poller (P4.15) starts when `UBAG_HELPER_NODES` is on and `UBAG_FLEET_MANAGER_URL` is set (`UBAG_FLEET_POLL_SECONDS`, `UBAG_FLEET_GRANT_STALE_GRACE_SECONDS`); with the URL unset nothing is ever granted and every job runs locally. The attempt reconciler (P4.18, same flag, no new rung) adds one tuning knob, read only when dispatch is on: `UBAG_HELPER_RECONCILE_WINDOW_SECONDS` (default 600, 60..86400: how long a submitted attempt may stay unresolved with its helper unreachable, from the lapse of its lease); it lives in `nodes/reconciler.go` (policy), `executor/reconcilegate.go` and `executor/remoteresume.go` (effects), and with dispatch on the stale-job sweep no longer expires attempts (the reconciler owns the fence of a submitted one).
- Helper binary `apps/gateway/cmd/ubag-helper` (P4.11) is a separate process, outside the flag ladder, configured only by `UBAG_HELPER_LISTEN`, `UBAG_HELPER_NODE_ID`, `UBAG_HELPER_CA_FILE`, `UBAG_HELPER_TLS_CERT_FILE`, `UBAG_HELPER_TLS_KEY_FILE`, `UBAG_HELPER_PRIMARY_URI_SAN`, `UBAG_HELPER_PRIMARY_SPKI_SHA256`, `UBAG_HELPER_WORKLOAD_VERSION`, `UBAG_HELPER_ADAPTERS_DIR`, `UBAG_HELPER_CHROME_BIN`, `UBAG_HELPER_MAX_ATTEMPTS`, and (P4.12, the worker pool the binary now runs) `UBAG_HELPER_WORKER_SCRIPT` (required), `UBAG_HELPER_WORKER_PYTHON`, `UBAG_HELPER_PROFILE_ROOT` (the workers' `UBAG_PROFILE_DIR`). The three `*_CA_FILE`/`*_TLS_*_FILE` names are the same names the primary's helper-plane listener uses (P4.5) with the matching meaning on each side; they are never set in one process. The primary's identity on the helper side is the URI SAN `spiffe://ubag/primary/<id>` (ADR-0013). The staging client lives in `internal/helper/staging` (moved in P4.11). The pool the helper runs is `internal/workerdaemon` (extracted from the executor in P4.12; `executor.DaemonPool` is a thin adapter over it); the helper-side runner is `internal/helper/runner`. Helper-hosted voice (P5.10) adds, on the helper process only, `UBAG_HELPER_VOICE` (on the helper it is read alone: the ladder is the primary's), `UBAG_HELPER_VOICE_ENVS` (`<instance_ref>=<cdp_port>:<relay_port>,...`, always 127.0.0.1), `UBAG_HELPER_VOICE_KEY_DIR`, `UBAG_HELPER_VOICE_UDP_PORTS` (`<min>-<max>`), `UBAG_HELPER_VOICE_NAT_1TO1_IP` and `UBAG_HELPER_VOICE_SERVER_VIA_TURN`; the media endpoint is `internal/helper/voicehub` (voice.MediaHub on a loopback relay) and is linked only into a `-tags helpervoice` build. The audio relay (`deploy/vps/browser/audio-relay.py`) gains `UBAG_VOICE_RELAY_KEY_FILE` (per-attempt relay key file, re-read on every hello).
- Primary-side voice placement (P5.9, ADR-0017) adds no flag and no migration: with the whole ladder on (`voice.HelperVoiceEnabled`), `serve` builds `voiceplace.Placer` over the same `nodes.Placer` and tenant `helperauth.ProfileStore` that dispatch uses (`serve/voice_nodes.go`; the ladder without helper placement is a startup error) and releases the node slots of ended sessions every 5 s. The package is `internal/voiceplace` (not `internal/voice`, which the `helpervoice` helper build links and which must not import `nodes`); `voice.LeaseHolder` gains `NodeID` (read from the 0025 column). New metric `ubag_helper_voice_placements_total{outcome}`.
- Primary-side helper voice media (P5.11, ADR-0018) adds no flag and no migration: with the whole ladder on, `serve` builds `voice.RemoteNegotiator` (`serve/voice_nodes.go`: `newVoiceRemoteFromEnv`, which reuses the dispatch dial identity `UBAG_HELPER_CA_FILE` / `UBAG_HELPER_CLIENT_CERT_FILE` / `UBAG_HELPER_CLIENT_KEY_FILE`, the node store's endpoints, `UBAG_HELPER_WORKLOAD_VERSION` and the `UBAG_ADAPTERS_DIR` digest for the handshake checks, and `UBAG_VOICE_RELAY_SECRET` as the master the per-attempt keys derive from) and serves the media plane through `voice.NodeRouter`: a session with a node goes to the negotiator, every other one to the primary's hub. The negotiator is in `internal/voice/remote.go` (not a package of its own: it imports only the proto, gRPC and four seams, so the `helpervoice` helper build stays inside the P5.10 import guard); `helperclient.Conn` now carries `HelperVoiceServiceClient` on the same verified connection; the voice store gains `CommitMuted` (fenced like `CommitTransition`; no migration); `voice.AttemptIDFor` derives the attempt id from the session and generation; `voiceplace.Placer.ProfileRef` resolves the node-side identity. Constants, not env: lease window 60 s, renewal every 20 s, clock-skew bound 5 s, credentials end 4 h after the session was created. Metrics: lease kind `voice` on `ubag_lease_renew_failures_total`; stale or fenced node events count in `ubag_helper_fenced_rejects_total` (`stale_generation`, `attempt_mismatch`, `commit_fenced`).

## Phase map

P0 reconcile + measure (inert); P1 hardening/correctness; P2 streaming + SSE; P3 worker pool/attempts; P4 helper plane; P5 helper voice; P6 read-time queue_reason/contracts; P7 Rust gate + publication; P8 consolidation (P8.1 merges slice shards into PROGRESS.md).

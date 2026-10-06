# Perf + shared-fleet flag inventory

One row per environment variable the program added or gated. This file is machine-checked by `node tools/flag-graduation-check.mjs` (`pnpm check:flag-graduation`): every row's `Read in` file must mention the variable, its `Compose` column must match `docker-compose.vps.yml`, every `managed` row must have a section in `ROLLOUT.md`, and every row must appear in `deploy/vps/env.example`. Values are never recorded here; only names and documented non-secret defaults.

Column meanings.

- **Default**: behaviour when the variable is unset. `off` means inert (today's behaviour). `on` means the behaviour is live already and the variable is a kill-switch.
- **Compose**: how `docker-compose.vps.yml` passes it to the gateway container. `none` = not listed in `environment:`, so setting it in `env.local` has **no effect** until a reviewed compose line (empty default) is added. `empty` = passed with an empty default. Anything else is the literal default.
- **Graduation**: `managed` = has a per-flag checklist in `ROLLOUT.md`. `live` = already on in production, only the kill-switch is documented. `knob` = a tuning value that belongs to a managed flag and has no checklist of its own.
- Truthy values: always write the literal `true`. The gateway's `envBool` accepts only `1`, `true`, `yes`, and `UBAG_SSE_CLOSE_ON_TERMINAL` accepts only `true`.

| Flag | Side | Default | Compose | Graduation | Read in |
|---|---|---|---|---|---|
| `UBAG_WORKER_STRICT_SUBMIT` | gateway + worker | off | empty | managed | `apps/gateway/internal/executor/submission.go` |
| `UBAG_WORKER_STRICT_STREAM_END` | worker | off | none | managed | `apps/worker/ubag_worker/live/engine.py` |
| `UBAG_WORKER_STREAM_EVENTS` | worker | off | empty | managed | `apps/worker/ubag_worker/live/daemon_protocol.py` |
| `UBAG_WORKER_STREAM_INGEST` | gateway | off | empty | managed | `apps/gateway/internal/executor/streamrunner.go` |
| `UBAG_WORKER_ATTEMPT_EVENT_IDS` | gateway | off | none | managed | `apps/gateway/internal/executor/workerconsumer.go` |
| `UBAG_EVENT_NOTIFY` | gateway | off | none | managed | `apps/gateway/internal/serve/serve.go` |
| `UBAG_WORKER_POOL_SIZE` | gateway | 1 | 1 | managed | `apps/gateway/internal/serve/serve.go` |
| `UBAG_SSE_CLOSE_ON_TERMINAL` | gateway | off | none | managed | `apps/gateway/internal/httpapi/server.go` |
| `UBAG_FILESPOOL_HONOR_NOT_BEFORE` | gateway | off | none | managed | `apps/gateway/internal/serve/serve.go` |
| `UBAG_REDACT_REMOTE_ENDPOINT` | gateway | off | none | managed | `apps/gateway/internal/httpapi/topology_handlers.go` |
| `UBAG_PROFILE_OPTIONS_POLICY` | worker | legacy | empty | managed | `apps/worker/ubag_worker/live/envelope.py` |
| `UBAG_VOICE_RECONCILER_FAIL_CLOSED` | gateway | off | empty | managed | `apps/gateway/internal/serve/serve.go` |
| `UBAG_WARM_RESUME_FASTPATH` | worker | off | empty | managed | `apps/worker/ubag_worker/live/daemon.py` |
| `UBAG_EXECUTOR_ATTEMPTS` | gateway | off | none | managed | `apps/gateway/internal/serve/serve.go` |
| `UBAG_HELPER_NODES` | gateway | off | none | managed | `apps/gateway/internal/serve/helper_nodes.go` |
| `UBAG_HELPER_PLANE` | gateway | off | none | managed | `apps/gateway/internal/serve/helper_plane.go` |
| `UBAG_HELPER_DISPATCH` | gateway | off | none | managed | `apps/gateway/internal/serve/helper_dispatch.go` |
| `UBAG_HELPER_VOICE` | gateway + helper | off | none | managed | `apps/gateway/internal/helper/voice_config.go` |
| `UBAG_FLEET_MANAGER_URL` | gateway | unset | none | managed | `apps/gateway/internal/nodes/allocation_source.go` |
| `UBAG_ADMISSION_SHARED` | gateway | on | none | live | `apps/gateway/internal/serve/serve.go` |
| `UBAG_VOICE_LANE_EXCLUSION` | gateway | on | empty | live | `apps/gateway/internal/serve/voicelane.go` |
| `UBAG_WORKER_POOL_MAX` | gateway | 3 | empty | knob | `apps/gateway/internal/serve/serve.go` |
| `UBAG_WORKER_POOL_WAIT_MS` | gateway | 30000 | empty | knob | `apps/gateway/internal/serve/serve.go` |
| `UBAG_WORKER_STREAM_FLUSH_MS` | gateway | 50 | empty | knob | `apps/gateway/internal/executor/streamingest.go` |
| `UBAG_WORKER_STREAM_MAX_BYTES` | gateway | 8388608 | empty | knob | `apps/gateway/internal/executor/streamingest.go` |
| `UBAG_EVENT_FALLBACK_MS` | gateway | 2000 | none | knob | `apps/gateway/internal/serve/serve.go` |
| `UBAG_SSE_MAX_STREAMS` | gateway | 0 | none | knob | `apps/gateway/internal/httpapi/server.go` |
| `UBAG_HELPER_RECONCILE_WINDOW_SECONDS` | gateway | 600 | none | knob | `apps/gateway/internal/serve/helper_dispatch.go` |
| `UBAG_FLEET_POLL_SECONDS` | gateway | 30 | none | knob | `apps/gateway/internal/nodes/allocation_source.go` |
| `UBAG_WORKER_STAGE_TIMINGS` | worker | off | none | knob | `apps/gateway/internal/workerdaemon/env.go` |

Notes.

- `UBAG_WORKER_STAGE_TIMINGS` is a measurement aid (stage attribution for benchmark runs), not a behaviour change; leave it off outside benchmark runs.
- Not in this table on purpose: `UBAG_WORKER_DAEMON`, `UBAG_WORKER_CONCURRENCY`, `UBAG_EXECUTOR_MODE`, `UBAG_GATEWAY_STORE`, `UBAG_VOICE_STORE`, `UBAG_VOICE_RELAY_SECRET` (existing production configuration, not program flags; see `docs/perf-fleet/slices/P0.2.md` for the recorded parity).
- The helper agent's own variables (`UBAG_HELPER_LISTEN`, `UBAG_HELPER_TLS_CERT_FILE`, `UBAG_HELPER_VOICE_UDP_PORTS` and the like) are documented in `docs/perf-fleet/RUNBOOK.md` and `deploy/helper/`; they configure the helper process, not the primary.
- Names in the shards that look like flags but are not: `UBAG_QUEUE_REASONS` is a generated SDK manifest constant (queue reasons are computed at read time with no switch, D8); `UBAG_SPOOL_PRIORITY_LANES` was deliberately not built (P1.4); `UBAG_VOICE_HELPER_MEDIA` is a stale work-item name for `UBAG_HELPER_VOICE` (P5.8).

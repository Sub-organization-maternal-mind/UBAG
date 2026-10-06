# Per-slice progress shards

One file per roadmap slice (docs/perf-fleet/slices/<ID>.md): status, files, flags and defaults, migrations, checks run and not run, deviations, follow-ups. P8.1 consolidated them into the sections "Perf + shared-fleet program" of PROGRESS.md and AGENT_HANDOFF.md, the flag inventory in ../FLAGS.md and the rollout procedure in ../ROLLOUT.md. Read those first; open a shard for the detail.

Statuses below were taken from each shard's own Status line at the integration tip when P8.1 was written (not re-verified against a live system). "merged" means the slice's PR is in feat/perf-fleet, not that anything is deployed or that its flag is on.

| Slice | Title | Status |
|---|---|---|
| P0.1 | Read-only probe rewrite and parity shard | merged |
| P0.2 | Production parity record and admission rollout posture | merged |
| P0.3 | Budget drift, glossary, ADRs, allocation table | merged |
| P0.4 | Fail-closed legacy benchmark, e2e, monitor and chaos paths | code complete; the manual CI validation run was not performed |
| P0.5 | Acceptance harness integrity and goal thresholds | merged |
| P0.6 | Make the dashboard bundle-weight gate real | merged |
| P0.7 | Opt-in loopback pprof and Go runtime/process metrics | merged |
| P0.8 | Executor micro-benchmarks and worker slot-cost measurement | merged |
| P0.9a | Stage timings: schema + observability contract | merged |
| P0.9b | Stage timings: Python worker marks + mock adapter | merged |
| P0.9c | Stage timings: Go gateway stage metrics | merged |
| P0.10 | cgroup, throttle and host-pressure sampler in the harness | merged |
| P0.11 | Workload manifest and run provenance | merged |
| P0.12 | Event-delivery latency and idle DB load scenario | merged |
| P0.13 | Baseline matrix runner + memory-headroom budget | partial |
| P0.14 | Voice and admission carry-over safety | merged |
| P0.15 | Uploaded-audio and attachment path facts + workload manifest entry | merged |
| P0.16 | Real-Chrome helper-footprint spike harness | merged |
| P1.1 | Characterization tests: event waiting, streams, consumer terminal validation | merged |
| P1.2 | Per-job event wake hub replacing 50 ms SQL polling (local mode) | merged |
| P1.3 | gRPC stream, OpenAI facade and cancel-watch on the event hub | merged |
| P1.4 | File-spool correctness: not_before, lost-race lease, flat enqueue cost | merged |
| P1.5 | Live-worker concurrency guard | merged |
| P1.6 | Pre-helper tenant isolation fixes | merged |
| P1.7 | Dashboard polling hardening | merged |
| P1.8b | Attachment copies, warm-page reloads, idle polling | merged |
| P2.1 | Close dashboard, contract and fixture drift | merged |
| P2.2 | Worker daemon protocol v2 and attempt/provisional event semantics | merged |
| P2.3 | Single helper gRPC contract (ubag.helper.v1) | merged |
| P2.4 | Node-allocation consumer schema and error codes | merged |
| P2.5 | Authz actions and audit vocabulary | merged |
| P2.6 | Truthful queue depth: GET /v1/jobs/summary | merged |
| P2.7 | SSE and gRPC stream resume and close semantics contract | merged |
| P2.8 | Relay protocol v2 shared vectors | merged |
| P3.1 | Truncation guard: a deadline-cut stream is never completed | merged |
| P3.2 | No blind resubmit: prompt_submitted marker and post-submit terminal failure | merged |
| P3.3 | Slot-scoped page registry and physical-session identity lock | merged |
| P3.4 | Daemon protocol v2, Python side | merged |
| P3.5 | Worker env passthrough, chat-ledger sink in daemon, mock synthetic knobs | merged |
| P3.6 | Go DaemonPool: bounded isolated slots replacing the single mutex | merged |
| P3.7 | Local attempt identity and attempt-scoped event ids (carve-out) | merged |
| P3.8 | Python incremental engine streaming: pre-submit buffered, post-submit live | merged |
| P3.9 | StreamingWorkerRunner / EventSink with batch adapter | merged |
| P3.10 | Daemon streaming ingest: provisional until one valid terminal | merged |
| P3.11 | Batch event apply, one notify per batch, token coalescing, byte budget | merged |
| P3.12 | Submission boundary on the local path: no replay after submit | merged |
| P3.13 | SSE handler on the hub with resume, close-on-terminal and stream cap | merged |
| P4.1 | Pure eligibility, pressure hysteresis and ceiling logic | merged |
| P4.2 | Attempt ledger with lease generation and fenced commits | merged |
| P4.3 | Queue lease TTL, renewal, expiry reclaim and attempt-aware reaper | merged |
| P4.4 | Node, allocation and node-registry store | merged |
| P4.5 | Helper trust plane: separate mTLS listener, URI-SAN node identity, dev certs | merged |
| P4.6 | Attempt capability tokens and app-JWT audience guard | merged |
| P4.7 | HelperAttemptSpec projection and helper env allowlist | merged |
| P4.8 | Fleet audit wiring with fail-closed append | merged |
| P4.9 | Fenced idempotent commit and helper ingest validation | merged |
| P4.10 | Streamed, checksummed asset staging | merged |
| P4.11 | ubag-helper service core against a fake runner | merged |
| P4.12 | Helper bounded pool, staging and identity lock wired in | merged |
| P4.13 | Read-only provider readiness probe | merged |
| P4.14 | Primary remote runner, HelperPicker interface and in-process cancel registry | merged |
| P4.15 | AllocationSource: poll manager grants with last-known-good | merged (inert until `serve` starts the poller; see P4.17) |
| P4.16 | Profile and conversation affinity (tenant-owned profile_ref) | merged |
| P4.17 | Placer, prober and serve wiring | merged |
| P4.18 | Attempt reconciliation: helper lost, gateway restart, no blind resubmit | merged |
| P4.19 | Helper workload manifest, image and CI (inert) | merged |
| P4.20 | Live canary with one helper (EXTERNAL-BLOCKED) | partial (external-blocked: no canary has run) |
| P4.21 | Grant shrink / drain policy, 1-to-N ramp rule, host-pressure telemetry source | merged |
| P4.22 | Reconciler for queued-in-DB-but-not-in-spool jobs | merged |
| P4.23 | Helper-plane metrics, alert rules and runbook (before canary) | merged |
| P5.1 | Voice frame-age metric and in-process relay latency bench | merged |
| P5.2 | Voice link-quality metrics (jitter, loss, mic queue depth) | merged |
| P5.3 | Age-bounded mic and speaker queues | merged |
| P5.4 | Human-supervised voice activation probe and live evidence | partial |
| P5.5 | Voice lease excludes text and file jobs on the same browser | merged |
| P5.6 | Voice context index in control jobs and per-environment relay isolation | merged |
| P5.7 | Helper voice RPCs and per-attempt derived credentials | merged |
| P5.8 | Voice lease generation, node id and terminating hold | merged |
| P5.9 | Node-aware voice placement and node-lane admission | merged |
| P5.10 | Helper-side voice endpoint: MediaHub, loopback relay, browser and audio co-located | merged |
| P5.11 | Primary RemoteMediaNegotiator and control forwarding | merged (Postgres parity and live two-way audio not run) |
| P6.1 | queue_reason and operator fleet read API: contract and SDK surface | merged |
| P6.2 | Fleet read handlers and queue_reason population | merged |
| P6.3 | Dashboard truthful queue depth and shared snapshot store | merged |
| P6.4 | Dashboard queue reasons, placement and assigned-capacity panels | merged |
| P6.6 | Conversation-hot warm resume fast path | partial (built, merged OFF, not live-verified) |
| P6.7 | Operator docs and ledger | merged |
| P7.1 | synthetic_chat browser fixture (warm-daemon path) | merged |
| P7.2 | 1/2/5/10/20 workload ladder, paused 100-client / 1000-queued mode, capacity report | partial |
| P7.3 | Helper isolation acceptance suite | partial |
| P7.4 | Relay measurement hooks, real-libopus goldens and black-box seams | partial |
| P7.5 | Relay baseline profile and pre-registered stop-rule check | partial |
| P7.6 | Python relay baseline fixes before any Rust verdict | partial |
| P7.7 | Rust-gate A/B harness, evaluator and verdict (no Rust written) | partial |
| P7.8 | CONDITIONAL: Rust relay crate, env-selectable and rollbackable (NOT STARTED) | blocked (not started; Rust gate unevaluated) |
| P7.9 | Publish measured capacity and close the ledger | partial |
| P8.1 | Flag graduation and rollout runbook | merged when its PR is merged |
| P1.8 | Create-path pprof + DB pool sizing | no shard at this tip (not landed) |
| P6.5 | Dashboard voice sessions and capabilities panel | merged |

# UBAG perf + shared-fleet program — BINDING CONTEXT (read first, every slice)

Source: user-approved plan ("UBAG performance optimization and shared VPS fleet plan", 2026-10-05) + a 15-agent
read-only gap analysis + an adversarial critic. This file is the single authority for decisions and rules.
Files next to this one: `slices.json` (slice specs + dependencies), `workitems.json` (file-level work items by id),
`roadmap-notes.json` (risks, deferred items, critic verdict), `reader-summaries.json` (code map + plan-claim checks).

## 1. Ground truth (verified, do not re-investigate)

- Integration branch is `feat/perf-fleet` (off origin/main e0667b5). Treat what main already ships (voice, shared admission, execution
  leases, migrations 0019-0021, the acceptance harness) as LIVE in production.
- This repo is public: the deployed commit, secret posture, container and host sizing and co-tenancy are NOT recorded here. They live in a
  private ops note held by the owner. Slices use only the documented defaults in `docker-compose.vps.yml` and `deploy/vps/env.example`
  (env var names and non-secret defaults): UBAG_WORKER_DAEMON, UBAG_WORKER_CONCURRENCY, UBAG_EXECUTOR_MODE, UBAG_GATEWAY_STORE,
  UBAG_VOICE_STORE (default memory), UBAG_VOICE_RELAY_SECRET (unset by default, so live media is unavailable), GOMAXPROCS, GOMEMLIMIT,
  UBAG_WORKER_POLL_INTERVAL_MS. Any other production fact is a `follow_ups` item for the owner, not something to commit.
- The plan's "starting point" table is largely TRUE for config; but many plan CAPABILITIES do not exist (no lease generation / fencing,
  exec lease is 90 s renewed every 10 s, no node registry, no helper proto, no incremental events, blind 3x resubmit exists,
  deadline-cut streams are reported completed). See `reader-summaries.json` plan_claim_checks.
- Capacity is identity-bound (UNIQUE tenant/target/identity_ref; safe-mode forbids copying credentials), not CPU-bound.
- The OET fleet manager is OUTSIDE this repo and has no project-facing allocation API. Build UBAG's side against a fake/consumer schema.

## 2. Decisions (user delegated all decisions; recommended options adopted)

- D1 `main` is fast-forwarded (done). All work lands on branches/PRs; NOTHING is pushed to main; nothing is deployed.
- D2 Admission/exec-lease rollout: accept as live; add a kill-switch whose DEFAULT PRESERVES current live behaviour.
- D3 Helper plane: primary dials helper over WireGuard (mTLS gRPC server on helper); primary polls manager grants against a UBAG-owned
  schema (last-known-good, but when the manager is unreachable NO new grants/increases and no new placements beyond already-granted limits);
  manager CA issues certs <=72h, UBAG pins SPKI, URI-SAN node identity; provider logins are out-of-band human logins (no viewer broker).
  The UBAG-side ceiling table (1.5 CPU/2.5 GiB, 3/5, 75%/62.5%) is defence-in-depth ONLY: effective cap = min(manager grant, table).
- D4 Deadline-cut stream => job ends `timed_out` with `data.partial`, never `completed`, no partial text in `result`. Post-submit ambiguity =>
  `failed` with `submitted=true` and `reconcile_required`. Flags UBAG_WORKER_STRICT_STREAM_END and UBAG_WORKER_STRICT_SUBMIT default OFF.
  The worker engine.py change IS in scope.
- D5 No isolated lab host exists. Harness/tooling is built; numbers measured on this laptop/Docker are labelled NON-AUTHORITATIVE.
  Gates: steady-state accept p95<=200 ms, read p95<=100 ms; 100-client burst bound separate. Never aim load at the shared VPS.
- D6 One active operation per (provider, physical session) identity; scale by identities/helpers. Account inventory is unknown -> the capacity
  report states the ceiling honestly and marks it unverified.
- D7 Helper voice media terminates ON the helper (browser, audio env, relay and WebRTC endpoint co-located); primary keeps public signaling.
- D8 queue_reason is computed at read time (memoized per request), no migration; apps/mobile types.ts is part of contract propagation.
- D9 P2.7 / P3.13 (SSE resume/close semantics) are BUILT, additive, flag-gated, default off.
- D10 Legacy k6/Locust scripts that falsely report success are fixed or deleted in favour of the acceptance harness; LISTEN/NOTIFY stays deferred.

## 3. Hard rules for every slice

1. Public GitHub repo: NEVER commit secrets, env values, tokens, raw prod probe output, `.env*`, private keys, personal data. Env var NAMES and
   documented non-secret defaults only.
2. NEVER SSH to production, never touch the VPS, never dispatch/deploy workflows, never push to `main`, never target a PR at `main`.
   If you need another production fact, list it under `follow_ups` and continue with the conservative assumption.
3. Safe-mode is a hard product constraint: no automated login, credential scraping/storage, CAPTCHA solving; readiness probes are read-only
   (no typing/submitting) and must not run while an operator login/viewer session is active.
4. Contracts first: change packages/openapi, shared-schemas, proto BEFORE implementations; keep changes additive (public REST/SDK/auth/response
   contracts must not break). Regenerate manifests with the repo tools; never hand-merge generated manifests/SDK fingerprints.
5. Risky runtime behaviour ships behind env flags that are inert by default (or live-preserving where D2 says so). Name flags with the taxonomy:
   UBAG_WORKER_STREAM_EVENTS (Python), UBAG_WORKER_STREAM_INGEST (Go), UBAG_WORKER_STRICT_STREAM_END, UBAG_WORKER_STRICT_SUBMIT,
   UBAG_WORKER_POOL_SIZE, UBAG_WORKER_ATTEMPT_EVENT_IDS, UBAG_EVENT_NOTIFY (hub), UBAG_HELPER_NODES, UBAG_HELPER_PLANE, UBAG_HELPER_DISPATCH,
   UBAG_HELPER_VOICE, UBAG_FILESPOOL_HONOR_NOT_BEFORE, UBAG_REDACT_REMOTE_ENDPOINT, UBAG_VOICE_RECONCILER_FAIL_CLOSED. Reuse an existing flag
   rather than inventing a synonym.
6. Migrations: strictly additive and idempotent; they auto-apply to production on deploy. Pick the next free number from the allocation table
   created by P0.3 (docs/perf-fleet/ALLOCATION.md) and the actual highest number on `origin/feat/perf-fleet` at merge time; renumber on conflict.
   Postgres is the default store; memory and sqlite stores must stay behaviour-compatible (sqlite may fail closed for helper-plane features).
7. Fail closed under uncertainty; bound every queue/buffer/body; tenant-scope every lookup; no `err.Error()` leaks in responses.
8. Follow repo conventions (Go dependency-light, existing error codes, existing test style). Match surrounding code. Do not add dependencies
   unless the slice says so (proto/gRPC generation toolchains are pre-agreed for the helper contract).
9. TARGETED CHECKS ONLY (CLAUDE.md): a single Go package `go vet`/`go test -run`, a single pytest file, one `node tools/<check>.mjs`,
   `bash -n`, `node --check`. Do NOT run `pnpm check`, `pnpm test:v0*`, full suites, docker builds, full dashboard builds or load tests longer
   than a short smoke. Postgres/Docker/protoc-dependent tests: write them env-gated and skipped by default; do not run them. State exactly which
   checks you ran and which you could not.
10. Progress notes: DO NOT edit PROGRESS.md or AGENT_HANDOFF.md (merge-conflict magnets). Write `docs/perf-fleet/slices/<SLICE_ID>.md`
    (status, files, flags + defaults, migrations, checks run / not run, evidence, deviations, follow-ups). P8.1 consolidates them.
11. Windows dev box: Go/Python/Node via PATH or the portable toolchain (`tools/run-go-tests.mjs`); POSIX-only tests must self-skip on Windows.
12. Commits: `git commit -s` (DCO) and end the message with
    `Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>`. Small, reviewable commits; contract commit before implementation commit.
13. PR bodies end with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.

## 4. Critic-binding amendments (apply those naming your slice id)

- P0.1: only the probe-script rewrite + parity shard remain (FF and the probe were done by the orchestrator).
- P0.2: record production parity from section 1 (sanitized) in docs/perf-fleet/slices/P0.2.md; implement the D2 kill-switch (live-preserving default)
  only if shared admission has no switch today.
- P0.8: pair with P0.16 (real-Chrome footprint harness). Executor micro-benchmarks stay Go benchmarks.
- P0.9 is split into P0.9a (contract), P0.9b (Python marks), P0.9c (Go metrics); do only your third.
- P0.13: local, short, clearly labelled NON-AUTHORITATIVE baselines; the 60-minute steady-state and 20-workload runs need the lab host (D5).
- P0.14: voice posture = routes live by default, memory store, relay secret unset so media unavailable. Do NOT change the compose voice-store default.
  Reconciler fail-closed change is behind UBAG_VOICE_RECONCILER_FAIL_CLOSED (default off). Document the containment switch (UBAG_VOICE_STORE=disabled)
  in the runbook without applying it anywhere.
- P1.4: not_before honouring behind UBAG_FILESPOOL_HONOR_NOT_BEFORE (default off); lost-race lease fix is a separate commit.
- P1.6: three separate commits: (a) tenant-keyed topology upsert (check schema impact; reserve migration number), (b) remote_endpoint redaction,
  contract-first, flagged UBAG_REDACT_REMOTE_ENDPOINT (check apps/dashboard types.ts and apps/mobile types.ts consumers and SDKs first),
  (c) tab context_id + profile-policy flag (default legacy).
- P1.8 / P1.8b: see slices.json. If a goal is unreachable on the current sizing, document evidence + resize recommendation; do not resize prod.
- P3.6: couple UBAG_WORKER_POOL_SIZE with the consumer PoolSize (UBAG_WORKER_CONCURRENCY) so the pool really parallelises; default size 1 == today;
  saturation after a lease is a delayed Retry, never an immediate re-queue or post-ack overload; cap by measured primary cgroup headroom;
  land the lease-then-place note/ADR in the same slice.
- P3.7: attempt-scoped event ids behind UBAG_WORKER_ATTEMPT_EVENT_IDS (default off); specify mixed-id behaviour for retries spanning a deploy;
  verify envelope.py tolerates the attempt block first (depend on P3.4 semantics, adapt as needed).
- P4.1: ceiling table is defence-in-depth (D3). P4.13: rate-limited, read-only, never while an operator login/viewer is active.
- P4.15: manager unreachable => no grant increases / no new capacity (D3). P4.18: bounded reconcile window + terminal `failed` with
  `reconcile_required` when the helper stays unreachable; P4.22 covers queued-in-DB-not-in-spool.
- P5.7: depends on D7 (media on helper), not on live evidence from P5.4. P5.4 is a human-supervised probe: build the tooling and runbook only.
- P6.1: D8. P7.5: the stop rule is roadmap-proposed; ALSO evaluate the plan's gate (>=20% UBAG-controlled overhead or >=25% CPU/mem, material in
  the mixed workload). P7.2/P7.9: publish numbers with which flags were on; label local numbers non-authoritative.
- P7.8 (Rust relay crate) is conditional on a "go" verdict from P7.7; without lab measurements the honest outcome is "not started; gate unevaluated".
- P4.20 (live canary) is EXTERNAL-BLOCKED: produce the runbook/checklist/fake-manager harness, do not claim a canary ran.

## 5. How to land a slice (git workflow)

1. `git fetch origin` then `git checkout -B feat/pf-<slice-id-lowercase-dashes> origin/feat/perf-fleet` (e.g. feat/pf-p0-4). Your cwd is an isolated worktree.
2. Implement; run targeted checks; commit (see rule 12).
3. `git push -u origin HEAD`, then `gh pr create --base feat/perf-fleet --head <branch>` (never base main).
4. Merge: `gh pr merge <number> --merge`. If it is not mergeable: `git fetch origin && git rebase origin/feat/perf-fleet`, resolve conflicts
   keeping BOTH sides' intent, re-run your targeted checks, `git push --force-with-lease`, retry (max 5 rounds).
   A failed targeted check after rebase is a failure to report, not to hide.
5. If blocked (premise false, external dependency, would violate a rule): do NOT force it. Land whatever is safely landable (docs/tests/contract),
   mark status `blocked` or `partial`, and explain.

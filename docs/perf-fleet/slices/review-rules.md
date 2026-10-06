# Review (rules lens) fixes

Status: all seven findings re-verified against the code; all were real. Branch `feat/pf-review-rules`, PR into `feat/perf-fleet`.

## Fixed

1. **SSE contract (P3.13 / P2.7), medium.** `apps/gateway/internal/httpapi/sse.go` now reads `after_sequence` (400 `UBAG-VALIDATION-EVENT-SEQUENCE-001` on a negative or malformed value), takes the larger of it and the `Last-Event-ID` cursor, and answers 204 for a cursor at or past the terminal event with `UBAG_SSE_CLOSE_ON_TERMINAL` off as well as on (it used to be gated on the flag and on the header). The route's OpenAPI now documents the 503 + `Retry-After` stream-cap response and drops the stale "until the gateway implements resume" paragraph; `docs/.../job-lifecycle.md` and `reference/api.md` agree. The stream-cap code was renamed from the unregistered `UBAG-SSE-STREAM-CAP-001` to `UBAG-OVERLOAD-SSE-STREAMS-001` (it was never emitted by default: the cap is off) and registered in `errors.json`; SDK manifests regenerated with `tools/make-sdks/generate-manifest.mjs`. New Go test `TestSSEResumeConformanceFixture` replays `packages/conformance/fixtures/streaming/sse-resume.json`; the old "stays open with the flag off" test now asserts 204.
2. **JobEvent schema violations (P3.7 / P4.9 / P4.12 / P3.8), medium.** (a) The local path now stamps `att_` + 32 hex of sha256(lease id) (`attemptIDForLease`, workerconsumer.go) instead of the raw queue lease id. (b) Helper ingest reduces `data.partial` to the schema's closed `{text, token_events}` (`schemaPartial`; `truncated` and `token_count` are dropped; payloadpolicy already exempts `token_events`). (c) Helper ingest maps `stream_end_reason` onto the schema enum (`schemaStreamEndReason`: known values pass; otherwise cancelled/deadline by outcome status, else `error`) and keeps the helper's own token as `data.stream_end_detail`. Unit tests added; existing tests updated to the derived ids and the new shapes.
3. **UBAG_VOICE_LANE_EXCLUSION default-on (P5.5), high.** It was a new default-on scheduling change presented as already live. It is now opt-in (`envBool`, default off); compose files, env examples, ADR-0012, the live-voice guide, FLAGS.md (now `off`, `managed`), ROLLOUT.md (new per-flag checklist, removed from the "already live" section) and PROGRESS.md say so. Test rewritten as `TestVoiceLaneExclusionIsOptIn`.
4. **D2 kill-switch not operable on vps compose (P0.2 / P8.1), medium.** `docker-compose.vps.yml` now passes `UBAG_ADMISSION_SHARED: ${UBAG_ADMISSION_SHARED:-true}` (default preserves live behaviour); FLAGS.md Compose column is `true`; ROLLOUT.md section 5 states it is delivered.
5. **FLAGS.md inventory incomplete (P8.1), medium.** The header now says the machine-checked table is the program flags only, and a second "Other runtime variables" table lists the 22 behaviour-changing variables the review named, with defaults and compose delivery. `deploy/vps/env.example` has a top-of-file compose-delivery warning and marks the three opt-ins that compose does not pass. `tools/flag-graduation-check.mjs` also scans `deploy/**/*.py` for names so the relay variables resolve.
6. **Stale ledger (P5.11 / P6.5 / P8.1), medium.** PROGRESS.md and AGENT_HANDOFF.md now say P5.11 and P6.5 merged (only P1.8 is unlanded); ALLOCATION.md credits ADR-0009 to P4.21. (The P3.6 shard repeat the review mentions does not exist in the shard text.)
7. **dashboard `client.ts` headers (P1.7), medium.** `response.headers?.get('Retry-After')`.
8. **gofmt (P1.6 / P6.2), medium.** `gofmt -w apps/gateway/internal/httpapi/topology_handlers.go`.

## Skipped or only partly done

- "Have flag-graduation-check fail on any code UBAG_ read absent from FLAGS.md": not done. The code reads hundreds of pre-existing UBAG_ names, so a ratchet needs a baseline list first. The second table is hand-maintained and says so.
- "Add a test that validates real worker-event and helper-ingest output against job-event.schema.json": not done; the Go unit tests assert the normalizers' output shapes against the schema's rules, but no Ajv harness runs on emitted events. Follow-up.
- "A bound or an alarm for how long a held job may be deferred" (voice lane): documented as a pre-condition in the ROLLOUT checklist (alarm on held-job age before C2); no code bound was added because the flag is now off.
- Python worker `events.py` still copies the envelope attempt id verbatim; with the derived id it now conforms, and no Python change was needed.

## Checks run

`gofmt -l`, `go vet ./...` (gateway), `go test ./internal/executor/` (full package), `go test ./internal/serve/ ./internal/helper/...`, `go test ./internal/httpapi/ -run SSE`, `go vet` in `packages/sdk-go`, `node tools/flag-graduation-check.mjs`, `node tools/check-contracts.mjs`, `node tools/make-sdks/generate-manifest.mjs`.

## Checks not run

- Dashboard vitest (no `node_modules` in the worktree), Postgres tests, `pnpm check`.
- `go test ./internal/httpapi/` complete package: `TestAntigravityLoginRelaysCodeToOwnedWorker` fails in this environment (503 isolated worker unavailable); it is unrelated to these changes (not touched, not SSE).

# Worker, clients, CLI, dashboard, daemon, and docs attachment report

## Outcome

Completed the final non-gateway multi-file attachment scope:

- Worker manifest safety, MIME/kind fail-closed behavior, ordered attachment telemetry, and warm-session file-state clearing.
- TypeScript and Go helper aliases, typed limits, multipart body typing, and focused helper tests.
- Repeatable CLI `--attach <path>[:kind]` parsing with Windows-drive safety, MIME inference, `.webm` video override, and duplicate/unknown rejection.
- Hallmark/NAJM multi-file dashboard picker, remove/clear behavior, pre-network limits, multipart submission, required states, and horizontal-overflow protection.
- Explicit false-by-default VPS worker-daemon wiring plus OpenAPI, cookbook, CLI, VPS, coverage, progress, and handoff documentation.

## Focused verification

- `python -m pytest apps/worker/tests/test_attachments.py apps/worker/tests/test_warm_daemon.py -q` — 18 passed.
- `cmd /c pnpm --filter @ubag/sdk build` — passed.
- `node --test packages/sdk-typescript/test/attachments.test.mjs` — 3 passed.
- `go test ./... -run "Test(CreateJobWithAttachments|CreateJobMultipartPreservesOrderMetadataAndHeaders|AttachmentLimits)$" -count=1` from `packages/sdk-go` — passed.
- `cmd /c pnpm --filter @ubag/cli build` — passed.
- `node --test --test-name-pattern "keeps repeated attach" packages/cli/test/cli.test.mjs` — 1 passed.
- `cmd /c pnpm --filter @ubag/dashboard exec vitest run src/lib/attachments.test.ts src/lib/api/client.test.ts --pool=forks --minWorkers=1 --maxWorkers=1` — 17 passed.
- `cmd /c pnpm --filter @ubag/dashboard check` — 0 errors and 0 warnings.
- `cmd /c pnpm --filter @ubag/dashboard build` — passed in the targeted implementation pass.
- `gofmt -w packages/sdk-go/attachments.go packages/sdk-go/attachments_test.go` — completed.
- `git diff --check` — passed.

No broad suite or CI flow ran, per project instruction.

## Review follow-up

- Fixed the real picker state ordering so `loading` remains visible while the native input is disabled in flight; the focused attachment state test passed (**3/3**).
- Strengthened warm reuse with two real `LiveSessionEngine` attachment manifests through one reused `MockPageDriver`; pre-attach state was empty for both jobs and the recorded batches were only `[first.pdf]` then `[second.wav]` (**9/9 warm-daemon tests passed**).
- Prestarted the already-built dashboard preview and ran only the jobs-page Chromium grep; it passed at **320, 375, 414, and 768 px** with picker and body overflow assertions (**1/1**).
- Fresh `svelte-check` remained **0 errors / 0 warnings**, and `git diff --check` passed.

No broad suite or CI flow ran.

## Commit

`497472c457e5592238d73614b288af6a3979db86` (`feat(attachments): finish clients worker and dashboard`)

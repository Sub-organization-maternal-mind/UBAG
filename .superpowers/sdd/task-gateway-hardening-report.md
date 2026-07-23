# Gateway attachment hardening report

Date: 2026-07-23  
Branch/base: `feat/multi-file-attachments` / `28c4b5b`

## Requirements addressed

- Legacy audio alias create/upload compatibility with accepted-audio MIME gating.
- Typed shape/key/kind/duplicate/content-type/count/filename validation and aligned schema/error catalog.
- Held PUT/multipart declared-key, actual MIME, adapter policy, gateway/per-file/total cap enforcement.
- Multipart duplicate rejection, metadata preflight, streaming SHA-256, byte-sensitive sorted-tuple idempotency, cleanup/rollback.
- Six live-provider catalog `file_attach` policy; generic/mock remain unsupported.
- Planned labeled attachment, awaiting, timeout, materialization, multipart, and rollback metrics.
- Batch attachment entries use the held `StatusCreated` dispatch gate.
- Review blockers: immutable declared bytes after dispatch with exact PUT replay, true SQLite conditional-update CAS, surfaced list-finalize failure, safe declared filename/MIME suffixes, and idempotent outbox crash-window recovery.

## Files changed

`apps/gateway/internal/attachments/{attachments.go,attachments_test.go}`; `apps/gateway/internal/executor/{attachment_metrics.go,attachments_test.go,workerconsumer.go}`; `apps/gateway/internal/httpapi/{attachments_gate.go,attachments_gate_test.go,server.go,server_test.go}`; `apps/gateway/internal/jobs/{sqlite.go,sqlite_cas_test.go}`; `packages/shared-schemas/{errors.json,schemas/job-request.schema.json}`; `PROGRESS.md`; `AGENT_HANDOFF.md`. `.serena/` was not touched.

## Red evidence

- Parser typed validation: `go test ./internal/attachments -run 'TestDeclaredAttachmentsTypedValidationErrors' -count=1` — 0 passed, 12 failed (untyped/accepted invalid fields).
- Core HTTP: `go test ./internal/httpapi -run 'Test(LegacyAudioAliasCreateAndUploadMIMEGate|HeldAttachmentPUTFailsClosed|MultipartRejectsMIMEAndDuplicateParts|MultipartPreflightsBeforeStreamingBinaryParts|MultipartIdempotencyIncludesAttachmentBytes|MultipartEnforcesPolicyTotalAndPerFileCaps|BatchAttachmentEntryUsesHeldDispatchGate|AdapterCatalogExposesAttachmentPolicyForLiveProvidersOnly)$' -count=1` — 0 passed, 13 failed.
- Metrics: `go test ./internal/httpapi -run '^TestAttachmentMetricsExposePlannedDimensions$' -count=1` — 0 passed, 1 failed.
- Materialization metric: `go test ./internal/executor -run '^TestMaterializeAttachmentsCountsArtifactReadFailures$' -count=1` — build failed because snapshot did not exist.
- Review follow-ups: `go test ./internal/httpapi -run 'Test(DeclaredAttachmentBytesBecomeImmutableAfterDispatch|AttachmentFinalizeReportsListFailure|RecoverQueuedAttachmentOutbox)$' -count=1` — recovery passed; 2 failed (201 instead of 409/500).
- Filename/extensions: `go test ./internal/executor -run 'Test(MaterializeAttachmentsPreservesSafeDeclaredFilename|AttachmentMIMEExtensionFallbacks)$' -count=1` — 0 passed, 2 failed.
- SQLite SQL predicate: `go test ./internal/jobs -run '^TestSQLiteTransitionStatusUsesConditionalUpdate$' -count=1` — 0 passed, 1 failed.
- Replay self-review: `go test ./internal/httpapi -run '^TestDeclaredAttachmentBytesBecomeImmutableAfterDispatch$' -count=1` — exact replay returned 409 instead of 201.

## Green evidence

All from `apps/gateway`:

- `go test ./internal/attachments -run 'TestDeclaredAttachments|TestValid' -count=1` — 19 passed.
- `go test ./internal/executor -run 'Test(MaterializeAudioArtifact|MaterializeAttachments|AttachmentMIMEExtensionFallbacks)' -count=1` — 8 passed.
- `go test ./internal/jobs -run 'Test(SQLiteTransitionStatus|MemoryStoreTransitionStatus)' -count=1` — 2 passed.
- `go test ./internal/httpapi -run 'Test(Attachment|Attachments|Multipart|BatchAttachment|AdapterCatalog)' -count=1` — 17 passed.
- `go vet ./internal/attachments ./internal/executor ./internal/httpapi ./internal/jobs` — no issues.
- `git diff --check` — clean.
- Both changed JSON files parsed with PowerShell `ConvertFrom-Json` — `JSON_PARSE_OK`.

No broad suite was run.

## Commit

Implementation commit SHA: `a2a1ce80e84b57ed2b376ec72b7a229dd3e21be5`.

## Self-review and concerns

`DeclaredAttachments` remains the single declared-key source; bytes remain artifact-store-only; created-state queue/terminal moves remain CAS-based; multipart hashes are sorted and byte-sensitive; exact PUT replay works while new overwrite/delete fails.

Concerns: mutation serialization is process-local, so multi-gateway deployment should add a store-level immutable write primitive. Crash recovery is outbox-only; direct enqueue is not replayed because it is not guaranteed idempotent. Validation was intentionally focused.

## Review-fix follow-up

Addressed all four confirmed review items with TDD:

- Extracted `prepareCreateJob` and made normal JSON and multipart create use it. Multipart resolves API version, applies templates once, checks `job:create`, kill switch, callbacks, payload/model/attachment policy, and both plugin hooks before file staging; the prepared request is passed to `createJob` so preparation is not repeated.
- Runtime manifest parsing now rejects unknown entry properties, keys over 512 Unicode code points, and content types over 128 Unicode code points with typed codes.
- Chunked multipart starts with an envelope-plus-8-KiB framing bound, then switches the remaining reader to the adapter policy total plus another explicit 8 KiB framing allowance.
- Multipart stored-success counters are emitted only after every artifact write succeeds; rollback leaves stored counts at zero and increments rollback.

Red:

- `go test ./internal/attachments -run '^TestDeclaredAttachmentsTypedValidationErrors$' -count=1` — 11 passed, 4 failed; the three new cases were accepted.
- `go test ./internal/httpapi -run 'Test(MultipartSharedPreflightBeforeBinaryStaging|ChunkedMultipartUsesPolicyDerivedStreamCap|MultipartStoredMetricWaitsForFullCommit)$' -count=1` — 0 passed, 5 failed: authorization returned body-read instead of 403, template defaults were not applied, chunked parsing consumed 262,814 bytes, and rollback reported stored success.

Green:

- The same parser command — 15 passed.
- The same review HTTP command — 5 passed.
- `go test ./internal/attachments -run 'TestDeclaredAttachments|TestValid' -count=1` — 22 passed.
- Focused create/attachment/template/plugin HTTP regression command — 36 passed.
- `go vet ./internal/attachments ./internal/httpapi` — no issues.
- `git diff --check` — clean.

Review-fix commit SHA: reported in the completion handoff.

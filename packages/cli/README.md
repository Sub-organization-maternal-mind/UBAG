# @ubag/cli

Minimal TypeScript/Node developer CLI for the UBAG gateway and local mock worker.

## Build

```powershell
cmd /c pnpm --filter @ubag/cli build
```

Run the built CLI:

```powershell
cmd /c pnpm --filter @ubag/cli cli -- --help
```

## Defaults

- Gateway URL: `UBAG_BASE_URL`, then `UBAG_GATEWAY_URL`, then `http://localhost:8080`
- API version: `UBAG_API_VERSION`, then the SDK default
- App secret: `UBAG_APP_SECRET`, then `dev-secret`
- Use `--no-auth` to omit the bearer token.

## Commands

```powershell
cmd /c pnpm --filter @ubag/cli cli -- health
cmd /c pnpm --filter @ubag/cli cli -- diagnose
cmd /c pnpm --filter @ubag/cli cli -- create-job --target mock_target --command-type echo --prompt "Hello UBAG"
cmd /c pnpm --filter @ubag/cli cli -- get-job job_123
cmd /c pnpm --filter @ubag/cli cli -- list-jobs --limit 10
cmd /c pnpm --filter @ubag/cli cli -- list-job-events job_123 --limit 10
cmd /c pnpm --filter @ubag/cli cli -- list-events --limit 10
cmd /c pnpm --filter @ubag/cli cli -- list-targets
cmd /c pnpm --filter @ubag/cli cli -- list-adapters
cmd /c pnpm --filter @ubag/cli cli -- list-webhooks
cmd /c pnpm --filter @ubag/cli cli -- list-artifacts job_123
cmd /c pnpm --filter @ubag/cli cli -- delete-artifact job_123 report.txt --idempotency-key idem_delete_123
cmd /c pnpm --filter @ubag/cli cli -- replay-webhook --delivery-id whdel_123
cmd /c pnpm --filter @ubag/cli cli -- cancel-job job_123 --reason "operator requested"
cmd /c pnpm --filter @ubag/cli cli -- retry-job job_123
cmd /c pnpm --filter @ubag/cli cli -- stream-sse job_123
cmd /c pnpm --filter @ubag/cli cli -- adapter-test --target mock
cmd /c pnpm --filter @ubag/cli cli -- mock-run --prompt "Hello local worker"
```

`stream-sse` is intentionally bounded and safe for the current v0 gateway. It
connects to `/v1/sse/jobs/{job_id}`, reads a limited number of events, and
aborts after a timeout instead of holding an unbounded stream open.

`mock-run` invokes `apps/worker/run_mock_worker.py` from the repo root. Override
the Python executable with `UBAG_PYTHON` or `--python`.

## Create Job Payloads

Use convenience options:

```powershell
cmd /c pnpm --filter @ubag/cli cli -- create-job --target mock_target --command-type echo --input-json "{\"prompt\":\"Hello\"}"
```

Or pass a full create-job envelope:

```powershell
cmd /c pnpm --filter @ubag/cli cli -- create-job --file job.json
cmd /c pnpm --filter @ubag/cli cli -- create-job --payload "{\"client\":{\"app_id\":\"demo\"},\"job\":{\"target\":\"mock_target\",\"command_type\":\"echo\",\"input\":{\"prompt\":\"Hello\"}}}"
```

## Mock Worker

Run the existing Python worker with a generated default payload:

```powershell
cmd /c pnpm --filter @ubag/cli cli -- mock-run
```

Pass through worker input and output files:

```powershell
cmd /c pnpm --filter @ubag/cli cli -- mock-run --input job.json --output events.jsonl
```

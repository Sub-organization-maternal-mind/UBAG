# @ubag/edge-store

SQLite-oriented edge data contracts for UBAG v0.

## Queue Semantics

The `Queue` interface in `src/queue.ts` is the runtime boundary. Feature code enqueues named JSON jobs and runtime adapters implement the storage and worker behavior.

- `enqueue` creates a queued job with JSON payload, payload version, priority, delay/run time, retry limit, optional dedupe key, and optional idempotency key.
- Active dedupe keys collapse duplicate queued, leased, or retry-scheduled work and return the existing job.
- Idempotency keys are scoped. The same scoped key with the same request hash returns the same job; a different request hash is a conflict.
- `leaseNext` atomically claims one due job by queue name, priority descending, run time ascending, and creation time ascending. Expired leases are reclaimable.
- `acknowledge`, `reject`, and `extendLease` require the active lease token.
- Retryable `reject` calls reschedule until `maxAttempts` is reached. Final rejection writes dead-letter state.
- `cancel` is cooperative and prevents future leases for non-terminal work.
- `getStatus` keeps terminal jobs visible for product workflows.
- `getStats` provides lightweight backpressure counters.

The SQLite table examples live in `../../migrations/sqlite`.

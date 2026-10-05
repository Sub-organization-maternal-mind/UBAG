# Attempt lease contract for helper execution, and the Helper Node vocabulary

Helper-dispatched work uses an Attempt Lease: 120 s TTL, 20 s renewal, a generation counter that fences stale writers (a result/heartbeat carrying an older generation is rejected), and an input fingerprint so a retried attempt cannot silently change its input. This is a delta from the live Exec Lease (90 s TTL, 10 s heartbeat, token-per-job, no generation), which stays unchanged for non-helper dispatch; both terms are defined in CONTEXT.md. "Fleet" in the worker (ADR-0005) is unrelated to the external Fleet Manager; contracts (proto, shared-schemas) must say Helper Node, never bare "fleet node". Post-submit ambiguity ends the job `failed` with `submitted=true` and `reconcile_required`; deadline-cut streams end `timed_out` with `data.partial`. Decided 2026-10-06 so contract work (packages/proto/proto/ubag/helper/v1) adopts one vocabulary before any code.

Status: accepted

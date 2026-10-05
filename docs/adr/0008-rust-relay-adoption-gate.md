# Rust relay adoption is gated by measurement with a pre-registered stop rule

No Rust relay work starts until the voice hardening branch is reconciled and instrumented, and live voice acceptance exists. The baseline is the optimised Python relay, measured after that merge, never the unoptimised one. Gates: proceed only if UBAG-controlled overhead is at least 20% or CPU/memory savings are at least 25% in the material mixed workload; the roadmap stop rule (stop if the optimised-Python baseline already meets the latency/footprint targets) is recorded before measuring and not edited afterwards. Without lab-host measurements (no isolated lab exists) the honest outcome is "not started; gate unevaluated". Decided 2026-10-06 because Rust would add a third runtime after the SDK pruning and comparing against unstable code is meaningless.

Status: accepted

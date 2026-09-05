# Contracts change before implementations

UBAG is contracts-first: `packages/openapi`, `packages/shared-schemas`, and `packages/proto` define the API and must change before any implementation (gateway, workers, SDKs), gated by `pnpm check:contracts` and `pnpm check:blueprint`. Decided because the SDKs are schema-generated and validated against the shared conformance suite — implementation-first drift breaks every client surface simultaneously and invisibly.

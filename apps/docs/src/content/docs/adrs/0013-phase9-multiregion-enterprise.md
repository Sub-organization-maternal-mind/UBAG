---
title: "ADR 0013: Phase 9 — Multi-Region & Enterprise"
description: Region-subject scheme, home-region write-fence, kill-switch states, MFA/JIT as UBAG-native, audit WORM + SIEM streaming, Garage backend.
---

# ADR 0013: Phase 9 — Multi-Region & Enterprise

**Status:** Accepted  
**Date:** 2026-06-02  
**Author:** UBAG Platform Team

---

## Status

Accepted (2026-06-02).

---

## Context

UBAG Phases 0–8 delivered a functionally complete, reliability-hardened, and operationally packaged platform. Phase 9 targets four enterprise capabilities: (1) multi-region job routing and geo-replication, (2) MFA and just-in-time admin elevation as first-class platform primitives, (3) immutable audit trails with SIEM integration, and (4) a self-hosted S3-compatible object store (Garage) for air-gapped or sovereign-cloud deployments. Several architectural decisions were required to ensure these features compose cleanly with each other and with prior phases, and that the existing single-region operating mode is entirely unaffected.

---

## Decision 1 — Region routing via NATS subject namespacing

**Decision:** Job routing across regions is expressed through NATS subject namespacing using the format `ubag.jobs.<region>.<lane>.<jobID>`. The `<region>` segment defaults to the literal string `"default"` for deployments that have not enabled multi-region mode. The `<lane>` segment preserves Phase 2b priority lanes (`high`, `normal`, `low`). The `<jobID>` segment is the existing opaque job identifier. No additional NATS stream or KV bucket is introduced for region routing; the existing stream definitions are augmented with subject-filter subjects that include the region prefix.

**Rationale:** Embedding the region in the subject name rather than in a header or metadata field keeps routing logic in the NATS subject tree where JetStream filter subjects can enforce it without consumer-side branching. The `"default"` fallback means that single-region clusters need no configuration change and produce subjects that are structurally identical to their Phase-2b predecessors with a fixed prefix prepended. This approach composes correctly with priority lanes: the region occupies a dedicated positional segment so that existing lane-based consumers can be updated to a subject-filter prefix match without code changes beyond the filter expression.

**Consequences:**

- `internal/region/router.go` implements `RouteSubject(region, lane, jobID string) string` and the inverse `ParseSubject(subj string) (region, lane, jobID string, err error)`.
- All new NATS subject definitions in `internal/region/` follow this scheme; legacy subjects emitted by prior phases are translated at the executor enqueue boundary.
- Documentation in `docs/operations/multi-region.md` illustrates the subject tree and shows how leaf-node subject import/export scopes are configured for cross-region forwarding.
- Single-region deployments experience no observable behavior change; their NATS subjects include `ubag.jobs.default.*.*` as an alias for the existing unqualified pattern.

---

## Decision 2 — Home-region pin is additive schema (nil = unpinned)

**Decision:** Tenant home-region affinity is stored in a new nullable column `home_region TEXT NULL` added to the `gateway_tenants` table. A `NULL` value means the tenant is unpinned and may be served by any region. A non-null value is a region identifier (matching the `<region>` segment of Decision 1) that acts as the write-fence: write operations for that tenant are accepted only by the named region; read operations may be served by any region with an in-region replica. The column is added via a migration that is included in the Phase 9 upgrade path and is fully backward-compatible.

**Rationale:** Making `home_region` nullable rather than defaulting to a sentinel string (e.g., `"default"`) avoids the need to update every existing tenant row when the migration runs. Existing tenants that have never had a home-region pin have `NULL`, which the runtime interprets as unpinned — the same behavior as before Phase 9. Operators can pin tenants incrementally; the change is applied at the row level with no platform restart required.

**Consequences:**

- `internal/region/pin.go` implements the `TenantPin` type and `GetPin` / `SetPin` operations over the store.
- The write-fence check in `internal/region/killswitch.go` reads `home_region` at request time (cached per tenant with a short TTL) and rejects write-class API calls that arrive at a non-home region with HTTP 307 Temporary Redirect pointing to the home-region endpoint.
- The `gateway_tenants` migration is included in the standard `ubag migrate` path; it is a no-op for single-region deployments.
- API documentation explicitly states that `null` in the `home_region` field of the tenant resource means "unpinned".

---

## Decision 3 — Kill switch is runtime state with three levels

**Decision:** The region kill switch exposes three operational states — `active`, `draining`, and `disabled` — stored in a NATS KV bucket keyed by region name. State transitions are triggered via `POST /v1/admin/regions/{region}/state`. The semantics are:

- **`active`** — normal operation; the region accepts new jobs and serves traffic.
- **`draining`** — the region rejects new job submissions (HTTP 503 with `Retry-After` pointing to another region) but continues executing in-flight jobs; the `/v1/ready` health probe returns HTTP 200 so the load balancer keeps the region in rotation for read traffic and job-status queries.
- **`disabled`** — the region rejects all traffic; the `/v1/ready` probe returns HTTP 503, causing the load balancer (GeoDNS health check or Kubernetes liveness probe) to remove the region from the rotation.

**Rationale:** Three states are the minimum necessary to support graceful traffic migration without data loss. A binary active/disabled switch forces operators to choose between hard cutover (which risks dropping in-flight jobs) and keeping a region in the load balancer when it should not receive new work. The `draining` intermediate state models the standard load-balancer drain pattern used by cloud providers and is sufficient for planned maintenance, failover rehearsal, and controlled geo-migration. Storing state in NATS KV rather than in Postgres avoids a dependency loop where the kill switch relies on the very database it may be protecting.

**Consequences:**

- `internal/region/killswitch.go` implements the three-state FSM, including transition guards (e.g., `disabled` → `active` requires explicit operator confirmation to prevent accidental re-activation).
- The `/v1/admin/regions/{region}/state` endpoint requires the `region:manage` capability, which in turn requires MFA verification (see Decision 4).
- `docs/operations/multi-region.md` includes a kill-switch drill runbook with expected health-probe behavior at each state transition.
- Monitoring: a Prometheus gauge `ubag_region_state` with labels `{region, state}` is emitted; the PrometheusRule added in Phase 8 (Task 2.2) is extended with an alert for `ubag_region_state{state="disabled"} > 0`.

---

## Decision 4 — MFA and JIT elevation are UBAG-native, not IdP-delegated

**Decision:** Multi-factor authentication (TOTP-based) and just-in-time admin elevation are implemented as first-class UBAG platform primitives in `internal/mfa/` and `internal/jitadmin/`. The IdP (Keycloak or any OIDC-compliant provider) is responsible for authenticating the principal's password credential; UBAG is responsible for gating its own privileged actions (capabilities `role:manage`, `data:export`, `region:manage`) behind a second factor and a time-boxed elevation grant. IdP-side MFA (e.g., Keycloak OTP) may coexist but is not a substitute for UBAG-native MFA on these actions.

**Rationale:** Delegating privileged-action gating to the IdP would require every supported IdP to implement step-up authentication flows that are consistently enforceable from UBAG's perspective. In practice, IdP step-up support is uneven across providers, and IdP MFA state is not visible to UBAG's authorization middleware at request time without additional token claims or introspection round-trips. Implementing MFA natively gives UBAG a single, auditable enforcement point that is independent of IdP capabilities, ensures that MFA events appear in the UBAG audit log (Decision 5), and allows elevation grants to carry fine-grained capability scopes and expiry that the IdP token cannot express.

**Consequences:**

- `internal/mfa/` implements TOTP enrollment, verification, recovery code generation, and the MFA middleware that gates privileged endpoints.
- `internal/jitadmin/` implements elevation grants: a principal requests elevation (`POST /v1/admin/elevation`), an approver approves it (`POST /v1/admin/elevation/{id}/approve`), and the grant is time-boxed (default 30 minutes, configurable per tenant).
- Elevation grant creation and approval are both themselves MFA-gated.
- Every MFA verification attempt (success or failure) and every elevation grant lifecycle event is written to the audit log (Decision 5).
- `docs/operations/enterprise-auth.md` documents the enrollment and elevation flows, including recovery code management.

---

## Decision 5 — Audit stays a hash chain (WORM) + SIEM streaming

**Decision:** The audit subsystem (`internal/audit/`) retains the append-only, hash-chain (WORM) design established in earlier phases. No mutation interface exists in the store layer; the database user running the gateway has `INSERT`-only grants on the audit table (no `UPDATE`, no `DELETE`). The `SealHead` anchor mechanism (periodic hash-chain snapshots stored off-table) is retained for tamper-evidence verification. In addition, Phase 9 adds a SIEM bridge (`internal/siem/`) that streams audit events to external SIEM systems (Splunk HTTP Event Collector, Elastic Beats, generic syslog) via the `BridgeStore` pattern: every `Append` call fans out to both the local audit store and the configured SIEM sinks.

**Rationale:** The WORM property ensures that audit records cannot be silently modified even by an attacker with database write access. Adding SIEM streaming does not weaken the WORM guarantee because the bridge fans out to both stores simultaneously; the local hash chain is authoritative and the SIEM copy is a secondary stream. Keeping the bridge inside the gateway process (rather than as a separate sidecar) eliminates the latency and reliability complexity of a sidecar queue, and keeps the fan-out synchronous so that a failed SIEM write can be surfaced as an auditable error rather than silently dropped.

**Consequences:**

- `internal/audit/` exposes only `Append`, `Query`, `VerifyChain`, and `SealHead` operations. No `Update` or `Delete` is defined.
- `internal/siem/` implements `BridgeStore`, `ExporterSink` (HTTP), `SyslogSink`, and the sink registry; sinks are configured via `UBAG_SIEM_*` environment variables documented in `docs/operations/enterprise-auth.md`.
- Audit records for MFA events (Decision 4) are written through the same `BridgeStore` as all other audit events.
- The `BridgeStore` applies a deadline to each SIEM sink write (default 5 s) and records sink failures in a dedicated Prometheus counter `ubag_siem_export_errors_total{sink}`.

---

## Decision 6 — Garage is an S3-compatible ArtifactStore backend

**Decision:** Garage (https://garagehq.deuxfleurs.fr/) is supported as an artifact storage backend via a thin constructor alias over the existing `MinIOArtifactStore`. A `GarageArtifactStore` constructor sets Garage-specific defaults (path-style addressing, multi-region endpoint selection) and is wrapped by a `ReplicatingArtifactStore` that fans writes to multiple Garage nodes to achieve geo-replication. No new S3 protocol surface is required; Garage's S3-compatible API is used as-is.

**Rationale:** Garage is the canonical self-hosted, S3-compatible object store for sovereign-cloud and air-gapped deployments where AWS S3 or MinIO-as-a-service is not available. Its multi-node, zone-aware replication maps directly onto UBAG's multi-region model. Because Garage is S3-compatible, no new SDK or HTTP client is needed; the `MinIOArtifactStore` implementation handles the protocol, and the `GarageArtifactStore` constructor is a thin alias that sets the correct endpoint and bucket-style parameters. The `ReplicatingArtifactStore` wrapper is storage-backend-agnostic and can be used with any `ArtifactStore` implementation, including MinIO or AWS S3, for symmetric geo-replication.

**Consequences:**

- `deploy/multi-region/garage/` contains the Garage configuration files and Docker Compose setup documented in `docs/operations/multi-region.md`.
- Operators enable Garage by setting `UBAG_ARTIFACT_BACKEND=garage` and `UBAG_GARAGE_ENDPOINTS=<comma-separated node URLs>`.
- The `ReplicatingArtifactStore` accepts a `ReplicationFactor` parameter (default 2); artifact writes succeed only if at least `ceil(ReplicationFactor / 2)` nodes acknowledge.
- The existing MinIO and AWS S3 backends are unaffected; Garage is an additive backend selection.

---

## Decision 7 — GeoReplication=On gates all Phase 9 enterprise features

**Decision:** All Phase 9 enterprise features (multi-region routing, home-region pinning, kill switch, NATS supercluster, Garage backend, SIEM streaming, JIT elevation, and the SSO authorization-code flow) are gated behind a single feature flag, `GeoReplication`, which is active when the deployment is operating under the `enterprise` profile or when the environment variable `UBAG_ENABLE_GEO_REPLICATION=1` is set. All lower-tier deployments (`edge`, `small`) and all existing test suites run with `GeoReplication=Off` and are entirely unaffected by Phase 9 code paths.

**Rationale:** A single top-level gate prevents Phase 9 code paths from activating in lower-tier deployments where the required infrastructure (NATS supercluster, pgactive, Garage) is absent. It also provides a clean rollback mechanism: clearing `UBAG_ENABLE_GEO_REPLICATION` or reverting the profile to `small` disables all Phase 9 features atomically. Gating individual features separately would require operators to manage multiple flags and would make it harder to reason about which features are active in a given deployment.

**Consequences:**

- `internal/region/region.go` exports `IsGeoReplicationEnabled(cfg Config) bool` which is the single authoritative check.
- All Phase 9 middleware, route registrations, and background goroutines check this flag at startup; if `GeoReplication=Off`, they are no-ops or return `nil` handlers.
- The `enterprise` profile in `internal/profile/` sets `GeoReplication=true` by default.
- CI runs the Phase 9 package tests (`internal/region/...`, `internal/mfa/...`, `internal/jitadmin/...`, `internal/audit/...`, `internal/siem/...`) unconditionally; the feature gate is tested explicitly with both `On` and `Off` states in those package tests.

---

## Consequences

Summary of cross-cutting consequences for Phase 9:

1. **NATS subject scheme** `ubag.jobs.<region>.<lane>.<jobID>` is the new canonical format. Single-region deployments use `default` as the region segment and are unaffected in behavior.
2. **Home-region pinning** is a per-tenant, nullable database column. Existing tenants are unpinned (`NULL`) by default; pinning is an incremental, operator-driven action.
3. **Kill switch** has three states (`active`, `draining`, `disabled`). Load-balancer shedding is driven by the `/v1/ready` probe; in-flight jobs are protected by the `draining` state.
4. **MFA and JIT elevation** are UBAG-native. IdP authentication and UBAG authorization are separate concerns; the IdP token proves identity, and UBAG enforces its own second factor for privileged actions.
5. **Audit WORM** is preserved. SIEM streaming is additive fan-out, not a replacement. Sink failures are surfaced via metrics, not silently dropped.
6. **Garage** is an additive S3-compatible backend. It requires no protocol changes; the `MinIOArtifactStore` implementation handles the wire protocol.
7. **`GeoReplication=On`** is the single gate for all Phase 9 features. Lower tiers and existing suites are unaffected.
8. Phase 9 introduces no changes to the job schema, plugin ABI, or SDK interfaces that are breaking for existing integrations. The NATS subject-scheme change is additive (new prefix, existing consumers updated with new filter expressions).

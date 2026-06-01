---
title: "ADR 0012: Phase 8 — Deployment Infrastructure"
description: Deploy path layout, Kubernetes operator module, goreleaser release brain, upgrade-only tier migration, and custom Caddy build with rate-limit and WAF modules.
---

# ADR 0012: Phase 8 — Deployment Infrastructure

**Status:** Accepted  
**Date:** 2026-06-02  
**Author:** UBAG Platform Team

---

## Status

Accepted (2026-06-02).

---

## Context

UBAG Phases 0–7 delivered a functionally complete and reliability-hardened platform. Phase 8 targets the operational deployment layer: packaging, release automation, Kubernetes-native management, multi-cloud Terraform, and a hardened HTTP reverse proxy. Several structural decisions were required to keep the deployment artefacts coherent across three deployment tiers (edge, small, enterprise) and five cloud targets.

---

## Decision 1 — `deploy/` path stays; no rename to `infra/`

**Decision:** The existing `deploy/` directory is retained as the canonical root for all deployment artefacts (Docker Compose files, Helm chart, Terraform modules, Grafana dashboards, GitOps manifests, installers). It is **not** renamed to `infra/`.

**Rationale:** `infra/` is a common convention in Terraform-first organisations where infrastructure provisioning is the primary concern. UBAG's deployment surface spans Compose, Helm, Terraform, and bare-metal installers equally; `deploy/` is a neutral term that does not privilege any one mechanism. Renaming would break all existing Makefile targets, CI paths, and operator runbook references with no functional benefit.

**Consequences:**
- All new Phase 8 artefacts are placed under `deploy/` (e.g., `deploy/operator/`, `deploy/terraform/`, `deploy/helm/`, `deploy/compose/`).
- Documentation and CI references continue to use `deploy/` as the canonical root.

---

## Decision 2 — Kubernetes Operator is a separate Go module at `deploy/operator/`

**Decision:** The UBAG Kubernetes operator is implemented as a standalone Go module rooted at `deploy/operator/`, with its own `go.mod` and `go.sum`. It is **not** placed inside `apps/gateway/`.

**Rationale:** The operator uses `controller-runtime` and `client-go`, which introduce transitive dependencies (CRD scaffolding, leader-election, webhook registration) that are orthogonal to the gateway's HTTP/NATS/plugin surface. Co-locating the operator inside `apps/gateway/` would inflate the gateway's dependency graph and complicate upgrade paths. A separate module enforces a clean boundary: the operator may import public gateway types (via versioned interfaces), but the gateway does not depend on the operator.

**Consequences:**
- The `operator` CI job in `.github/workflows/ci.yml` runs `go build ./...` and `go test ./...` from `deploy/operator/`, using `deploy/operator/go.mod` to pin the Go version.
- `go-version-file: deploy/operator/go.mod` is the authoritative Go version source for the operator CI job.
- The operator is released as a separate container image (`ghcr.io/ubag/operator`) via goreleaser.

---

## Decision 3 — goreleaser is the single release brain

**Decision:** All release artefacts — Go binaries (`ubag`, `ubag-operator`), container images, Helm chart packaging, SBOM generation, and GitHub Release creation — are driven by a single `.goreleaser.yaml` at the repository root. No alternative release mechanisms (manual `docker build`, ad-hoc `helm package`) are used in CI.

**Rationale:** A single release tool eliminates drift between binary versions and container image tags. goreleaser's `--snapshot` flag enables local testing of the full release pipeline without publishing. The `make release-snapshot` target provides a deterministic local preview. goreleaser's SBOM integration (via `syft`) supersedes the separate `anchore/sbom-action` step in CI for release builds; the CI security-scan job retains `anchore/sbom-action` only for PR-level SBOM generation.

**Consequences:**
- `make release` runs `goreleaser release --clean` (production).
- `make release-snapshot` runs `goreleaser build --snapshot --clean` (local preview, no publish).
- The `.goreleaser.yaml` is the authoritative source of truth for binary names, archive formats, and container image labels.
- Hotfixes must go through the same goreleaser pipeline; there is no out-of-band release path.

---

## Decision 4 — Upgrade-only tier migration (no downgrades)

**Decision:** The `ubag migrate` command supports only **upgrade** transitions between deployment tiers (edge → small → enterprise). Downgrade paths (e.g., enterprise → small) are explicitly unsupported and rejected with a descriptive error.

**Rationale:** Tier upgrades are additive: moving from edge to small provisions additional infrastructure (Postgres, NATS cluster, Kubernetes) that does not exist in the lower tier. Downgrades require deprovisioning shared infrastructure that may be in use by other tenants or processes, and require data migration decisions (e.g., exporting from Postgres back to SQLite) that are highly environment-specific. Supporting downgrades automatically would create false confidence in an operation that requires human judgment about data and infrastructure state.

**Consequences:**
- `ubag migrate --to <tier>` validates that `<tier>` is strictly higher than the current tier. If not, it exits with a non-zero status and a message directing the operator to the tier-migration runbook.
- The `make migrate-tier` Makefile target accepts `TO=<tier>`, `FROM=<tier>`, and `DRY_RUN=--dry-run` variables. It always passes `--to $(TO)` and optionally `--from $(FROM)` and `$(DRY_RUN)`.
- Operators who need to move to a lower tier must follow the manual runbook in `docs/operations/tier-migration.md`, which covers data export, infrastructure decommission, and fresh installation at the target tier.

---

## Decision 5 — Custom Caddy build (xcaddy with rate-limit + WAF)

**Decision:** The UBAG HTTP reverse proxy is a custom Caddy binary built with `xcaddy`, incorporating two non-standard modules:

1. **`github.com/mholt/caddy-ratelimit`** — per-IP and per-route rate limiting with configurable burst and window parameters.
2. **`github.com/corazawaf/coraza-caddy`** — Coraza WAF integration providing OWASP Core Rule Set (CRS) enforcement.

The standard `caddy` binary from the upstream Docker image or package manager is **not** used in production deployments.

**Rationale:** The upstream Caddy binary does not include rate-limiting or WAF modules. Both capabilities are required for production edge and small-tier deployments where UBAG is exposed to the public internet. `xcaddy` is the official Caddy extension build tool and produces a standard Go binary with no runtime module loading; the resulting binary is statically linked and does not require the Caddy module system at runtime.

**Consequences:**
- `node tools/check-caddy.mjs` validates that the Caddy configuration file references only modules present in the expected custom build. This check runs in CI (`lint` job) and is exposed via `make caddy-validate`.
- The `release-snapshot` and `release` targets produce the custom Caddy binary as part of the goreleaser build matrix.
- Operators building Caddy locally must have `xcaddy` installed (`go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest`). The build command is documented in `docs/operations/deployment.md`.
- The WAF CRS ruleset version is pinned in `.goreleaser.yaml` to prevent unintended rule updates from breaking existing deployments.

---

## Consequences

Summary of cross-cutting consequences for Phase 8:

1. **`deploy/` path** is stable and canonical. No renaming. All tooling, CI, and documentation reference `deploy/`.
2. **Operator module** at `deploy/operator/` has an independent Go dependency graph. Gateway and operator upgrades are independently releasable.
3. **goreleaser** is the single release mechanism. Manual release steps outside `.goreleaser.yaml` are not supported and will not be documented.
4. **Tier migration** is upgrade-only. Downgrade requires a manual runbook. `ubag migrate` will refuse downgrade requests.
5. **Custom Caddy** is required for all internet-facing deployments. The standard Caddy binary must not be substituted. A CI check (`node tools/check-caddy.mjs`) enforces configuration compatibility.
6. Phase 8 introduces no changes to the public REST API, job schema, plugin ABI, or SDK interfaces. All changes are infrastructure and packaging.

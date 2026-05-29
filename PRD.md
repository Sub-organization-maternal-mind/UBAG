# UBAG Product Requirements Document

## 1. Product Intent

UBAG, the Universal Browser-Automation Gateway, is a self-hostable open-source platform that lets desktop apps, server apps, mobile apps, browser extensions, CLIs, no-code tools, and legacy systems drive web-based AI and automation targets through stable APIs.

The first implementation follows a docs-first approach. The PRD, progress ledger, ADRs, protocol docs, operating docs, and runnable Astro Starlight docs site were established first; the repository now layers the v0 edge runtime, SDK, worker, dashboard, and small-profile scaffolding on top of that baseline.

## 2. Locked Decisions

- Delivery model: staged platform, not a big-bang implementation.
- First deployment targets: `edge` and `small`.
- First adapter strategy: mock and generic adapters in v0; all listed AI providers by v1.
- Provider login model: user-owned manual login through live session/noVNC.
- Automation stance: safe mode first; no bundled CAPTCHA solver, credential scraping, or hidden account takeover.
- Privacy default: Standard first; HIPAA/GDPR modes are planned but not v0 blockers.
- License posture: Apache-2.0 for SDKs, adapters, and templates; AGPL-3.0 for server-side platform components.
- Docs framework: Astro Starlight.
- Package manager: pnpm.
- Visual system: Hallmark NAJM direction from `design.md`.

## 3. Audiences

- App developers integrating UBAG from desktop, web, backend, mobile, scripts, extensions, or no-code systems.
- Platform operators running UBAG on a laptop, small VM, Kubernetes cluster, or enterprise environment.
- Adapter developers maintaining target-specific browser automation.
- Security reviewers and compliance owners validating data handling and audit controls.

## 4. Goals

- Provide one stable gateway for many clients and many browser automation targets.
- Make retries safe through idempotency and stable error contracts.
- Support both lightweight local operation and production self-hosting.
- Keep browser automation isolated from durable control-plane state.
- Make observability, audit, and operational recovery first-class.
- Keep SDK contract manifests generated from shared contracts and verify SDK behavior with conformance tests.
- Preserve user-owned accounts and audited manual login for web AI targets.

## 5. Milestone 0 Boundary

Milestone 0 was the docs-first baseline. It intentionally established the reviewable planning and contract surface before product service code was introduced. The current repository includes post-Milestone-0 implementation work for the gateway, worker, adapters, SDKs, CLI, dashboard, deployment profile, security contracts, observability contracts, and conformance fixtures.

## 6. Release Phases

### Milestone 0: Docs-First Baseline

Deliver planning docs, ADRs, protocol docs, Starlight docs site, and progress tracking. Acceptance is a successful `pnpm install`, docs build, and blueprint coverage check.

### v0: Edge MVP

Deliver monorepo foundations, REST jobs API, SSE events, guarded WebSocket baseline, app-secret auth, idempotency, stable errors, SQLite/localfs contracts, mock target, generic adapter manifests, provider safe-mode manual-session path, minimal CLI, sidecar, static dashboard prototype, and opt-in small-profile integrations for Postgres stores, NATS dispatch/worker consumption, MinIO artifacts, signed webhook outbox delivery, observability contracts, and the first TypeScript/Python/Go SDK wave.

### v1: Production OSS Platform

Harden the v0/small opt-in surfaces for GA production operation, add native gRPC/gRPC-Web serving, richer bidirectional WebSocket/dashboard semantics, live user-owned provider adapters, broader workflow/template/cache runtime behavior, output normalization, semantic cache hardening, rate-limit runtime enforcement, secret rotation, and additional SDK conformance where live transports exist.

### v2: Enterprise And Ecosystem

Deliver remaining SDKs, WASM plugin system, adapter registry, Helm, Terraform, installers, GitOps, SSO/SAML/SCIM, mTLS, multi-region, SIEM export, mobile monitor, and formal disaster recovery.

## 7. Success Criteria

- Every public feature area from the blueprint is mapped to a milestone.
- Documentation is navigable through the Starlight site.
- The docs site uses NAJM/Hallmark visual direction without fabricated metrics or claims.
- The implementation workstreams can be picked up independently through the documented contracts, tests, and ownership boundaries.
- The docs-first baseline remains auditable even though product code now exists.

## 8. Risks

- Provider website drift can make real adapters fragile. Mitigation: mock/generic first, adapter SDK tests, drift detection, canary rollout, and manual-login audit.
- Multi-profile storage can diverge. Mitigation: common store/queue/blob interfaces and profile conformance tests.
- Semantic cache can leak sensitive data. Mitigation: tenant partitioning and disabling sensitive caches in HIPAA/GDPR modes.
- SDK parity can decay. Mitigation: shared fixtures and conformance gates for every SDK.
- Overbuilding enterprise scope too early can delay useful delivery. Mitigation: staged release boundaries and explicit v0/v1/v2 acceptance gates.

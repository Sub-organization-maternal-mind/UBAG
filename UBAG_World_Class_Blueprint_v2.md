# Universal Browser-Automation Gateway — World-Class Engineering Blueprint v2.0

**Project Codename:** `UBAG` (Universal Browser Automation Gateway)
**Document Type:** Master Architecture & Build Plan
**Status:** Engineering-ready blueprint
**Last revised:** 2026-05-22
**Supersedes:** *ChatGPT API Integration Plan* (2026-05-21)

---

## 0. What changed vs the previous plan

The previous plan correctly identified the pivot from *one desktop app → one bot* to *many apps → one gateway → many targets*. This revision keeps that core insight and rebuilds everything around five non-negotiable engineering principles:

| Principle | Concrete meaning |
|---|---|
| **100% open-source** | Every component — runtime, database, queue, observability, browser engine, SDK toolchain — is permissively licensed (MIT / Apache-2.0 / BSD / MPL / AGPL). Zero vendor lock-in. Self-host anywhere. |
| **Performance-first** | Sub-100 ms gateway p99 (excluding browser work). Single worker handles 50+ concurrent sessions on commodity hardware. Cold-start a new browser session in < 1.5 s through pre-warming. |
| **Cross-compatible** | 11 first-class SDKs auto-generated from a single OpenAPI 3.1 + gRPC schema. 6 wire protocols. Runs on x86_64 + ARM64 + RISC-V. Windows/macOS/Linux/BSD. |
| **Lightweight, tiered** | Runs as a single 25 MB static binary with SQLite on a Raspberry Pi, *or* scales horizontally across multi-region Kubernetes. Same codebase, four deployment profiles. |
| **Stability over features** | Idempotency keys, circuit breakers, bulkheads, exactly-once-ish job semantics, transactional outbox, saga workflows, chaos-tested. Every failure mode is named and handled. |

**Net effect:** instead of a hand-rolled prototype glued to one website, you get an extensible platform with the operational maturity of a real product, while staying small enough to run on a $5 VPS.

---

## 1. Vision & Product Surface

### 1.1 One-sentence vision
> A single, self-hostable, open-source gateway that lets *any* application — desktop, mobile, server, script, browser extension — drive *any* web-based AI or automation target through stable, versioned APIs, with the operational guarantees of a real distributed system.

### 1.2 Who connects in
- **Desktop apps** — Electron, Tauri, .NET (WPF/WinForms/MAUI/Avalonia), Swift, Qt, GTK, JavaFX, Python (PySide/Tk), Flutter desktop.
- **Server/backend apps** — Node, Python, Go, Rust, Java, Ruby, PHP, Elixir, .NET, microservices.
- **Mobile apps** — iOS (Swift), Android (Kotlin), React Native, Flutter, Capacitor, .NET MAUI.
- **Browser extensions** — Chrome/Edge/Firefox/Safari extensions speaking REST or WebSocket.
- **CLIs / scripts** — Bash, PowerShell, Python, Lua, AppleScript — through the CLI binary or HTTP.
- **No-code / iPaaS** — n8n, Activepieces, Make, Zapier-style adapters via REST + Webhook.
- **Legacy apps** — through the localhost Sidecar Connector (no code changes needed to the legacy app).

### 1.3 What sits behind the gateway
- **Web AI chat targets** — DeepSeek Web, Claude.ai, ChatGPT, Gemini, Mistral Le Chat, Perplexity, Poe, Kimi, Qwen Chat, You.com, plus any future site.
- **Custom internal portals** — hospital PACS/RIS, EMRs, ERP web UIs, ticketing systems, dashboards.
- **Generic web tasks** — form fill, data extraction, OCR cleanup pipelines, document download, multi-step workflows.

---

## 2. Engineering Principles (non-negotiable)

These are the *hard rules* every PR must honor. They take precedence over feature velocity.

1. **No proprietary runtime dependencies.** PostgreSQL not Oracle; NATS or Redis not SQS; MinIO not S3; Grafana not Datadog. Cloud-hosted versions are optional, never required.
2. **API stability is a covenant.** Every public surface (`/v1/...`, gRPC `v1`, SDK methods) is versioned, deprecation-policied (12-month minimum), and covered by a conformance test suite shared across all SDKs.
3. **Idempotency by default.** Every state-mutating call requires (or auto-generates) an `Idempotency-Key`. Retries are safe.
4. **Backpressure everywhere.** Bounded queues, bounded connection pools, bounded memory per worker, bounded request bodies. The system slows down before it falls down.
5. **Observability is not optional.** Every request emits structured logs (JSON), metrics (Prometheus), and traces (OpenTelemetry). Sampling is configurable; the wire format is open.
6. **Secrets never touch disk in plaintext.** AES-256-GCM at rest, age/sops for config, OS keychain for the sidecar. Rotation is a first-class operation.
7. **Configuration is data, code is logic.** Adapters, prompt templates, rate limits, target definitions live in versioned config (Postgres + Git-syncable YAML), not in code.
8. **Lightweight mode must always work.** A new contributor must be able to clone, `make dev`, and have a working end-to-end system in under 5 minutes with no external services.
9. **No telemetry phones home.** Anonymous opt-in usage stats only, with full disclosure and a hard off switch.
10. **Every error is named and documented.** Stable error codes (`UBAG-AUTH-001`, `UBAG-ADAPTER-DRIFT-014`) — never just `500 Internal Server Error`.

---

## 3. Open-Source Technology Stack

The stack is chosen for the intersection of *performance, maturity, OSS license, small binary footprint, and polyglot tooling*. Where a choice has a strong alternative, both are listed so deployers can swap.

### 3.1 Core runtime & language choices

| Layer | Primary choice | License | Why | Alt |
|---|---|---|---|---|
| **API Gateway service** | **Go 1.22+** (chi router, std `net/http`) | BSD/MIT | Single static binary, ~15 MB, zero-GC pause matters at the edge, brilliant std lib, easy cross-compile to all OSes + ARM64. | Rust + `axum` (faster but slower to iterate); Bun + Hono (great DX, larger runtime) |
| **Browser worker** | **Python 3.12 + uvloop + Playwright** | PSF/Apache-2.0 | Playwright's Python binding is first-class, ecosystem of stealth tooling is Python-heavy (patchright, rebrowser-playwright, undetected-playwright). | Node + patchright (equally valid) |
| **Job orchestrator** | **Go** (same binary or separate) | BSD | Reuses gateway types, no FFI. | — |
| **Local Sidecar Connector** | **Rust 1.78+** (axum, tokio) | MIT/Apache-2.0 | Smallest possible binary (~4 MB), zero runtime dependency, signed releases, ships as a Windows service / macOS launchd plist / systemd unit. | Go (slightly larger) |
| **CLI** | **Go** with [Charm Bubble Tea](https://github.com/charmbracelet/bubbletea) for TUI | MIT | One binary, gorgeous TUI mode, cross-compiles to 25+ targets. | — |
| **Admin Dashboard** | **SvelteKit** + **Skeleton UI** + **Tailwind** | MIT | Smallest JS bundle of any modern framework (~30 KB hydration). PWA-installable. | SolidStart |
| **Mobile monitoring app** | **Tauri Mobile** (Rust + Svelte) | MIT/Apache | Same Svelte codebase, native binary, < 5 MB. | Flutter |

### 3.2 Data & messaging layer

| Layer | Primary | License | Why | Alt for lightweight tier |
|---|---|---|---|---|
| **OLTP database** | **PostgreSQL 16** with `pgvector`, `pg_partman`, `pg_cron`, `pg_stat_statements` | PostgreSQL License (BSD-like) | Most reliable OSS database, partitioning for jobs table, vector search for semantic cache, built-in cron, audit-log-ready. | **SQLite 3.45+** with WAL + Litestream for replication (Tier 1 only) |
| **Cache + ephemeral state** | **DragonflyDB** (Redis wire-compatible, BSL→Apache) or **Valkey** (Linux Foundation fork of Redis, BSD) | BSL/BSD | Drop-in Redis, 25× throughput on multi-core, single binary, no Lua quirks. | Redis 7 OSS / `ristretto` in-process cache |
| **Job queue / pub-sub** | **NATS JetStream** | Apache-2.0 | At-least-once + exactly-once-ish, persistent streams, request-reply, multi-tenant accounts, 12 MB binary, runs as cluster of 1. | **River** (Postgres-backed Go queue) for Tier 1 — zero extra service |
| **Object storage** (screenshots, recordings, HARs) | **MinIO** or **Garage** (Deuxfleurs) | AGPL-3.0 / AGPL-3.0 | S3-compatible. Garage is 15 MB, geo-distributed, no metadata DB needed. | Local filesystem with content-addressed naming |
| **Analytics / time series** | **ClickHouse** (per-job analytics) — *optional Tier 3+* | Apache-2.0 | Billions of events, real-time dashboards. | Postgres + `timescaledb` extension |
| **Full-text search** | **Meilisearch** or **Tantivy** (embedded) | MIT | Sub-50 ms search over jobs, prompts, errors. | Postgres `tsvector` |
| **Secret store** | **age** + filesystem; **HashiCorp Vault OSS** for Tier 3+ | BSD / MPL-2.0 | age is 1 MB; Vault for orgs that need it. | OS keychain via `keyring` |

### 3.3 Observability stack (all OSS, all opt-in)

| Concern | Tool | Notes |
|---|---|---|
| Metrics | **Prometheus** + **VictoriaMetrics** (long-term) | Pull model, std exposition format |
| Logs | **Loki** + **Promtail** / **Vector** | Cheap, label-indexed |
| Traces | **Tempo** + OpenTelemetry SDK | W3C tracecontext propagated through SDK → gateway → worker → browser |
| Dashboards | **Grafana OSS** | Pre-built dashboards shipped in `infra/grafana/` |
| Alerts | **Alertmanager** + **Karma** | Routes to Slack/Discord/Telegram/PagerDuty/Webhook |
| Continuous profiling | **Pyroscope** / **Parca** | Flame graphs in production |
| Error aggregation | **GlitchTip** (OSS, Sentry wire-compat) | Self-hosted, no SaaS lock-in |
| Synthetic monitoring | Built-in (see §19) | Canary jobs run every N minutes against each target |

### 3.4 Browser engine & anti-bot stack

| Layer | Tool | Why |
|---|---|---|
| Browser automation | **Playwright** + **Patchright** (Python) | Patchright is a drop-in Playwright fork that defeats common bot detection (CDP traces, navigator props, runtime evals) |
| Browser binary | **Chromium for Testing** (managed via `playwright install chromium`) | Pinned versions, fully OSS |
| Stealth | **patchright** + **fake-headless** profiles + custom user-data-dir | See §13 |
| Fingerprint randomization | **fingerprint-suite** (open-source, MIT) | Generates realistic UA/screen/canvas/audio/WebGL fingerprints |
| Proxy rotation (optional) | **goproxy** for HTTP rotation; or external residential pool of user's choice | Pluggable |
| CAPTCHA handling | Manual queue UI in dashboard (operator solves); pluggable solver adapter | We do not ship a paid solver |

### 3.5 Build, package, distribute

| Concern | Tool |
|---|---|
| Monorepo | **Turborepo** (apps/packages) + **Nx**-compatible task graph |
| Container base | **distroless** (`gcr.io/distroless/static`) or **Alpine 3.20** — final images < 30 MB |
| Single-binary packaging | Go `embed` for assets; `goreleaser` for cross-platform builds |
| Reproducible builds | `nix flake` + `goreleaser` with SLSA-3 provenance |
| Signing | **cosign** (Sigstore) for containers; **minisign** for binaries |
| SBOM | **syft** generates SPDX SBOM on every release |
| Schema-driven SDKs | **OpenAPI Generator** + **buf** (Protobuf) + **stainless-style** custom generators |

---

## 4. Deployment Profiles (Tiered Lightweight → Enterprise)

A core design constraint is that **the same codebase runs across four profiles**, selected at startup by `UBAG_PROFILE=edge|small|standard|enterprise` plus config overrides.

### 4.1 Tier 1 — `edge`
**Target:** Raspberry Pi 5 / NUC / $5 VPS / developer laptop.
**Footprint:** 1 process, ~80 MB RAM idle, single static binary, SQLite, embedded queue (River), in-process pub-sub, local filesystem object storage.
**Capacity:** ~5 concurrent jobs, 1–3 browser sessions, single user/tenant.
**Use case:** Personal use, single hospital workstation, demo, dev loop.

### 4.2 Tier 2 — `small`
**Target:** Single VM, docker-compose.
**Footprint:** Gateway + 1–3 workers + Postgres + DragonflyDB + MinIO + Grafana.
**Capacity:** ~50 concurrent jobs, 10–30 browser sessions, multi-tenant up to ~10 apps.
**Use case:** Small clinic, single-team SaaS, internal tool.

### 4.3 Tier 3 — `standard`
**Target:** Kubernetes (k3s, k0s, or full k8s) on 3–10 nodes.
**Footprint:** Helm chart with HA Postgres (CloudNativePG operator), DragonflyDB cluster, NATS JetStream cluster, MinIO distributed, full observability stack.
**Capacity:** 1000+ concurrent jobs, browser workers auto-scaled, multi-tenant SaaS.
**Use case:** Production SaaS, multi-hospital, multi-app integration platform.

### 4.4 Tier 4 — `enterprise`
**Target:** Multi-region k8s with global load balancing.
**Footprint:** Tier 3 × N regions + global control plane + cross-region replication (Postgres logical replication or `pgactive`), Garage for geo-replicated object storage, multi-region NATS supercluster.
**Capacity:** Effectively unlimited.
**Use case:** Hospital networks, large enterprises, federal/regulated workloads.

### 4.5 Profile feature matrix

| Feature | edge | small | standard | enterprise |
|---|:---:|:---:|:---:|:---:|
| REST + WebSocket API | ✓ | ✓ | ✓ | ✓ |
| gRPC API | ✓ | ✓ | ✓ | ✓ |
| SDK support | All | All | All | All |
| Persistent jobs | ✓ (SQLite) | ✓ (PG) | ✓ (PG HA) | ✓ (multi-region) |
| Browser session pool | ≤3 | ≤30 | unlimited | unlimited |
| Admin dashboard | ✓ | ✓ | ✓ | ✓ + SSO |
| Webhooks | ✓ | ✓ | ✓ | ✓ |
| Idempotency, retries, DLQ | ✓ | ✓ | ✓ | ✓ |
| Semantic cache | optional | ✓ | ✓ | ✓ |
| Multi-tenant RBAC | — | ✓ | ✓ | ✓ |
| SSO (OIDC/SAML) | — | — | ✓ | ✓ |
| SCIM provisioning | — | — | optional | ✓ |
| Audit log → SIEM | — | local file | ✓ | ✓ + immutable |
| Distributed tracing | local | ✓ | ✓ | ✓ |
| Geo-replication | — | — | optional | ✓ |
| HIPAA/GDPR mode | optional | ✓ | ✓ | ✓ |

---

## 5. Revised High-Level Architecture

```
┌────────────────────────────────────────────────────────────────────────┐
│                          CLIENT TIER (anywhere)                        │
│  Desktop · Mobile · Server · Extension · Script · No-code · Legacy     │
│       │              │              │           │           │          │
│   SDK / REST    WebSocket / SSE   gRPC      Webhook ←     Sidecar      │
└───────┼──────────────┼──────────────┼───────────┼───────────┼──────────┘
        │              │              │           │           │
        ▼              ▼              ▼           ▼           ▼
┌────────────────────────────────────────────────────────────────────────┐
│                        EDGE / INGRESS                                  │
│   Caddy 2 (TLS, HTTP/3) │ rate-limit │ mTLS │ DDoS shield │ WAF        │
└────────────────┬───────────────────────────────────────────────────────┘
                 │
┌────────────────▼───────────────────────────────────────────────────────┐
│                       UBAG CONTROL PLANE                               │
│                                                                        │
│  ┌──────────────┐  ┌──────────────┐  ┌─────────────────┐               │
│  │ API Gateway  │  │ AuthN/AuthZ  │  │ Tenant Registry │               │
│  │  (Go/chi)    │  │ JWT·mTLS·    │  │ Apps · Devices  │               │
│  │  REST·WS·    │  │ OAuth2·OIDC  │  │ Users · Roles   │               │
│  │  gRPC·SSE    │  │ API keys     │  │ Quotas · Scopes │               │
│  └──────┬───────┘  └──────┬───────┘  └────────┬────────┘               │
│         │                 │                   │                        │
│  ┌──────▼─────────────────▼───────────────────▼──────────┐             │
│  │              Command Validator (JSON Schema)          │             │
│  │            Idempotency · Schema versioning            │             │
│  └──────────────────────┬────────────────────────────────┘             │
│                         │                                              │
│  ┌──────────────────────▼─────────────────────────────────┐            │
│  │            Job Orchestrator (sagas, retries)           │            │
│  │  Priority lanes · DLQ · Transactional outbox · CRON    │            │
│  └────┬─────────────────────────┬───────────────────────┬─┘            │
│       │                         │                       │              │
│       ▼                         ▼                       ▼              │
│  ┌─────────────┐         ┌──────────────┐       ┌──────────────┐       │
│  │ Prompt      │         │ Semantic     │       │ Webhook      │       │
│  │ Template    │         │ Cache (vec)  │       │ Dispatcher   │       │
│  │ Engine      │         │ + dedup      │       │ +retries+sig │       │
│  └─────────────┘         └──────────────┘       └──────────────┘       │
└────────────────┬───────────────────────────────────────────────────────┘
                 │
            NATS JetStream (or River for edge)
                 │
┌────────────────▼───────────────────────────────────────────────────────┐
│                       BROWSER WORKER FLEET                             │
│                                                                        │
│  ┌─────────────────────────────────────────────────────────────┐       │
│  │  Worker process (Python + Playwright + Patchright)          │       │
│  │  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐       │       │
│  │  │ Session Pool │  │ Adapter      │  │ Stealth +    │       │       │
│  │  │ + warming    │  │ Runtime      │  │ Fingerprint  │       │       │
│  │  └──────┬───────┘  └──────┬───────┘  └──────────────┘       │       │
│  │         │                 │                                 │       │
│  │  ┌──────▼─────────────────▼─────────────────────────┐       │       │
│  │  │ Target Adapters (DeepSeek, Claude.ai, Gemini, …) │       │       │
│  │  └─────────────────────┬────────────────────────────┘       │       │
│  └────────────────────────┼───────────────────────────────────┘        │
│                           ▼                                            │
│             Browser sessions (Chromium contexts)                       │
└────────────────┬───────────────────────────────────────────────────────┘
                 │
┌────────────────▼───────────────────────────────────────────────────────┐
│   DATA · OBSERVABILITY · CONTROL                                       │
│  Postgres · DragonflyDB · MinIO/Garage · ClickHouse(opt) · Meilisearch │
│  Prometheus · Loki · Tempo · Grafana · Alertmanager · GlitchTip        │
│  Admin Dashboard (SvelteKit) · noVNC live viewer · Mobile monitor      │
└────────────────────────────────────────────────────────────────────────┘
```

Key differences from the previous diagram:
- Edge/ingress layer made explicit (Caddy 2 — auto-TLS, HTTP/3 by default, 5 MB binary).
- AuthN/AuthZ broken out; supports JWT, mTLS, OAuth2, OIDC, API keys, device-bound tokens.
- Command validator with JSON Schema + idempotency before anything is queued.
- Semantic cache and prompt template engine sit *before* the queue — many requests never need a browser.
- Webhook dispatcher is a dedicated component with signed deliveries, retries, and DLQ.
- Browser worker fleet is horizontally scalable; session pools have explicit warming.
- Observability is a peer, not an afterthought.

---
## 6. Universal Command Contract v2 (versioned, idempotent, typed)

The contract is the single most important asset. Every SDK, adapter, dashboard, and worker depends on it. It's defined once in **OpenAPI 3.1** + **Protobuf v3** and code-generated to every SDK.

### 6.1 Job request envelope (v2)

```json
{
  "api_version": "2026-05-22",
  "idempotency_key": "01HXY5ZS3KAFQ3PZ8NQ8C7H1AT",
  "client": {
    "app_id": "radiology-reporting-windows",
    "app_version": "1.4.2",
    "device_id": "dev_01HX...",
    "user_ref": "dr.sidra@hospital.example",
    "sdk": { "name": "ubag-dotnet", "version": "2.0.1" }
  },
  "job": {
    "target": "deepseek_web",
    "command_type": "medical_report_generation",
    "conversation_id": "conv_123",
    "template_id": "radiology_ct_brain_v3",
    "input": {
      "modality": "CT",
      "region": "Brain",
      "patient_age": 55,
      "patient_sex": "M",
      "findings": "Acute infarct in left MCA territory"
    },
    "options": {
      "priority": "normal",
      "timeout_seconds": 180,
      "return_mode": "final",
      "response_formats": ["markdown", "plain_text", "sections"],
      "retry_policy": "default",
      "cache_policy": "semantic_30d",
      "trace_context": "00-4bf92f3577b34da6a3ce-00f067aa0ba902b7-01"
    },
    "callbacks": {
      "webhook_url": "https://app.example.com/ubag/callback",
      "webhook_secret_id": "wh_sec_abc"
    },
    "context": {
      "locale": "en-GB",
      "tags": ["radiology", "ct", "stroke-pathway"],
      "cost_center": "rad-dept"
    }
  }
}
```

### 6.2 Job response envelope

```json
{
  "api_version": "2026-05-22",
  "job_id": "job_01HXY5ZS3KAFQ3PZ8NQ8C7H1AU",
  "idempotent_replay": false,
  "status": "completed",
  "target": "deepseek_web",
  "result": {
    "output": {
      "text": "...full report...",
      "markdown": "...",
      "plain_text": "...",
      "sections": { "findings": "...", "impression": "..." },
      "html": "..."
    },
    "validation": { "schema_id": "radiology_ct_brain_v3.out", "passed": true },
    "cached": false,
    "cache_source": null
  },
  "metadata": {
    "queued_at": "2026-05-22T07:10:00Z",
    "started_at": "2026-05-22T07:10:01Z",
    "completed_at": "2026-05-22T07:10:42Z",
    "duration_ms": 41210,
    "browser_session_id": "sess_01HX...",
    "adapter": "deepseek_web@1.7.3",
    "worker": "worker-eu-west-3",
    "retries": 0,
    "cost": { "browser_seconds": 41.2, "credits": 1 }
  },
  "trace_id": "4bf92f3577b34da6a3ce29d0f3b...",
  "events_url": "/v1/jobs/job_01HXY.../events"
}
```

### 6.3 Stable error contract

Every error returns:
```json
{
  "error": {
    "code": "UBAG-ADAPTER-DRIFT-014",
    "category": "adapter",
    "message": "Target UI changed; submit button selector not found",
    "retryable": true,
    "retry_after_ms": 5000,
    "details": { "adapter": "deepseek_web@1.7.3", "step": "submit_prompt" },
    "doc_url": "https://docs.ubag.dev/errors/UBAG-ADAPTER-DRIFT-014",
    "trace_id": "4bf92f3577..."
  }
}
```

**Error code namespaces:** `UBAG-AUTH-*`, `UBAG-VALIDATION-*`, `UBAG-QUOTA-*`, `UBAG-RATE-*`, `UBAG-QUEUE-*`, `UBAG-WORKER-*`, `UBAG-BROWSER-*`, `UBAG-ADAPTER-*`, `UBAG-TARGET-*`, `UBAG-TEMPLATE-*`, `UBAG-CACHE-*`, `UBAG-WEBHOOK-*`, `UBAG-INTERNAL-*`.

### 6.4 Idempotency semantics
- Client provides (or SDK auto-generates) a ULID `idempotency_key` per logical operation.
- Server stores `(app_id, idempotency_key) → job_id` for 24 h (configurable up to 30 d).
- Replays return the *original* job_id and result, with `idempotent_replay: true` and no new work performed.
- Replays with a *different* payload return `UBAG-VALIDATION-IDEMPOTENCY-CONFLICT`.

### 6.5 Versioning policy
- Date-based `api_version` in every request (server defaults if absent).
- Breaking changes ship under a new date; old versions supported ≥ 12 months.
- SDKs pin a default version per release and can override per call.
- Server returns `Ubag-Api-Version-Used` header for debugging.

---

## 7. Core Components (deep dive)

### 7.1 Edge / Ingress (Caddy 2)
- Automatic TLS via Let's Encrypt / ZeroSSL / internal CA.
- HTTP/3 (QUIC) by default — meaningful latency wins on poor networks (hospital Wi-Fi).
- Built-in rate limiting plugin per IP / per token bucket.
- ModSecurity-style WAF via Coraza plugin (OSS).
- Static config in `Caddyfile`, hot-reloadable.

### 7.2 API Gateway (Go)
- Single Go binary, ~15 MB, links Postgres + DragonflyDB + NATS clients.
- Routers: `chi` for HTTP, `grpc-go` for gRPC, `gorilla/websocket` (or `nhooyr/websocket`) for WS, `r3labs/sse` for Server-Sent Events.
- Connection pooling: `pgxpool` for Postgres, `redis/go-redis/v9` for Dragonfly, NATS native conn pool.
- Built-in OpenTelemetry instrumentation via `otelhttp` and `otelgrpc`.
- All handlers go through middleware chain: trace → recover → log → auth → rate limit → idempotency → validate → handle.

### 7.3 AuthN/AuthZ service
- Token kinds: `app_secret` (static), `app_jwt` (short-lived, derivable), `device_token` (bound to device fingerprint), `user_token` (OAuth2/OIDC), `service_account` (mTLS), `personal_access_token` (admin scope).
- Pluggable identity providers: built-in, OIDC (Authelia, Keycloak, Authentik, Google, Azure AD), SAML (for Tier 4).
- Authorization: RBAC + ABAC. Roles bundle scopes; ABAC adds attribute predicates (e.g. `tenant_id = client.tenant_id AND target IN allowed_targets`).
- Verifiable: every authZ decision is logged with `policy_hash` for audit reproducibility.

### 7.4 Tenant Registry
- Hierarchy: **Org → Project → App → Device → Session**.
- Each level has independent quotas, scopes, and audit streams.
- API key formats: `ubag_sk_<env>_<base58 256 bits>` with embedded checksum so leaks can be auto-revoked from GitHub via the standard secret-scanning partner program.

### 7.5 Command Validator
- JSON Schema (Draft 2020-12) per `command_type × api_version`.
- Schemas live in `packages/shared-schemas/` and are bundled into every SDK and the gateway.
- Validation rejects unknown fields by default (`additionalProperties: false`) to keep contracts tight.
- Custom validators per template (e.g. ICD-10 code validation for medical templates).

### 7.6 Job Orchestrator
- Implements the **Saga pattern** for multi-step workflows (job chains).
- Implements the **Transactional Outbox pattern**: writes job + outbox event in one PG transaction, a relay process pushes to NATS — guaranteeing no lost events.
- Priority lanes: `critical | high | normal | low | bulk` mapped to separate NATS subjects.
- Cron-style scheduling via `pg_cron` or built-in scheduler.
- Concurrency limits per (app, target) enforced via Redis token bucket + sliding window.

### 7.7 Prompt Template Engine
- **Jinja2** (Python) and **Pongo2** (Go) — same template syntax, render identically.
- Templates versioned in DB (`template_id@version`) and Git-syncable.
- Conditionals, loops, includes, macros, and a curated `safe` filter set.
- Built-in helpers: `medical.icd10()`, `units.convert()`, `dt.format()`, `pii.redact()`.
- Templates ship a JSON Schema for **input** and **output**; outputs are validated post-extraction.

### 7.8 Semantic Cache
- Per `(target, command_type, app_id, locale)`, prompts are hashed *and* embedded.
- Storage: `pgvector` (PG) with HNSW index. Approx O(log N) lookup.
- A request with cosine similarity ≥ `threshold` (default 0.97) and matching deterministic flags returns the cached result, marked `cached: true, cache_source: "semantic"`.
- Exact-hash cache (SHA-256 of canonicalized prompt + options) is checked first for instant returns.
- TTL configurable per template; PII templates default to **no cache**.
- Cache invalidation API: by `template_id`, `target`, or `tag`.
- Privacy: cache is partitioned by tenant; cross-tenant leakage is structurally impossible.

### 7.9 Webhook Dispatcher
- Outbound deliveries signed with **HMAC-SHA256** in `Ubag-Signature` header, with `Ubag-Timestamp` and 5-minute replay window.
- Retry schedule: 0, 30 s, 2 m, 10 m, 30 m, 2 h, 6 h, 12 h, 24 h (configurable). Stored in DB-backed queue.
- Per-endpoint circuit breaker (half-open after consecutive failures).
- DLQ surfaced in dashboard with one-click manual retry.

### 7.10 Browser Worker Fleet (see §13 for the deep dive)

### 7.11 Admin Dashboard (SvelteKit)
- PWA-installable, offline shell, < 100 KB initial JS.
- Pages: Apps, Devices, Targets, Adapters, Templates, Jobs, Failed Jobs, Webhooks, Cache, Sessions, Users, Audit, Settings, Metrics (embedded Grafana).
- Live Browser Viewer via **noVNC** over WebSocket (operator can take control if a CAPTCHA appears).

### 7.12 Local Sidecar Connector (Rust)
- Runs as Windows Service / launchd agent / systemd unit.
- Exposes `http://127.0.0.1:7878` REST + Unix domain socket on macOS/Linux + named pipe on Windows.
- Auto-update via signed releases (minisign).
- Encrypted device token in OS keychain (`keyring` crate).
- Offline queue (sled or redb) persists jobs when offline.
- Reconnect with exponential backoff + jitter.
- Single ~4 MB binary.

### 7.13 CLI (Go + Bubble Tea)
- Commands: `auth login`, `apps create`, `jobs send`, `jobs watch`, `targets list`, `templates render`, `cache purge`, `dashboard open`, `doctor` (diagnostics), `bench`.
- TUI mode: `ubag tui` opens a full-screen dashboard in the terminal — jobs, logs, metrics, all keyboard-driven.

### 7.14 Plugin & Extension System
- WASM-based plugin host (using **wasmtime** in Go, **wasmer** in Python worker).
- Plugin types: pre-job, post-job, pre-adapter, post-adapter, response-transformer, custom-command-type.
- Plugins are language-agnostic (any language that compiles to WASI), sandboxed (no FS/net by default), and capability-gated.
- Marketplace: a Git repo of community plugins, distributed as signed `.wasm` files.

---

## 8. Client SDK Strategy (11 first-class SDKs)

### 8.1 Schema-driven, never hand-written
- Source of truth: `openapi.yaml` (REST) + `*.proto` (gRPC) + shared JSON Schemas.
- Generated by a single pipeline: `make sdks` → outputs all 11 SDKs to `packages/sdk-*/`.
- Hand-written *ergonomics layer* per SDK on top of generated stubs (retries, idempotency, streaming, file uploads).
- Conformance test suite (~250 tests) runs against every SDK in CI — same assertions, every language.

### 8.2 SDK list

| # | Language / Runtime | Package name | Target ecosystems |
|---|---|---|---|
| 1 | **TypeScript / JavaScript** (Node 20+, Bun, Deno, browser) | `@ubag/sdk` | Electron, Tauri, web, extensions, Node servers |
| 2 | **Python 3.10+** (sync + async) | `ubag` | PySide, Tk, Django, FastAPI, scripts |
| 3 | **Go 1.21+** | `github.com/ubag/ubag-go` | Microservices, CLIs |
| 4 | **Rust 1.78+** (tokio + sync) | `ubag` (crates.io) | Tauri, embedded, perf-critical |
| 5 | **.NET 8** (C#, F#) | `Ubag.Sdk` (NuGet) | WPF, WinForms, MAUI, Avalonia, ASP.NET |
| 6 | **Java 17+** / **Kotlin** | `dev.ubag:ubag-sdk` (Maven) | JavaFX, Spring, Android |
| 7 | **Swift 5.9+** | `Ubag` (SwiftPM) | macOS, iOS, server-side Swift |
| 8 | **Ruby 3.2+** | `ubag` (RubyGems) | Rails, scripts |
| 9 | **PHP 8.2+** | `ubag/ubag-sdk` (Composer) | Laravel, WordPress plugins |
| 10 | **Dart / Flutter** | `ubag` (pub.dev) | Mobile + Flutter desktop |
| 11 | **Elixir** | `ubag` (Hex) | Phoenix, BEAM systems |

### 8.3 SDK feature parity matrix (all SDKs MUST support)
- Sync + async/await idioms native to each language.
- Auto-retry with exponential backoff + jitter (configurable).
- Idempotency key auto-generation.
- Streaming via WebSocket or SSE (callbacks/observables/async iterators).
- gRPC client (where ergonomic).
- File upload (multipart + chunked).
- Webhook signature verification helpers.
- Built-in OpenTelemetry tracing hooks.
- Local Sidecar auto-discovery (`http://127.0.0.1:7878` first, falls back to cloud).
- Offline queue (where the platform allows).
- Pluggable HTTP client (so users can swap in their own).

### 8.4 Example: TypeScript SDK (idiomatic, streaming, typed)

```ts
import { Ubag } from "@ubag/sdk";

const ubag = new Ubag({
  appId: "radiology-electron",
  appSecret: process.env.UBAG_SECRET!,
  // auto-discovers local sidecar; falls back to cloud
});

// Simple call
const result = await ubag.jobs.run({
  target: "deepseek_web",
  templateId: "radiology_ct_brain_v3",
  input: { age: 55, sex: "M", findings: "..." },
});

console.log(result.output.sections.impression);

// Streaming with async iterator
for await (const event of ubag.jobs.stream({ target: "deepseek_web", prompt: "..." })) {
  if (event.type === "token") process.stdout.write(event.text);
  if (event.type === "completed") break;
}
```

### 8.5 Example: Python SDK (sync + async)

```python
from ubag import Ubag

ubag = Ubag(app_id="...", app_secret="...")

# sync
result = ubag.jobs.run(target="deepseek_web", prompt="...")

# async streaming
import asyncio
from ubag.aio import Ubag as AsyncUbag

async def main():
    async with AsyncUbag(app_id="...", app_secret="...") as client:
        async for event in client.jobs.stream(target="deepseek_web", prompt="..."):
            print(event)

asyncio.run(main())
```

### 8.6 Example: Rust SDK

```rust
use ubag::{Client, JobRequest};

let client = Client::builder()
    .app_id("...").app_secret("...")
    .build()?;

let result = client.jobs().run(JobRequest::prompt("deepseek_web", "...")).await?;
println!("{}", result.output.text());
```

### 8.7 Conformance test suite
Every SDK runs the same JSON-defined test plan against a mock gateway: 250+ scenarios covering happy path, every error code, retries, idempotency, streaming, timeouts, large payloads, Unicode, malformed responses, webhook signature verification. A red conformance test blocks the release of that SDK.

---

## 9. Integration Methods (6 wire protocols, 5 patterns)

### 9.1 Wire protocols supported

| Protocol | Use when |
|---|---|
| **REST/JSON over HTTP/2 + HTTP/3** | Default for most clients |
| **WebSocket** | Streaming events, bidirectional |
| **Server-Sent Events (SSE)** | One-way streaming (browser-friendly, simpler than WS) |
| **gRPC + gRPC-Web** | Strongly typed clients, high throughput, polyglot servers |
| **MessagePack-RPC** (over WS or TCP) | Bandwidth-constrained or embedded clients |
| **MQTT 5** (optional plugin) | IoT/edge devices, intermittent connectivity |

### 9.2 Integration patterns

**Pattern A — Direct SDK / REST** (95% of cases)
```
App ──HTTPS──> Gateway ──> Worker ──> Result
```

**Pattern B — Streaming (WebSocket / SSE / gRPC server-stream)**
```
App <══WS/SSE══> Gateway <══ Events ══ Worker
```
Events: `queued` → `assigned` → `browser_opened` → `prompt_submitted` → `token` (repeated) → `completed | failed`.

**Pattern C — Local Sidecar Connector**
```
Legacy App ──HTTP localhost──> Sidecar ──WSS──> Gateway
```
Sidecar handles auth, retry, offline queue, encryption. Legacy app sees a trivial REST endpoint.

**Pattern D — CLI / subprocess bridge**
```
PowerShell / Bash / cron ──> `ubag jobs send ...` ──> stdout JSON
```

**Pattern E — Webhook callback (fire-and-forget)**
```
App ──POST──> Gateway   (returns 202 + job_id)
                ↓
              (work)
                ↓
Gateway ──POST signed──> App's webhook URL
```

**Pattern F — Workflow chains (sagas)**
A single submitted job can declare downstream jobs (templated DAG). E.g. *generate report → translate → render PDF → email*. Each step is a job; the orchestrator runs them with retries, compensations, and a single trace.

---

## 10. API Surface (v1)

### 10.1 Resources
- `/v1/jobs` — create, list, get, cancel, retry
- `/v1/jobs/{id}/events` — historical event log (paginated)
- `/v1/stream` — WebSocket (bi-directional)
- `/v1/sse/jobs/{id}` — SSE event stream for one job
- `/v1/workflows` — multi-step workflows
- `/v1/templates` — list, get, render (dry-run)
- `/v1/targets` — list, get adapter info, health
- `/v1/apps` — CRUD (admin)
- `/v1/devices` — register, list, revoke
- `/v1/webhooks` — endpoints, deliveries, retries
- `/v1/cache` — list, purge
- `/v1/audit` — query audit log
- `/v1/health`, `/v1/ready`, `/v1/version`, `/metrics`

### 10.2 OpenAPI 3.1 contract
Lives at `docs/openapi.yaml`. Single source of truth. Drives:
- SDK generation
- Postman collection / Bruno workspace
- Mock server (Prism)
- API documentation portal (Scalar / Redoc)
- Validation middleware in the gateway itself

### 10.3 gRPC contract (`proto/ubag/v1/*.proto`)
Same data model, generated from a shared IDL via `buf`. Server streaming and bidi streaming first-class.

### 10.4 GraphQL (optional, plugin)
A read-mostly GraphQL view over jobs, templates, targets — useful for dashboards. Not the primary API.

### 10.5 Pagination, filtering, sorting (consistent everywhere)
- Cursor pagination: `?cursor=...&limit=50` returning `next_cursor`.
- Field filtering: `?filter[status]=failed&filter[target]=deepseek_web`.
- Sorting: `?sort=-created_at`.
- Sparse fieldsets: `?fields=id,status,duration_ms`.
- Includes: `?include=events,result`.

### 10.6 Rate limiting
- Token bucket (per app, per token, per IP) — communicated via `RateLimit-Limit`, `RateLimit-Remaining`, `RateLimit-Reset` headers (IETF draft).
- 429 with `Retry-After`.
- SDKs honor 429 automatically.

---
## 11. Authentication & Security (defense in depth)

### 11.1 Authentication flows
| Flow | Use case | Token |
|---|---|---|
| **App secret** | First-time bootstrap, server-to-server | `Authorization: Bearer ubag_sk_prod_...` |
| **App JWT** | Short-lived (5 min), refreshable, derived from app secret | `Authorization: Bearer eyJ...` |
| **Device token** | Per-installed-app, bound to device fingerprint | `Authorization: Bearer ubag_dt_...` |
| **User OAuth2/OIDC** | Per-user access in multi-user apps | Standard OAuth2 |
| **mTLS** | High-assurance server clients (Tier 3+) | Client certificate |
| **Personal access token** | Admin / CLI usage | `ubag_pat_...` |

### 11.2 Authorization (RBAC + ABAC)
- **Roles:** `viewer`, `developer`, `operator`, `admin`, `superadmin` — assignable at org / project / app scopes.
- **Scopes:** `jobs:create`, `jobs:read`, `jobs:cancel`, `templates:read`, `templates:write`, `targets:use:<name>`, `webhooks:manage`, `audit:read`, `admin:*`.
- **ABAC predicates:** evaluated per request — e.g. `request.target in app.allowed_targets and request.cost_estimate <= remaining_quota`. Predicates compiled via **CEL** (Common Expression Language, Apache-2.0).
- **Policy bundles** can be loaded from filesystem, Git (with hash verification), or **Open Policy Agent** (Rego) for orgs that prefer it.

### 11.3 Secrets management
- App secrets and webhook secrets are stored as **argon2id** hashes (verification) plus AES-256-GCM encrypted plaintext (recovery, behind a master KEK).
- KEK is loaded from `age` keyfile, Vault, AWS KMS, or HSM (Tier 4).
- Rotation: `secrets rotate <id>` issues new secret, accepts both for a configurable grace period, then revokes.

### 11.4 Transport security
- TLS 1.3 only on public endpoints; HTTP/3 by default.
- HSTS preload, strict CSP on dashboard, secure cookies, SameSite=strict.
- mTLS between gateway ↔ workers ↔ database (Tier 3+).
- Tailscale/WireGuard mesh option for fully private deployments.

### 11.5 Data protection
- All PII fields in jobs are encrypted at rest with envelope encryption (per-tenant DEK, KEK in keystore).
- **HIPAA mode** turns on: full PII encryption, no semantic cache, no prompt logging beyond hashes, BAA-friendly audit trail, automatic data retention enforcement.
- **GDPR mode** adds: subject access export endpoint, right-to-erasure cascade, data residency tagging, configurable retention with hard cutoff.

### 11.6 Audit log (immutable, queryable)
- Every state-mutating call writes an event to an append-only audit table.
- Optional **merkle-chain** (each row contains hash of previous) — tamper-evident.
- Streaming export to S3 / SIEM (Splunk, Elastic, Wazuh, Graylog).
- Retention: configurable, default 1 year, max forever.

### 11.7 Supply chain
- Reproducible builds, SLSA-3 provenance.
- Containers signed with **cosign**, verified by Kyverno/Connaisseur admission controllers.
- Binaries signed with **minisign**.
- **SBOM** (SPDX, CycloneDX) shipped with every release.
- Dependencies pinned via lockfiles; renovate-bot for managed updates.
- `govulncheck`, `cargo audit`, `pip-audit`, `npm audit` run in CI; high-sev blocks release.

### 11.8 Threat model summary

| Threat | Mitigation |
|---|---|
| Credential leak in repo | Token format scannable by GitHub Secret Scanning partner program; auto-revoke |
| Replayed webhook | Signature + timestamp + 5-min window + nonce cache |
| DDoS on ingress | Caddy rate limit + WAF + circuit breakers + queue backpressure |
| Malicious adapter (plugin) | WASM sandbox, capability-gated, signed, reviewed |
| Target site changes | Drift detection (§13.7), synthetic monitoring, automatic adapter rollback |
| Browser fingerprinting / detection | Patchright, fingerprint randomization, residential proxies, humanized input |
| Insider tampering | Merkle-chained audit, RBAC, MFA, just-in-time admin elevation |
| Lost/stolen device | Device tokens revocable, geofencing + anomaly detection on `last_seen` |

---

## 12. Browser Session Strategy

### 12.1 Session lifecycle
```
   ┌──────────┐   warm    ┌──────────┐   assign   ┌──────────┐   release   ┌──────────┐
   │ idle     │──────────>│ ready    │───────────>│ in_use   │────────────>│ ready    │
   │ (paused) │           │          │            │ (job N)  │             │          │
   └──────────┘           └──────────┘            └────┬─────┘             └──────────┘
                                                       │ failure / timeout
                                                       ▼
                                                  ┌──────────┐
                                                  │ quarantine│ → diagnose → re-warm or kill
                                                  └──────────┘
```

### 12.2 Session pools
- Per `(target, tenant, profile_class)`.
- **Warm pool**: configurable N sessions kept logged-in, idle, page on chat landing — drastically reduces p95 (no cold-start: open browser → login → wait → submit).
- **Burst pool**: spins up additional sessions on queue depth threshold.
- **Cooldown**: between jobs, an optional human-like idle window (5–20 s, randomized).
- **Recycle policy**: after N jobs or M hours, session is recycled to keep memory clean and rotate fingerprints.

### 12.3 Per-session metadata
```
session_id, target, profile_dir, login_state, fingerprint_id, proxy_id,
assigned_apps[], current_job_id, jobs_completed, last_health_ok,
created_at, last_used_at, recycle_at, quarantine_reason
```

### 12.4 Persistent profiles
- Stored under `var/profiles/<target>/<profile_id>/` (filesystem) — Chromium `--user-data-dir`.
- Encrypted-at-rest option: profile dirs reside inside an encrypted volume (LUKS / fscrypt / dm-crypt).
- Backup: nightly tarball to object storage; restore in one command.

### 12.5 Manual login & re-login
- When `login_state == logged_out`, a session enters `awaiting_login`.
- The dashboard shows a **Live Login** card: operator clicks "Take control" → **noVNC** opens → operator completes login (including 2FA, magic links) → marks "Login complete" → session returns to pool.
- The same UI handles unexpected CAPTCHAs mid-job.
- Headed-mode workers can also be exposed via direct VNC for power users.

### 12.6 Concurrency model
- One job per session at a time (preserves conversational state and avoids race conditions in single-tab chats).
- Multiple sessions can run in parallel within one worker process (separate browser contexts).
- Multiple workers can run on one host (CPU-bound limit ~ 1 worker per 2 cores for browser-heavy work).

---

## 13. Browser Worker Engine (deep dive)

### 13.1 Engine: Playwright + Patchright
- **Patchright** is a patched fork of Playwright that removes the canonical CDP fingerprints (e.g. `navigator.webdriver`, runtime evaluation traces, `Runtime.enable` leak) that bot-detection scripts look for.
- **Why not Puppeteer?** Playwright has better multi-context isolation, auto-waiting selectors, and a more stable trace viewer.
- **Why not Selenium?** Higher latency, weaker isolation, less mature TS/Python APIs.

### 13.2 Stealth layers (composable)
1. **Patchright** — defeats the obvious detectors.
2. **Realistic fingerprints** — generated by `fingerprint-suite` (MIT), consistent navigator/screen/canvas/WebGL/audio across the session.
3. **Humanized interaction** — `playwright-extra`-style mouse paths (Bézier with jitter), variable typing speed with realistic typos & corrections in scratch fields, scroll easing.
4. **Network shaping** — emulate target geo's bandwidth/latency; consistent timezone, language, geolocation, locale.
5. **Resource filtering** — block ads/trackers/analytics for speed *and* reduced fingerprint surface; allow-list per adapter.
6. **Proxy rotation** — optional residential pool per session, with sticky-session per conversation.
7. **TLS fingerprint** — Chromium's native TLS used unmodified (modifying it usually backfires); proxy-side ja3/ja4 rotation if needed.

### 13.3 Adapter SDK
Adapters are isolated Python modules implementing this interface:

```python
class TargetAdapter(Protocol):
    name: str                       # "deepseek_web"
    version: str                    # semver "1.7.3"
    supported_command_types: list[str]
    homepage_url: str
    requires_login: bool
    capabilities: AdapterCapabilities  # streaming, file_upload, conversation_memory, ...

    async def health_check(self, page: Page) -> HealthReport: ...
    async def ensure_logged_in(self, page: Page, ctx: SessionCtx) -> LoginState: ...
    async def open_new_conversation(self, page: Page) -> ConversationHandle: ...
    async def resume_conversation(self, page: Page, conv_id: str) -> ConversationHandle: ...
    async def submit_prompt(self, page: Page, prompt: str, files: list[FileRef] = []) -> SubmitReceipt: ...
    async def stream_tokens(self, page: Page) -> AsyncIterator[Token]: ...
    async def wait_for_completion(self, page: Page, timeout_s: int) -> None: ...
    async def extract_output(self, page: Page) -> RawOutput: ...
    async def normalize_output(self, raw: RawOutput) -> NormalizedOutput: ...
    async def on_error(self, page: Page, err: Exception) -> AdapterErrorHint: ...
    async def teardown(self, page: Page) -> None: ...
```

### 13.4 Selector strategy (resilient to UI churn)
Adapters declare **selector strategies** in priority order — gateway tries each in turn:

```python
SELECTORS = {
    "submit_button": [
        Strategy("test_id", "send-button"),
        Strategy("aria_label", "Send message"),
        Strategy("role", role="button", name=re.compile(r"send", re.I)),
        Strategy("css", "button[type=submit]"),
        Strategy("ml", model="submit_button_v3"),  # last-resort vision model
    ],
    ...
}
```

The optional **ML last-resort** uses a small open-source vision model (e.g. **OmniParser-2** weights, or distilled internal model) to locate UI elements when all CSS/ARIA selectors fail. Results are reported as drift events so adapters can be patched.

### 13.5 Adapter versioning & blue-green
- Each adapter is `name@version`. The active version per target is stored in DB.
- Deploy new version → it runs on a small `canary_percent` of jobs → metrics compared to current → auto-promote or auto-rollback.
- Rolling forward and rolling back are both single-row DB updates.

### 13.6 Built-in adapters (Day 1)
- `deepseek_web`
- `claude_web`
- `chatgpt_web`
- `gemini_web`
- `mistral_lechat`
- `perplexity_web`
- `generic_chat` — config-driven (selectors + URLs in YAML), covers ~80% of simple chat sites with no code.
- `generic_form` — for arbitrary form-fill tasks.
- `mock` — for testing.

### 13.7 Drift detection
- **DOM snapshot diffing**: every session takes structural snapshots (tag/attr tree, no text) of key UI states; compared to baseline; significant drift opens a ticket.
- **Synthetic monitoring**: scheduled canary jobs per adapter; failures → page on-call.
- **Anomaly metrics**: success rate, p50/p99 duration, output length distribution per adapter — outliers alert.
- **Auto-quarantine**: if drift score crosses threshold, the adapter is automatically downgraded to the previous version and the team is notified.

### 13.8 Recording & replay
- Every job optionally captures: full HAR, screencast (WebM, off by default), DOM snapshots at key steps, console/network logs.
- Stored in MinIO/Garage, retention configurable.
- Dashboard "Time-travel debug" lets you scrub through any failed job — see the exact frame where the click missed.

### 13.9 Resource governance
- Per-worker hard limits: max RSS, max CPU, max open contexts, max session lifetime.
- Workers self-restart when limits approached (drains cleanly, no in-flight job loss).
- cgroup v2 limits on Linux; Job objects on Windows.

---

## 14. Job Orchestration

### 14.1 Job states
```
created → queued → assigned → running → (token-streaming) → completing → completed
                            ↘ failed (retryable) → queued (retry)
                            ↘ failed (terminal)  → dead_letter
                            ↘ cancelled
                            ↘ timed_out
```

### 14.2 Retry policy
- Default exponential backoff with full jitter: `min(2^n * 1s, 60s) ± 30%`.
- Configurable per `command_type`, per `template_id`, per app.
- Categorized retries: transient (network, 5xx, target busy) — retried; validation/quota/permanent — not retried.
- Max retries default 3; configurable to 10.
- After exhaustion → DLQ, webhook fired, dashboard alert.

### 14.3 Multi-step Workflows (sagas)
```yaml
workflow_id: oet_letter_full_pipeline
steps:
  - id: draft
    target: deepseek_web
    template: oet_letter_draft_v2
    input_ref: $.workflow.input
  - id: critique
    target: claude_web
    template: oet_letter_critique_v1
    input:
      letter: "{{steps.draft.output.text}}"
  - id: revise
    target: deepseek_web
    template: oet_letter_revise_v1
    input:
      letter: "{{steps.draft.output.text}}"
      critique: "{{steps.critique.output.text}}"
  - id: render_pdf
    command_type: render_pdf
    input:
      markdown: "{{steps.revise.output.markdown}}"
on_failure:
  policy: compensate
  compensations:
    - id: notify_failure
      command_type: webhook
      url: "{{workflow.input.fallback_webhook}}"
```
- DAG executor; per-step retries; per-workflow timeout; partial results returned on early termination.
- Conditional steps via CEL expressions (`when: steps.critique.output.score > 8`).

### 14.4 Priority lanes
- 5 lanes mapped to NATS subjects: `jobs.crit`, `jobs.high`, `jobs.norm`, `jobs.low`, `jobs.bulk`.
- Workers prefer higher lanes; weighted fair queuing prevents starvation.
- Per-tenant lane caps (e.g. tenant X can use at most 2 critical workers concurrently).

### 14.5 Scheduling
- One-off `not_before: <RFC3339>` for delayed jobs.
- Cron-style recurring jobs (`pg_cron` integration).
- Calendar-aware (skip weekends, business hours only) via plugin.

### 14.6 Cancellation
- Cooperative: worker checks cancel token between steps; signals adapter to abort.
- Hard cancel kills the browser context after grace period.

### 14.7 Backpressure & flow control
- Queue depth metric drives admission control at the gateway. Beyond `max_queue_depth`, gateway returns 429 with `Retry-After` instead of accepting more work.
- Per-tenant fair share prevents one noisy app from starving others.

### 14.8 Exactly-once-ish guarantee
- Transactional outbox writes job + outbox event in one PG tx.
- NATS JetStream "exactly once" with message dedup window.
- Adapter side effects are designed idempotent (resume conversation if a job re-runs).

---

## 15. Prompt Template Engine

### 15.1 Template structure
```
templates/radiology/ct_brain_v3/
├── template.yaml          # metadata, version, input/output schemas, target hints
├── prompt.jinja2          # the actual prompt
├── input.schema.json
├── output.schema.json
├── tests/
│   ├── basic.json
│   ├── edge_no_findings.json
│   └── pii_redaction.json
└── README.md
```

### 15.2 Features
- **Inheritance:** templates can extend a base template.
- **Includes & macros:** reusable snippets (e.g. `_disclaimer.jinja2`).
- **Conditional sections:** `{% if patient.age < 18 %}pediatric guidance{% endif %}`.
- **Multi-locale:** auto-translate prompts via lookup tables or template variants.
- **Safety filters:** built-in `pii.redact()`, `medical.dose_check()`, `forbidden_phrases.scan()`.
- **A/B testing:** `template_id: foo` with two active versions split by ratio; metrics tracked.
- **Output validation:** rendered output passed through `output.schema.json`; failure triggers retry with stricter instructions.

### 15.3 Template registry
- Stored in Postgres; Git-syncable via webhook from a templates repo.
- Versioned; rollbacks are atomic.
- Each version captures a hash of all dependencies (includes, schemas).

### 15.4 Render API
- `POST /v1/templates/{id}/render` returns the rendered prompt without dispatching a job (great for testing).
- SDK helpers: `client.templates.render(id, input)`.

### 15.5 Built-in starter templates
- Radiology reports (CT/MR/US/X-ray, multiple regions).
- OET letters (referral, discharge, transfer).
- SOAP notes.
- Document summarization.
- Email rewrite (formal/informal).
- Code review.
- Translation.
- Structured extraction.

---

## 16. Response Normalization & Output Validation

### 16.1 The problem
Web AI targets return text formatted however the model felt like. Apps need predictable structure: *findings*, *impression*, *plain text for EMR*, *Markdown for UI*, *HTML for email*.

### 16.2 Normalization pipeline
```
raw_text
  → text_cleanup (collapse whitespace, fix encoding, strip artifacts)
  → format_detect (markdown? plaintext? mixed?)
  → section_parse (template-defined patterns, with fallback heuristics)
  → schema_validate (against output.schema.json)
  → on failure → retry_with_critique (auto-asks model to fix structure)
  → output{text, markdown, plain_text, sections, html}
```

### 16.3 Built-in renderers
- Markdown → HTML (sanitized via Bleach/DOMPurify rules).
- Markdown → DOCX (via Pandoc-WASM bundled with worker).
- Markdown → PDF (via Typst, OSS, 30 MB binary, beautiful output).
- Sections → custom JSON shape declared by template.

### 16.4 Output guards
- Forbidden-phrase scanner (e.g. "I cannot help with that") → triggers retry.
- Length sanity (too short / too long vs template expectations) → flag or retry.
- Hallucination heuristics (e.g. dates in the future, impossible doses) — optional template guards.

---

## 17. Caching Strategy

### 17.1 Cache tiers
1. **L0 — in-process LRU** (Ristretto / lru-cache) — hot template renders, schema parses.
2. **L1 — DragonflyDB / Valkey** — shared across gateway replicas; key TTLs in seconds–minutes.
3. **L2 — Postgres (pgvector + JSON)** — semantic cache for completed job outputs, days–months.
4. **L3 — Object storage** — large artifacts (PDFs, recordings) addressed by content hash.

### 17.2 Cache keys
- Exact: `sha256(canonical(template_id, input, target, options))`.
- Semantic: embedding vector + filter (template_id, target, locale, app_id).

### 17.3 Cache safety
- Per-tenant partitioning; cross-tenant reads structurally impossible.
- HIPAA mode disables L2/L3 caches for PII templates.
- "No-cache" header / option respected.
- Cache poison protection: writes must come from successful, validated jobs.

### 17.4 Invalidation
- By tag, template, target, or wildcard.
- Time-based TTL per template.
- Adapter version bump auto-invalidates affected entries.

---
## 18. Observability (logs, metrics, traces, profiles)

### 18.1 Logs
- **Structured JSON** on stdout (12-factor); Vector or Promtail ships to Loki.
- Fields: `ts, level, service, trace_id, span_id, app_id, tenant_id, job_id, target, adapter, msg, ...`.
- PII redaction filter applied before emission (configurable patterns).
- Log levels per-service, hot-reloadable via SIGHUP or admin API.

### 18.2 Metrics (Prometheus)
- **RED** (Rate, Errors, Duration) per endpoint, per command_type, per target, per adapter.
- **USE** (Utilization, Saturation, Errors) per resource (workers, sessions, queue, DB pool).
- Cardinality budget per service enforced — no per-job-id labels.
- Pre-built dashboards shipped in `infra/grafana/dashboards/`:
  - Gateway latency & errors
  - Queue depth & flow
  - Worker fleet health
  - Per-target adapter performance
  - Per-tenant usage
  - Webhook delivery
  - Cache hit rates
  - Browser session pool

### 18.3 Traces (OpenTelemetry)
- W3C tracecontext propagated through SDK → gateway → orchestrator → worker → adapter (down to individual selector calls).
- Spans: `gateway.request`, `auth.verify`, `validate.command`, `queue.enqueue`, `worker.assign`, `adapter.submit_prompt`, `adapter.wait`, `adapter.extract`, `normalize.parse`, etc.
- Trace exporter: OTLP/gRPC → Tempo (or Jaeger/Honeycomb-OSS/Grafana Cloud).
- Trace sampling: 100% on errors, 10% normal (tunable).

### 18.4 Continuous profiling
- **Pyroscope** sidecar (or Parca). Flame graphs in production, no perceptible overhead.
- CPU, alloc, lock contention, goroutine count for Go services; CPU + memory for Python workers.

### 18.5 Error aggregation
- **GlitchTip** (OSS, Sentry-compatible SDK) receives unhandled exceptions and selected handled ones.
- Release tagging from `goreleaser` for stack-trace symbolication.

### 18.6 Synthetic monitoring (built-in)
- Per target, a small set of canary prompts runs every N minutes.
- Failure budget per target: e.g. 3 of last 10 → page on-call; 1 of last 50 → ticket.
- Synthetic results feed into the adapter health score.

### 18.7 SLOs (default targets)
| SLO | Target |
|---|---|
| Gateway availability (non-browser path) | 99.95% |
| Job acceptance latency p99 | < 200 ms |
| Browser job p50 (warm session) | < 15 s |
| Browser job p99 (warm session) | < 60 s |
| Webhook delivery success (eventually) | 99.99% within 24 h |
| Idempotent replay correctness | 100% |

---

## 19. Performance Engineering

### 19.1 Hot-path budget (target)
- TLS handshake: 1 RTT via 0-RTT QUIC where possible.
- Gateway middleware chain: < 5 ms total (auth cache hit, validate, idempotency check).
- Postgres job insert: < 10 ms (single row, partitioned table, prepared statement).
- NATS publish: < 2 ms.
- Total `/v1/jobs` p99 with cache hit: **< 50 ms**.
- Total `/v1/jobs` p99 cache miss: **< 100 ms** (excludes browser work).

### 19.2 Techniques
- **Prepared statement pool** in pgx; reusable across requests.
- **Connection pools sized to CPU × 2** for DB; monitored for saturation.
- **HTTP/2 multiplexing + HTTP/3** with `quic-go`.
- **Compression**: zstd preferred, gzip fallback. Brotli for HTML.
- **MessagePack** SDK option for ~30% smaller payloads vs JSON.
- **Vectorized JSON parsing**: `jsoniter` (Go), `orjson` (Python), `simdjson` where appropriate.
- **Batch endpoints**: `POST /v1/jobs/batch` for up to 100 jobs in one round trip.
- **Pre-warm browser pool** removes 1–3 s of cold start per job.
- **Adapter prompt prep** runs while waiting for browser navigation.
- **Speculative execution**: when a streaming target is detected mid-response, start parsing partial output.
- **Tail-call avoidance** in Python hot paths; use `__slots__` for frequently-allocated classes.

### 19.3 Memory engineering
- Browser worker target: < 300 MB per active session.
- Resource interception drops images/fonts/analytics by default — typically 60% bandwidth saved.
- Garbage browser tabs killed proactively (DOM nodes > threshold, RSS > threshold).
- Profile dirs cleaned of caches > 24 h old on session recycle.

### 19.4 Network engineering
- **Cloudflare-style edge caching** for static SDK + dashboard assets when self-hosted CDN is desired (Caddy + Varnish optional).
- **DNS pre-resolve** for target sites at worker startup.
- **Connection reuse** to NATS, DB, object storage via long-lived clients.

### 19.5 Benchmarks (publish these; CI gates regressions)
| Workload | Target |
|---|---|
| `POST /v1/jobs` empty queue, cache hit | 10k RPS / instance |
| `POST /v1/jobs` cache miss (enqueue only) | 5k RPS / instance |
| Webhook delivery throughput | 2k/s / dispatcher |
| Concurrent browser sessions / worker | 50 (idle), 15 (active) |
| End-to-end p50 with warm session (DeepSeek prompt) | 12 s |

### 19.6 Edge optimization
- Edge tier (`edge` profile) runs gateway + worker + sqlite in a single binary with shared memory queue — sub-millisecond enqueue.
- Suitable for on-prem hospital workstations where outbound internet is restricted and a local browser session is the entire system.

---

## 20. Stability & Reliability

### 20.1 Failure modes mapped
| Failure | Detection | Response |
|---|---|---|
| Target site down | Synthetic monitor + per-adapter error spike | Pause adapter, fail open with `503 + Retry-After`, alert |
| Target UI changed | Drift detection (DOM diff) + selector fallback exhausted | Auto-rollback adapter, file ticket |
| Browser crash | Worker watchdog + heartbeat | Kill session, restart, retry job |
| Worker OOM | cgroup OOM + Prometheus alert | Drain & restart worker, jobs requeue |
| DB primary down | pg_stat + healthcheck | Promote replica (managed by CloudNativePG operator), gateway buffers writes via NATS |
| Queue lag | NATS consumer lag metric | Auto-scale workers (k8s HPA on `nats_consumer_pending`) |
| Webhook target down | Per-endpoint circuit breaker | Backoff, DLQ after retries exhausted |
| Cache stampede | Singleflight + jittered TTLs | Coalesce concurrent identical requests |
| Token bucket exhaustion (target side) | 429s from target | Adaptive throttle, increase inter-request delay |
| Captcha | DOM detect | Move session to manual-resolution queue |

### 20.2 Circuit breakers (resilience4j-style, OSS impls)
- Per adapter, per webhook endpoint, per upstream dependency.
- States: closed → open → half-open with success-budget recovery.
- Metrics + dashboard panels.

### 20.3 Bulkheads
- Per-target worker quota.
- Per-tenant connection pool.
- One bad tenant or target cannot exhaust the others.

### 20.4 Graceful shutdown
- SIGTERM → stop accepting new work → finish in-flight (bounded grace) → ack queue messages → close connections → exit.
- Kubernetes `preStop` hook + PDBs ensure no in-flight loss during rollouts.

### 20.5 Chaos testing
- `chaos` toolkit integration. Game-day scripts:
  - Kill random worker every 30 s.
  - Inject 500 ms latency on DB.
  - Drop 5% of NATS messages.
  - Force target adapter to return malformed output.
- Run on staging weekly; CI runs a 60-second chaos suite per PR.

### 20.6 Data durability
- Postgres synchronous replication for Tier 3+.
- WAL archived to object storage (continuous backup).
- Point-in-time recovery tested monthly.
- Object storage cross-region replication for Tier 4.

### 20.7 Disaster recovery runbook (shipped)
- RPO target: 5 minutes.
- RTO target: 30 minutes.
- Step-by-step restore commands; tested quarterly by automation.

---

## 21. Storage Strategy

### 21.1 Postgres (primary OLTP)
- Schema versioned via **sqlc** (Go) + **Alembic** (Python) — single source of truth in `migrations/`.
- Tables partitioned where they grow large (`automation_jobs` by month).
- `pg_partman` automates partition lifecycle.
- `pg_stat_statements`, `auto_explain` enabled in non-prod.
- `pg_cron` schedules: cache TTL eviction, partition rotation, retention enforcement, backup tag.

### 21.2 SQLite (edge tier)
- WAL mode, `synchronous=NORMAL`, `mmap_size=1GB`.
- **Litestream** for continuous replication to S3-compatible storage when available.
- Same schema as Postgres via Alembic dialect (kept compatible by CI).

### 21.3 Cache: DragonflyDB / Valkey
- BSD/Apache-licensed, Redis wire-compatible.
- Multi-core scaling, no Lua snowflakes.
- Cluster mode for Tier 3+.
- Persistence configurable (none, RDB, AOF).

### 21.4 Queue: NATS JetStream
- Single 12 MB binary; clusters easily.
- Subjects per tenant (`jobs.tenant.<id>.<priority>`).
- Configurable retention, dedup window, ack policies.
- For `edge` profile: **River** (Go, Postgres-backed) — zero extra services.

### 21.5 Object storage: MinIO / Garage
- S3-compatible API.
- Garage's strength: no metadata DB, geo-replicated, < 100 MB binary, perfect for Tier 4.
- Bucket layout: `screenshots/`, `recordings/`, `artifacts/`, `templates/`, `backups/`.
- Lifecycle rules per bucket (e.g. recordings older than 30 d → cold tier or delete).

### 21.6 Search: Meilisearch / Tantivy
- Indexed: jobs (subject, error_message, app, tags), templates (name, content), audit log.
- Sub-50 ms full-text search powering the dashboard.

### 21.7 Analytics (optional Tier 3+): ClickHouse
- Append-only event ingest from NATS.
- Per-tenant usage rollups, cost reports, billing exports.

---

## 22. Revised Database Schema (DDL-ready, partitioned, indexed)

```sql
-- Identity
CREATE TABLE tenants (
  id              BIGSERIAL PRIMARY KEY,
  external_id     TEXT UNIQUE NOT NULL,
  name            TEXT NOT NULL,
  plan            TEXT NOT NULL DEFAULT 'free',
  data_region     TEXT NOT NULL DEFAULT 'default',
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at      TIMESTAMPTZ
);

CREATE TABLE projects (
  id              BIGSERIAL PRIMARY KEY,
  tenant_id       BIGINT NOT NULL REFERENCES tenants(id),
  external_id     TEXT NOT NULL,
  name            TEXT NOT NULL,
  environment     TEXT NOT NULL CHECK (environment IN ('dev','staging','prod')),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, external_id)
);

CREATE TABLE apps (
  id              BIGSERIAL PRIMARY KEY,
  project_id      BIGINT NOT NULL REFERENCES projects(id),
  app_id          TEXT UNIQUE NOT NULL,
  app_name        TEXT NOT NULL,
  platform_types  TEXT[] NOT NULL DEFAULT '{}',
  status          TEXT NOT NULL DEFAULT 'enabled',
  metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE app_credentials (
  id              BIGSERIAL PRIMARY KEY,
  app_id          BIGINT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  kind            TEXT NOT NULL,  -- 'app_secret' | 'jwt_signing' | 'webhook_secret'
  secret_prefix   TEXT NOT NULL,  -- e.g. 'ubag_sk_prod_AbCd' (for identification)
  secret_hash     TEXT NOT NULL,  -- argon2id
  secret_ciphertext BYTEA,        -- AES-256-GCM, optional recovery
  scopes          TEXT[] NOT NULL DEFAULT '{}',
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at      TIMESTAMPTZ,
  revoked_at      TIMESTAMPTZ
);

CREATE TABLE devices (
  id              BIGSERIAL PRIMARY KEY,
  app_id          BIGINT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  device_id       TEXT NOT NULL,
  device_name     TEXT,
  os              TEXT,
  app_version     TEXT,
  fingerprint_hash TEXT,
  last_seen_at    TIMESTAMPTZ,
  revoked_at      TIMESTAMPTZ,
  UNIQUE (app_id, device_id)
);

-- Targets and adapters
CREATE TABLE targets (
  id              BIGSERIAL PRIMARY KEY,
  name            TEXT UNIQUE NOT NULL,
  display_name    TEXT NOT NULL,
  category        TEXT NOT NULL,
  homepage_url    TEXT,
  enabled         BOOLEAN NOT NULL DEFAULT TRUE,
  requires_login  BOOLEAN NOT NULL DEFAULT TRUE,
  capabilities    JSONB NOT NULL DEFAULT '{}'::jsonb,
  metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE adapters (
  id              BIGSERIAL PRIMARY KEY,
  target_id       BIGINT NOT NULL REFERENCES targets(id),
  version         TEXT NOT NULL,
  module_path     TEXT NOT NULL,
  manifest        JSONB NOT NULL,
  is_active       BOOLEAN NOT NULL DEFAULT FALSE,
  canary_percent  INT NOT NULL DEFAULT 0,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (target_id, version)
);

CREATE TABLE app_target_permissions (
  id              BIGSERIAL PRIMARY KEY,
  app_id          BIGINT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  target_id       BIGINT NOT NULL REFERENCES targets(id),
  allowed_command_types TEXT[] NOT NULL DEFAULT '{}',
  rate_limit_per_minute INT NOT NULL DEFAULT 60,
  daily_quota     INT,
  UNIQUE (app_id, target_id)
);

-- Templates
CREATE TABLE prompt_templates (
  id              BIGSERIAL PRIMARY KEY,
  template_id     TEXT NOT NULL,
  version         TEXT NOT NULL,
  content         TEXT NOT NULL,
  input_schema    JSONB NOT NULL,
  output_schema   JSONB,
  metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
  is_active       BOOLEAN NOT NULL DEFAULT FALSE,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (template_id, version)
);

-- Jobs (partitioned monthly)
CREATE TABLE automation_jobs (
  id              BIGINT NOT NULL,
  job_id          TEXT NOT NULL,
  tenant_id       BIGINT NOT NULL,
  app_id          BIGINT NOT NULL,
  device_id       BIGINT,
  user_ref        TEXT,
  target_id       BIGINT NOT NULL,
  adapter_version TEXT,
  command_type    TEXT NOT NULL,
  template_id     TEXT,
  template_version TEXT,
  conversation_id TEXT,
  idempotency_key TEXT,
  priority        TEXT NOT NULL DEFAULT 'normal',
  status          TEXT NOT NULL,
  input           JSONB NOT NULL,
  output          JSONB,
  error_code      TEXT,
  error_message   TEXT,
  retries         INT NOT NULL DEFAULT 0,
  trace_id        TEXT,
  cache_hit       BOOLEAN NOT NULL DEFAULT FALSE,
  cost_credits    NUMERIC(12,4),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  queued_at       TIMESTAMPTZ,
  started_at      TIMESTAMPTZ,
  completed_at    TIMESTAMPTZ,
  PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX ON automation_jobs (app_id, created_at DESC);
CREATE INDEX ON automation_jobs (tenant_id, status, created_at DESC);
CREATE INDEX ON automation_jobs (job_id);
CREATE UNIQUE INDEX ON automation_jobs (app_id, idempotency_key) WHERE idempotency_key IS NOT NULL;

CREATE TABLE automation_job_events (
  id              BIGSERIAL,
  job_id          TEXT NOT NULL,
  seq             INT NOT NULL,
  event_type      TEXT NOT NULL,
  message         TEXT,
  metadata        JSONB,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
) PARTITION BY RANGE (created_at);

CREATE INDEX ON automation_job_events (job_id, seq);

-- Webhooks
CREATE TABLE webhook_endpoints (
  id              BIGSERIAL PRIMARY KEY,
  app_id          BIGINT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  url             TEXT NOT NULL,
  events          TEXT[] NOT NULL DEFAULT '{*}',
  secret_id       BIGINT REFERENCES app_credentials(id),
  enabled         BOOLEAN NOT NULL DEFAULT TRUE,
  circuit_state   TEXT NOT NULL DEFAULT 'closed',
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE webhook_deliveries (
  id              BIGSERIAL PRIMARY KEY,
  endpoint_id     BIGINT NOT NULL REFERENCES webhook_endpoints(id),
  job_id          TEXT,
  event_type      TEXT NOT NULL,
  payload         JSONB NOT NULL,
  attempt         INT NOT NULL DEFAULT 0,
  status          TEXT NOT NULL DEFAULT 'pending',
  http_status     INT,
  response_body   TEXT,
  next_attempt_at TIMESTAMPTZ,
  delivered_at    TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Sessions
CREATE TABLE browser_sessions (
  id              BIGSERIAL PRIMARY KEY,
  session_id      TEXT UNIQUE NOT NULL,
  target_id       BIGINT NOT NULL REFERENCES targets(id),
  worker_id       TEXT NOT NULL,
  profile_dir     TEXT NOT NULL,
  state           TEXT NOT NULL,
  login_state     TEXT NOT NULL,
  current_job_id  TEXT,
  jobs_completed  INT NOT NULL DEFAULT 0,
  last_health_at  TIMESTAMPTZ,
  recycle_at      TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Semantic cache
CREATE EXTENSION IF NOT EXISTS vector;
CREATE TABLE semantic_cache (
  id              BIGSERIAL PRIMARY KEY,
  tenant_id       BIGINT NOT NULL,
  target_id       BIGINT NOT NULL,
  template_id     TEXT,
  prompt_hash     TEXT NOT NULL,
  prompt_embedding vector(384),
  output          JSONB NOT NULL,
  hits            INT NOT NULL DEFAULT 0,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at      TIMESTAMPTZ
);
CREATE INDEX ON semantic_cache USING hnsw (prompt_embedding vector_cosine_ops);
CREATE INDEX ON semantic_cache (tenant_id, target_id, template_id, prompt_hash);

-- Audit (append-only, merkle-chained)
CREATE TABLE audit_log (
  id              BIGSERIAL PRIMARY KEY,
  ts              TIMESTAMPTZ NOT NULL DEFAULT now(),
  actor_kind      TEXT NOT NULL,
  actor_id        TEXT NOT NULL,
  tenant_id       BIGINT,
  action          TEXT NOT NULL,
  resource_kind   TEXT NOT NULL,
  resource_id     TEXT,
  request         JSONB,
  result          TEXT NOT NULL,
  prev_hash       BYTEA,
  this_hash       BYTEA NOT NULL
);
CREATE INDEX ON audit_log (tenant_id, ts DESC);

-- Outbox for reliable event emission
CREATE TABLE outbox_events (
  id              BIGSERIAL PRIMARY KEY,
  topic           TEXT NOT NULL,
  payload         JSONB NOT NULL,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  published_at    TIMESTAMPTZ
);
CREATE INDEX ON outbox_events (published_at NULLS FIRST, id);
```

---

## 23. Local Sidecar Connector (Rust, deep)

### 23.1 Why a sidecar
- Legacy apps that cannot bundle modern HTTPS/auth libraries.
- Apps that must work offline (queue locally, sync on reconnect).
- Single point of credential storage on a workstation, so end users never see API keys.
- Centralized place to enforce local rate limits, PII redaction, encryption.

### 23.2 Local API
- `POST http://127.0.0.1:7878/v1/jobs` — same shape as gateway.
- `GET  http://127.0.0.1:7878/v1/jobs/{id}` — status.
- `WS   ws://127.0.0.1:7878/v1/stream` — live events.
- `GET  http://127.0.0.1:7878/v1/health` — diagnostics.
- `POST http://127.0.0.1:7878/v1/login` — UI for users to re-auth.

### 23.3 Security
- Loopback-only by default. Optional Unix-domain-socket / named pipe (preferred where IPC permissions matter).
- mTLS for non-loopback bindings.
- Per-process token gating (Windows: token integrity check; Linux: peer cred via SO_PEERCRED).
- Credentials encrypted with OS keychain (Windows Credential Manager / macOS Keychain / libsecret).

### 23.4 Offline queue
- Local **redb** or **sled** store; jobs persisted with TTL.
- On reconnect, sidecar replays in order with idempotency keys.
- Disk encrypted (`fs-crypt` or app-level age envelope).

### 23.5 Lifecycle
- **Windows service** (via `windows-service` crate), auto-start.
- **macOS launchd agent** at `~/Library/LaunchAgents/dev.ubag.sidecar.plist`.
- **Linux systemd** user unit.
- **Auto-update**: pulls signed releases from a configurable URL; verifies minisign signature; atomic swap; rollback on failure to start.

### 23.6 Size
- Static MUSL build on Linux, Rust on Windows/macOS. Target: < 5 MB.

---

## 24. Admin Dashboard

### 24.1 Tech
- **SvelteKit** (smallest hydration bundle), **Skeleton UI**, **Tailwind CSS**, **Lucide icons**.
- TanStack Table for data grids; Chart.js for time-series; **xterm.js** for live worker shells; **noVNC** for browser viewing.
- Single binary serve via Caddy; or run as a separate service.

### 24.2 IA
- Overview (live job stream, queue depth, error rate)
- **Apps**: create/edit, rotate secrets, view usage, set scopes
- **Devices**: list, revoke, see fingerprints
- **Users & Roles**: invitations, SSO, MFA enrollment
- **Targets**: enable/disable, health, last drift, manage adapters
- **Adapters**: versions, canary controls, drift logs, roll back
- **Templates**: versions, render preview, diff, activate
- **Jobs**: search, filter, view events, replay, cancel
- **Failed Jobs / DLQ**: triage, manual retry, attach notes
- **Browser Sessions**: live status, take-control (noVNC), re-login, recycle
- **Webhooks**: endpoints, delivery log, manual replay, signing secrets
- **Cache**: search, purge by tag/template, hit rate
- **Workflows**: visual DAG editor
- **Audit Log**: filter, export, verify chain
- **Quotas & Billing**: per-app, per-target, cost reports
- **Settings**: profile, integrations, SSO, data residency, retention
- **Metrics**: embedded Grafana dashboards

### 24.3 Live Browser Viewer
- Each worker exposes a noVNC endpoint protected by short-lived tokens.
- Operator clicks "Watch" on a session → live screen + click forwarding.
- Recording lights up red so operator knows what's stored.

### 24.4 PWA + Mobile monitoring
- Dashboard is installable as a PWA.
- A trimmed-down **Tauri Mobile** app wraps the same SvelteKit code, adds native push for alerts.

---

## 25. Plugin & Extension System (WASM)

### 25.1 Plugin host
- **wasmtime** in the Go gateway (and **wasmer**-Python in the worker).
- WASI Preview 2; component model for typed interfaces.
- Capability-based: a plugin must be granted explicit permissions (network, FS scope, env vars, secrets, time, randomness).

### 25.2 Plugin kinds & hooks
- **Pre-job**: inspect/transform/reject incoming jobs (e.g. PII redactor).
- **Post-job**: transform/forward results (e.g. push to EMR).
- **Adapter-extension**: add steps to an existing adapter.
- **Custom command type**: implement a brand-new command with its own schema and adapter.
- **Webhook transformer**: shape payloads per consumer.
- **Custom validator**: domain-specific validation (ICD-10, LOINC, etc.).

### 25.3 Plugin manifest
```toml
[plugin]
name = "pii-redactor"
version = "1.0.0"
api = "ubag-plugin-v1"
hooks = ["pre_job"]
capabilities = []  # pure compute, no IO
[author]
name = "PolytronX"
[license]
spdx = "Apache-2.0"
```

### 25.4 Distribution
- A **plugin marketplace** (a Git repository with signed manifests + URLs).
- `ubag plugins install pii-redactor@1.0.0` verifies signature, capabilities, and installs.

### 25.5 Why WASM
- One distribution format for plugins from any language (Rust, Go, AssemblyScript, Zig, C, Swift).
- Sandboxed by default — a malicious plugin cannot exfiltrate data without explicit capability grants.
- Near-native performance; cold start in single-digit milliseconds.

---
## 26. Multi-Region & High Availability

### 26.1 Single-region HA (Tier 3)
- Gateway: 3+ replicas behind Caddy with health checks; rolling deploy via k8s.
- DB: CloudNativePG operator → primary + 2 sync replicas; auto-failover.
- DragonflyDB: 3-node cluster with replication.
- NATS: 3-node JetStream cluster with R3 streams.
- MinIO: distributed mode, 4-node erasure-coded.
- Workers: stateless, auto-scaled by HPA on `nats_pending` or CPU.

### 26.2 Multi-region (Tier 4)
- Global anycast / GeoDNS in front of regional ingresses.
- Postgres logical replication to the closest read replica per region (or `pgactive` for active-active where the workload tolerates).
- NATS supercluster with leaf nodes per region.
- Garage for geo-replicated object storage.
- Tenants pinned to a "home region" for residency, can opt into multi-region.

### 26.3 Browser worker placement
- Workers can be pinned to a region (so that login state stays in that region).
- A *roaming* mode allows workers on the user's premises (sidecar mode at scale) — useful for hospitals that cannot send data offsite.

### 26.4 Failure isolation
- Cells per region; a cell failure does not cascade.
- Per-tenant feature flags and per-region kill switches.

---

## 27. Backup, Disaster Recovery, Migration

### 27.1 Backups
- Postgres: continuous WAL archive to object storage + nightly base backup. Verified by automated restore test.
- Object storage: cross-region replication (Tier 4); versioning enabled.
- Configuration: declarative, in Git; the dashboard's "Export config" produces a single tarball.

### 27.2 Recovery
- One command: `ubag restore --from s3://backups/2026-05-22T03:00Z`.
- Rebuilds DB, restores object storage references, re-syncs DragonflyDB ephemeral state (rebuilt from DB).

### 27.3 Migration paths
- **Edge → Small**: `ubag migrate sqlite→postgres` runs schema convert + data copy.
- **Small → Standard**: deploy Helm chart, point Postgres at managed HA cluster, retire single-VM compose.
- **Standard → Enterprise**: enable multi-region module, run replication bootstrap.

### 27.4 Schema migrations
- Forward-only with reversible "expand → migrate → contract" pattern.
- Online: no table locks > 1 s in normal ops; long migrations use `pg_repack` or partition swapping.
- Every schema change CI-tested against a previous-version DB snapshot.

---

## 28. Compliance & Privacy

### 28.1 Modes
- **Standard mode**: default; reasonable defaults.
- **GDPR mode**: data subject export/erasure endpoints, retention enforcement, audit of access, EU residency pin.
- **HIPAA mode**: PII encrypted per-tenant, no semantic cache for PHI templates, audit chain immutable, BAA-friendly logs (no PHI in logs), break-glass admin access logged with reason.
- **SOC 2-ready mode**: enables all required controls (immutable audit, MFA enforcement, access reviews export, change management hooks).

### 28.2 Data classification
- Templates tag inputs/outputs with classifications (`PII`, `PHI`, `secret`, `public`).
- Storage, caching, logging behavior driven by classification.

### 28.3 Subject access
- `POST /v1/privacy/export` returns a downloadable archive of all data for a subject reference.
- `POST /v1/privacy/erase` cascades deletion with verifiable receipt.

### 28.4 Residency
- Per-tenant `data_region`. All storage operations check it; cross-region reads are denied unless explicitly allowed.

---

## 29. Deployment Options

### 29.1 Single static binary (`ubag` — edge profile)
```
ubag init                    # writes default config + creates DB
ubag start                   # starts everything in one process
ubag tui                     # gorgeous TUI dashboard
```

### 29.2 docker-compose (small profile)
- `infra/compose/` ships a `docker-compose.yml` with gateway + worker + Postgres + Dragonfly + Caddy + Grafana stack.
- One-line up: `docker compose up -d`.

### 29.3 Kubernetes Helm chart (standard/enterprise)
- `helm install ubag oci://ghcr.io/ubag/charts/ubag`.
- Values for HA, autoscaling, ingress, cert-manager, external secrets, OpenTelemetry collector, Grafana stack.
- **Operator** (CRD-driven) for declarative target/adapter/template management — GitOps-friendly.

### 29.4 OS-native installers
- **Windows**: MSI via WiX, with auto-update.
- **macOS**: signed `.pkg`, notarized; Homebrew formula.
- **Linux**: deb + rpm (built by `nfpm`), Snap + Flatpak optional.

### 29.5 GitOps
- Adapters, templates, target definitions, app config — all declarative YAML in a Git repo.
- The dashboard can render diffs between Git config and live state.
- ArgoCD / Flux compatible.

### 29.6 Terraform modules
- `terraform-ubag-aws`, `-gcp`, `-azure`, `-hetzner`, `-digitalocean` — provision the full stack with one module.

### 29.7 One-line installer
```
curl -sSf https://get.ubag.dev | sh
```
- Installs the `ubag` binary in `$HOME/.ubag/bin/`, sets up service files on request, prompts for profile.

---

## 30. Folder Structure (monorepo)

```
ubag/
├── apps/
│   ├── gateway/                  # Go: API gateway
│   ├── worker/                   # Python: browser worker
│   ├── orchestrator/             # Go: job orchestrator (can co-locate with gateway)
│   ├── webhook-dispatcher/       # Go
│   ├── sidecar/                  # Rust: local connector
│   ├── cli/                      # Go: ubag CLI + TUI
│   ├── dashboard/                # SvelteKit
│   └── mobile-monitor/           # Tauri Mobile (Rust + Svelte)
│
├── packages/
│   ├── proto/                    # Protobuf v1 + buf config
│   ├── openapi/                  # openapi.yaml
│   ├── shared-schemas/           # JSON Schemas for all command types
│   ├── sdk-typescript/
│   ├── sdk-python/
│   ├── sdk-go/
│   ├── sdk-rust/
│   ├── sdk-dotnet/
│   ├── sdk-java/
│   ├── sdk-swift/
│   ├── sdk-ruby/
│   ├── sdk-php/
│   ├── sdk-dart/
│   ├── sdk-elixir/
│   ├── prompt-templates/         # Starter templates (radiology, OET, etc.)
│   └── conformance/              # Test suite all SDKs must pass
│
├── adapters/
│   ├── _common/                  # Shared base classes, helpers, stealth
│   ├── deepseek_web/
│   ├── claude_web/
│   ├── chatgpt_web/
│   ├── gemini_web/
│   ├── mistral_lechat/
│   ├── perplexity_web/
│   ├── generic_chat/             # Config-driven adapter
│   ├── generic_form/
│   └── mock/
│
├── plugins/
│   ├── pii-redactor/
│   ├── icd10-validator/
│   └── examples/
│
├── infra/
│   ├── compose/                  # docker-compose for small tier
│   ├── helm/                     # Helm chart for standard/enterprise
│   ├── terraform/                # Per-cloud modules
│   ├── caddy/                    # Caddyfiles
│   ├── grafana/dashboards/       # Pre-built dashboards
│   ├── prometheus/rules/
│   ├── alertmanager/
│   ├── nats/
│   └── postgres/
│
├── migrations/                   # SQL migrations (Postgres + SQLite dialects)
│
├── docs/
│   ├── openapi.yaml
│   ├── adr/                      # Architecture Decision Records
│   ├── sdk-integration-guide.md
│   ├── adapter-developer-guide.md
│   ├── plugin-developer-guide.md
│   ├── operator-runbook.md
│   ├── security-model.md
│   ├── threat-model.md
│   ├── error-catalog.md
│   └── compliance/
│       ├── hipaa.md
│       ├── gdpr.md
│       └── soc2.md
│
├── tests/
│   ├── e2e/
│   ├── load/                     # k6 + locust scripts
│   ├── chaos/                    # chaos toolkit experiments
│   └── adapters/                 # adapter-specific harnesses
│
├── tools/
│   ├── make-sdks/                # SDK generation pipeline
│   ├── adapter-cli/              # scaffold + test adapters locally
│   ├── benchmark/                # benchmark suite
│   └── synthetic-monitor/
│
├── .github/workflows/            # CI: build, test, conformance, release
├── Makefile                      # `make dev`, `make test`, `make sdks`, `make release`
├── flake.nix                     # Nix dev shell (optional)
├── LICENSE                       # Apache-2.0 for SDKs, AGPL-3.0 for server (or dual)
└── README.md
```

---

## 31. Revised Development Phases (concrete deliverables)

### Phase 0 — Foundations (Week 0–1)
- Monorepo + tooling (Turborepo + Make + golangci-lint + ruff + biome).
- ADR #001: licensing (Apache-2.0 SDKs / AGPL-3.0 server).
- ADR #002: schema-driven SDKs.
- ADR #003: idempotency-first.
- CI pipeline (lint, test, build matrix, container build, sign, SBOM).
- `make dev` brings up edge profile end-to-end.

### Phase 1 — Contract & Schemas (Week 1–2)
- Final `openapi.yaml` + `*.proto` + JSON Schemas.
- Conformance test suite skeleton (~50 baseline tests).
- Mock gateway (Prism + custom).
- Documentation portal (Scalar) deploys from `openapi.yaml`.

### Phase 2 — Core Gateway (Week 2–5)
- Go gateway: REST + WS + SSE + gRPC.
- AuthN/AuthZ service.
- Postgres migrations (full schema).
- Idempotency, validation, rate limiting, observability instrumentation.
- Job orchestrator with priority queues, outbox, DLQ.
- Webhook dispatcher with HMAC + retries + circuit breaker.

### Phase 3 — Browser Worker MVP (Week 4–7)
- Python worker with Playwright + Patchright + fingerprint suite.
- Session pool with warming, manual login, noVNC bridge.
- Adapter SDK + first adapter: `deepseek_web`.
- Drift detection skeleton.
- HAR/screenshot/recording capture.

### Phase 4 — SDKs (Week 5–9, parallel)
- Generated TypeScript, Python, Go, .NET, Java, Rust SDKs.
- Ergonomics layers per SDK.
- Conformance suite passes on all five.
- Published to npm, PyPI, Go modules, NuGet, Maven, crates.io.

### Phase 5 — Templates, Cache, Workflows (Week 7–10)
- Prompt template engine with Jinja2 + Pongo2 parity.
- Semantic cache with pgvector.
- Workflow (saga) executor with DAG.
- Starter template library (radiology, OET, summarize, translate).

### Phase 6 — Sidecar + CLI + Dashboard (Week 8–12)
- Rust sidecar with offline queue + OS keychain + auto-update.
- Go CLI + TUI.
- SvelteKit dashboard MVP (apps, devices, jobs, sessions, templates, webhooks).
- Live browser viewer (noVNC integration).

### Phase 7 — Remaining SDKs + Plugin System (Week 10–14)
- Swift, Ruby, PHP, Dart, Elixir SDKs.
- WASM plugin host + 3 reference plugins.
- Plugin marketplace repo.

### Phase 8 — Hardening, Chaos, Benchmarks (Week 12–16)
- Chaos suite green.
- Benchmark suite published; CI regression gates.
- Penetration test (3rd-party or internal red team).
- Threat model document.
- Compliance modes (GDPR, HIPAA) verified.

### Phase 9 — Deployment Profiles (Week 14–18)
- Single-binary edge release with embedded everything.
- Production-grade Helm chart.
- Terraform modules per cloud.
- One-line installer.

### Phase 10 — Multi-Region + Enterprise (Week 18–24)
- Multi-region Helm overlays.
- Postgres logical replication / pgactive.
- NATS supercluster + leaf nodes.
- Garage geo-replication.
- SSO (Keycloak/Authentik test) + SCIM.

### Phase 11 — Community & Adapter Marketplace (ongoing)
- Adapter contribution guide + scaffolder (`ubag adapter new`).
- Public adapter registry with health badges.
- Quarterly release cadence; LTS branches every 4th release.

---

## 32. Testing Strategy

### 32.1 Unit tests
- Gateway: > 80% coverage, table-driven Go tests.
- Worker: pytest with Playwright traces captured on failure.
- SDKs: language-native frameworks; same scenarios across all.

### 32.2 Integration tests
- `make itest` brings up gateway + worker + DB + mock target in containers.
- Assertions on real adapter against a **stub site** that mimics target shapes.

### 32.3 E2E tests
- Run against staging environment with **real** targets.
- Gated: skipped on PRs from forks; manual trigger.

### 32.4 Conformance tests (SDKs)
- 250+ scenarios JSON-defined.
- Each SDK has a thin runner; same assertions guaranteed.

### 32.5 Load tests
- `k6` scripts for HTTP/WS; `locust` for SDK scenarios.
- Published baselines per release.

### 32.6 Chaos tests
- 60-second per-PR run (kill workers, drop NATS msgs, latency injection).
- Weekly 1-hour suite on staging.

### 32.7 Property tests
- Idempotency: replays always return identical results.
- Validation: random fuzzed inputs never crash the gateway.

### 32.8 Visual regression (dashboard)
- Playwright snapshots; tolerance configurable.

---

## 33. Operator Runbook (excerpt)

Every alert ships with a documented playbook in `docs/operator-runbook.md`.

Examples:

### Alert: `UBAG.AdapterDriftDetected`
1. Open dashboard → Adapters → identify adapter.
2. Inspect last successful vs last failed DOM snapshot diff.
3. Choose: (a) roll back to previous version, (b) patch selectors, (c) regenerate ML selectors.
4. Verify with `ubag adapter test deepseek_web@1.7.4 --canary 5%`.

### Alert: `UBAG.QueueBacklog`
1. Check `nats_pending` per stream.
2. Check worker capacity (`worker_active_sessions` vs `worker_max_sessions`).
3. Auto-scale should fire; if not, manually `kubectl scale deploy worker`.
4. If sustained, investigate target rate-limiting (we may be hammering DeepSeek).

### Alert: `UBAG.WebhookDeliveryFailing`
1. Open Dashboard → Webhooks → endpoint.
2. Inspect last responses; check breaker state.
3. Pause endpoint if customer reports outage.

### Alert: `UBAG.BrowserSessionQuarantined`
1. Dashboard → Sessions → see quarantine reason.
2. If CAPTCHA: open Live Login, solve, re-add to pool.
3. If 2FA challenge: trigger re-login workflow.
4. If repeated, rotate fingerprint / proxy.

---

## 34. Documentation Strategy

- **Reference docs** generated from OpenAPI / proto / JSON Schema.
- **Conceptual docs** in `docs/` (this plan, ADRs, threat model, compliance).
- **Guides** per audience: app developer, adapter developer, operator, security reviewer, contributor.
- **Recipes / cookbook**: 30+ end-to-end how-tos (e.g., "Add a target", "Replay a failed job", "Build a custom template").
- **Architecture Decision Records** for every significant choice.
- All docs in Markdown, rendered by Starlight (Astro) or Docusaurus, deployable as static site.

---

## 35. Community & Open-Source Governance

### 35.1 License posture
- **SDKs**: Apache-2.0 — maximum adoption, embeddable in proprietary apps.
- **Server (gateway, worker, orchestrator)**: AGPL-3.0 — protects against extract-and-sell SaaS forks; community can self-host freely. (Alternative: full Apache-2.0 for fastest growth, with revenue from hosted offering. Pick one in ADR.)
- **Adapters & templates**: Apache-2.0 (community contributions encouraged).

### 35.2 CLA / DCO
- DCO (Developer Certificate of Origin) preferred — friction-free.

### 35.3 Governance
- BDFL initially (project lead), transition to TSC after community matures.
- RFC process for major changes (template in `docs/rfcs/0000-template.md`).
- Stable / Beta / Alpha feature tagging.

### 35.4 Releases
- Quarterly minor releases; monthly patch releases; LTS every fourth minor (18-month support).
- Changelog generated from conventional commits; release notes hand-curated.

### 35.5 Community spaces
- GitHub Discussions, Discord/Matrix bridge, monthly community call.
- Contributor recognition: README, release notes, swag.

---

## 36. Cost & Operations Cheatsheet

### 36.1 Smallest viable production deployment
- 1× $5–10/mo VPS (2 vCPU, 2 GB RAM) — handles small profile for a single team comfortably.
- ~$0 if self-hosting on existing hardware.

### 36.2 Mid-size (100 apps, ~50k jobs/day)
- 3× small VMs for gateway/orchestrator/webhook (HA).
- 2× browser worker VMs (4 vCPU, 8 GB RAM each).
- 1× managed Postgres (or self-host with HA).
- 1× MinIO node, 1× DragonflyDB node, 1× NATS node.
- Total: ~$150–250/mo.

### 36.3 Operating tools
- Dashboard → click-and-fix UX for 90% of issues.
- CLI `ubag doctor` runs holistic diagnostics.
- `ubag bench` reproduces benchmark suite locally.

---

## 37. World-Class Feature Checklist (the additions)

Everything below is **net-new vs the previous plan**. Each line is independently deliverable.

### Protocol & API
- ✅ HTTP/3 (QUIC) on ingress
- ✅ gRPC + gRPC-Web
- ✅ Server-Sent Events
- ✅ MessagePack content negotiation
- ✅ Optional MQTT 5 for IoT/edge
- ✅ Batch endpoint (up to 100 jobs/request)
- ✅ Cursor pagination + sparse fieldsets
- ✅ IETF rate-limit headers
- ✅ Date-based API versioning
- ✅ Idempotency-Key semantics
- ✅ Webhook signing (HMAC-SHA256 + timestamp + nonce)

### Cross-platform / SDKs
- ✅ 11 first-class SDKs (TS, Py, Go, Rust, .NET, Java, Swift, Ruby, PHP, Dart, Elixir)
- ✅ Schema-driven generation pipeline
- ✅ Conformance suite ensures parity
- ✅ Native async + sync per language
- ✅ Built-in OpenTelemetry hooks
- ✅ Local sidecar auto-discovery

### Performance
- ✅ Single-digit-ms middleware chain
- ✅ Browser warm pool eliminates cold start
- ✅ Semantic cache with pgvector (HNSW)
- ✅ L0/L1/L2/L3 cache tiers
- ✅ Resource interception (60% bandwidth saved)
- ✅ Speculative streaming parse
- ✅ Pre-warm DNS + connections
- ✅ Vectorized JSON (orjson/jsoniter/simdjson)
- ✅ Published benchmark suite + CI regression gates

### Stability
- ✅ Idempotency keys everywhere
- ✅ Transactional outbox
- ✅ Saga workflows
- ✅ Circuit breakers per adapter / webhook
- ✅ Bulkheads per tenant / target
- ✅ Bounded queues + backpressure
- ✅ Graceful shutdown with drain
- ✅ Chaos suite in CI
- ✅ Stable error catalog (`UBAG-*` codes)
- ✅ DLQ with one-click replay
- ✅ Auto-rollback adapters on drift

### Browser engine
- ✅ Patchright stealth fork
- ✅ Realistic fingerprint suite
- ✅ Humanized mouse / typing
- ✅ Resource filtering
- ✅ Proxy rotation hooks
- ✅ DOM snapshot diff drift detection
- ✅ ML-fallback selectors
- ✅ Recording & time-travel debug
- ✅ Live noVNC viewer + take-control
- ✅ Session warming + recycle policy
- ✅ Synthetic monitoring per target

### Security
- ✅ TLS 1.3 / HTTP/3, mTLS optional
- ✅ JWT + OAuth2 + OIDC + SAML + SCIM
- ✅ RBAC + ABAC (CEL / Rego)
- ✅ Merkle-chain audit log
- ✅ HIPAA / GDPR modes
- ✅ Per-tenant envelope encryption
- ✅ Secrets in OS keychain (sidecar)
- ✅ Signed binaries + signed containers + SBOM
- ✅ Secret-scanner partner format

### Observability
- ✅ OpenTelemetry traces end-to-end
- ✅ Prometheus + Loki + Tempo + Grafana
- ✅ Continuous profiling (Pyroscope)
- ✅ GlitchTip error aggregation
- ✅ Pre-built dashboards
- ✅ Cardinality budget enforcement
- ✅ Synthetic monitoring
- ✅ SLO dashboards

### Platform
- ✅ 4 deployment profiles (edge → enterprise)
- ✅ Single static binary (edge)
- ✅ Helm chart + Kubernetes operator (standard+)
- ✅ Terraform modules per cloud
- ✅ One-line installer
- ✅ GitOps-friendly config
- ✅ Multi-region replication
- ✅ Backup with point-in-time recovery
- ✅ Migration tooling between tiers

### Developer / extensibility
- ✅ WASM plugin system (capability-gated)
- ✅ Adapter SDK with versioning + canary + auto-rollback
- ✅ Prompt template engine with A/B testing
- ✅ Output schema validation + auto-retry-with-critique
- ✅ Workflow DAG (sagas)
- ✅ Built-in renderers (DOCX, PDF via Typst, HTML)
- ✅ Mock target + mock adapter for tests
- ✅ `ubag adapter new` scaffolder

### Operations
- ✅ Live browser viewer + manual login
- ✅ Failed-job DLQ triage UI
- ✅ Cache search + purge by tag
- ✅ Per-app cost ledger
- ✅ Operator runbook per alert
- ✅ Mobile monitoring app
- ✅ TUI mode (`ubag tui`)

---

## 38. One-Line Architecture (revised)

```
Any client
  → SDK (11 langs) / REST / WS / SSE / gRPC / MsgPack / MQTT / CLI / Sidecar / Webhook
  → Caddy ingress (TLS 1.3 + HTTP/3 + WAF + rate-limit)
  → Go Gateway (auth → validate → idempotency → cache check → enqueue)
  → NATS JetStream (priority lanes + dedup + outbox)
  → Python Worker (session pool + Patchright + fingerprint + adapter)
  → Target website
  → Normalize + validate + render (MD/HTML/PDF/DOCX)
  → Persist (Postgres partitioned) + cache (pgvector) + emit events
  → Webhook (HMAC-signed, retried, DLQ) or stream back to client
  → Audit (merkle-chain) + observe (OTel → Tempo/Loki/Prometheus → Grafana)
```

---

## 39. Final Business Decisions Captured

| Decision | Choice | Rationale |
|---|---|---|
| Integration platform vs single bot | **Platform** | Reuse across all your apps; future-proof |
| OSS license | **Apache-2.0 SDKs + AGPL-3.0 server** (revisit) | Adoption + defensive |
| Primary gateway language | **Go** | Static binary, perf, ops simplicity |
| Browser engine | **Playwright + Patchright** | Best stealth + ergonomics today |
| Primary DB | **PostgreSQL** | Reliability + pgvector + partitioning |
| Cache | **DragonflyDB / Valkey** | Redis-compat, multi-core, OSS |
| Queue | **NATS JetStream** (River for edge) | Smallest reliable solution that scales |
| Observability | **Prometheus + Loki + Tempo + Grafana** | OSS standard |
| Plugins | **WASM** | Language-agnostic + sandboxed |
| Deployment | **4-tier same codebase** | One product, every scale |

---

## 40. Glossary

- **Adapter** — Code that knows how to drive one target website.
- **Adapter drift** — When a target's UI changes and selectors break.
- **App** — A client integration (e.g., "Radiology Reporting Windows").
- **Bulkhead** — Resource isolation between tenants/targets.
- **Canary** — Small-percentage rollout of a new adapter version.
- **Circuit breaker** — Auto-pause calls to a failing dependency.
- **CEL** — Common Expression Language for ABAC predicates.
- **DLQ** — Dead-letter queue for irrecoverable jobs.
- **Idempotency key** — Client-supplied (or auto) ID so retries are safe.
- **Outbox pattern** — Atomic write of state + event for reliable publishing.
- **Patchright** — Stealth-patched fork of Playwright.
- **Saga** — Multi-step workflow with compensations.
- **Sidecar (UBAG)** — Local-host Rust connector for legacy desktop apps.
- **Target** — A website that UBAG drives (e.g., DeepSeek Web).
- **Tenant** — Top-level isolation boundary.
- **WASM plugin** — Sandboxed user-supplied extension.

---

## Appendix A — Minimal example: end-to-end from a desktop app

```ts
// Electron renderer / Tauri front-end / any Node desktop app
import { Ubag } from "@ubag/sdk";

const ubag = new Ubag({
  appId: "radiology-electron",
  appSecret: process.env.UBAG_SECRET!,
  // Sidecar auto-discovered at http://127.0.0.1:7878
  // Falls back to https://gateway.your-domain.example if absent
});

// 1. Simple call with a template
const r = await ubag.jobs.run({
  target: "deepseek_web",
  templateId: "radiology_ct_brain_v3",
  input: {
    age: 55, sex: "M",
    findings: "Acute infarct in left MCA territory",
  },
  options: { responseFormats: ["markdown", "sections"] },
});
showReport(r.output.sections.impression);

// 2. Streaming token-by-token
const stream = ubag.jobs.stream({
  target: "claude_web",
  prompt: "Summarize this discharge letter…",
});
for await (const ev of stream) {
  if (ev.type === "token") appendToken(ev.text);
  if (ev.type === "completed") finalize();
}

// 3. Workflow — generate → critique → revise → render PDF
const wf = await ubag.workflows.run({
  workflowId: "oet_letter_full_pipeline",
  input: { scenario, candidateNotes },
});
downloadPdf(wf.steps.render_pdf.output.pdf_url);
```

The platform delivers the answer; the desktop app stays small and focused on UX.

---

## Appendix B — Quick comparison vs the previous plan

| Area | Previous | This plan |
|---|---|---|
| Tech stack | Implicit (FastAPI + Postgres + Redis) | Explicit, OSS, justified per layer, with alternatives |
| Profiles | One (cloud) | Four (edge → enterprise), same codebase |
| Protocols | REST + WS | REST + WS + SSE + gRPC + gRPC-Web + MessagePack + MQTT |
| SDKs | 5 named | 11 first-class, schema-driven, conformance-tested |
| Authentication | App secret + device | + JWT + OAuth2 + OIDC + SAML + SCIM + mTLS + ABAC |
| Idempotency | Not specified | Required and structured |
| Workflows | Not specified | Saga DAG with compensations |
| Caching | Not specified | L0/L1/L2/L3 with semantic cache (pgvector) |
| Templates | Mentioned | Versioned engine with A/B, schemas, validation |
| Adapters | Interface sketch | Versioned, canary, drift detection, ML fallback |
| Browser stealth | Not specified | Patchright + fingerprint suite + humanization + proxies |
| Live ops | Manual login | noVNC viewer + take-control + recording + time-travel |
| Observability | Not specified | OTel + Prometheus + Loki + Tempo + Pyroscope + GlitchTip |
| Reliability | Implicit retries | Outbox + sagas + circuit breakers + bulkheads + DLQ + chaos |
| Sidecar | Mentioned | 4 MB Rust binary, offline queue, OS keychain, auto-update |
| Plugins | Not specified | WASM, capability-gated, marketplace |
| Compliance | Not specified | HIPAA + GDPR + SOC 2-ready modes |
| Deployment | Docker compose | Static binary + compose + Helm + operator + Terraform + installers |
| Multi-region | Not specified | Tier 4: NATS supercluster + PG logical / pgactive + Garage |
| Documentation | Plan only | OpenAPI → reference + ADRs + runbooks + threat model + cookbook |

---

*End of master blueprint. This document is the contract between product vision and engineering execution; every PR title should map to a section here.*

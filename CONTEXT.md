# UBAG — Universal Browser-Automation Gateway

UBAG is a self-hostable gateway that lets any client drive web-based AI and automation targets through stable, versioned APIs. This glossary is the canonical domain vocabulary for the whole repo — single context covering apps, packages, adapters, and docs.

## Language

### Platform & tenancy

**Tenant**:
The top-level isolation boundary; work, data, and browser resources are partitioned by tenant and never shared across tenants.
_Avoid_: org, workspace

**Client**:
Anything that calls UBAG — SDK, CLI, sidecar, mobile app, browser extension, server app.
_Avoid_: caller (as a domain noun), consumer

**App**:
The registered client identity (`app_id`) a client authenticates as; scoping partner of tenant.

**Device**:
An enrolled installation of an app, bound to a hardware fingerprint.

### Work

**Job**:
The atomic unit of work: one command against one target.
_Avoid_: run, task

**Job Status**:
One of 14: `created, queued, assigned, running, token_streaming, completing, completed, completed_with_warnings, failed_retryable, failed_terminal, dead_letter, cancelled, timed_out, scheduled` (gateway reality; the JSON schema lags by `scheduled`).
_Avoid_: accepted, failed, retrying, done, pending (phantom SDK/dashboard words)

**Priority**:
Client-facing urgency `low | normal | high | urgent`; mapped internally to dispatch lanes `crit | high | norm | low | bulk`.

**Idempotency Key**:
Client-supplied unique-per-operation key; replays return the original job.

**Command Type**:
The verb of a job, validated per (command_type × api_version) schema.

**API Version**:
The date-form contract token carried by every request (e.g. `2026-05-22`) — distinct from both URL prefixes (`/v1/`) and the PRD's release milestones (v0/v1/v2).

**Workflow Definition** / **Workflow Run**:
A versioned multi-step job chain (`wfd_…`) and one execution of it (`wfr_…`).

### Targets & conversations

**Target**:
A website UBAG drives (e.g. DeepSeek Web, a hospital portal).
_Avoid_: provider (standalone)

**Adapter**:
The versioned integration module that drives one target (`name@version`).
_Avoid_: queue/store backend, browser-engine driver (other senses of "adapter")

**Conversation**:
The caller-owned logical thread with a target.

**Conversation Key**:
The conversation's identifier — wire field `conversation_id`, store column `conversation_key` — scoped to (tenant, app, target); reusing it resumes the same provider chat.

**Conversation Model**:
How a target supports parallel chats: `url` (default), `tabbed` (inert alias of `url` today), `spa-singleton` (one provider context per tab; not yet wired in the live path).

**Provider Chat Thread**:
The provider-side thread a conversation maps to, referenced by its **Thread Ref** (chat URL only; `provider_thread_ref` / `thread_ref`).
_Avoid_: chat, thread (bare), conv_id

**Model Settings**:
Per-job provider UI settings, validated against the adapter's model catalog.

### Browser orchestration

**Browser Instance**:
One browser process; the resource and crash domain.

**Provider Context**:
One logged-in browser context per (target, identity) — the identity and rate-limit domain.
_Avoid_: browser session, session (v2.0 compat name)

**Channel Tab**:
One tab running exactly one conversation; the unit of in-flight work.
_Avoid_: channel (bare)

**Account Binding**:
The client-supplied ownership reference for a manual session (wire field `account_binding_id`; `identity_ref` in orchestration code). Defaults to `unbound`, in which case all unbound jobs on a target share one provider context.

**Identity**:
Which login/account on a target; the scoping key of provider contexts alongside tenant and target. Established by the job's Account Binding.

**Adaptive Concurrency (AIMD)**:
The per-(target, identity) tab-ceiling controller: additive increase on sustained success, multiplicative decrease on negative signals.

**Submit Pacer**:
The per-(target, identity) token bucket spacing prompt submissions across tabs.

### Events, alerts, audit

**Job Event**:
A sequenced, contract-typed event on a job (19 types incl. `token`, `blocked`, `warning`).

**Worker Event**:
Worker telemetry the gateway ingests; non-lifecycle types are intercepted, not persisted as job events.

**Blocked**:
Worker signal that a job cannot proceed without human action or retry; never a job status — the gateway records it as `failed_retryable`.

**Alert**:
The human-action-needed entity with lifecycle `open → notified → acknowledged → resolved | expired`; kinds `captcha | manual_login | verification | other`.

**Audit Record**:
An append-only, hash-chained (tamper-evident) record of a state-mutating action.

### Auth & security

**Gateway Session**:
The server-side SSO auth session bound to tenant/app/role.
_Avoid_: session (bare — see also Provider Context)

**App Secret**:
Static bearer credential; authenticates the app-secret principal.

**App JWT**:
Short-lived RS256 client token (claims `tid`/`sub`/`role`; lifetime capped at 24h).

**Personal Access Token (PAT)**:
Long-lived admin-scoped credential (`ubag_pat_…`), stored hash-only.

**Device Token**:
Per-installation credential bound to a device fingerprint.

**Role**:
One of seven: `viewer, developer, operator, admin, superadmin, support, service`.

**RBAC Action**:
A `namespace:verb` authorization string (e.g. `job:read`, `alerts:manage`).

**Safe Mode**:
The hard product constraint: user-owned manual sessions only — automated login, credential scraping, credential storage, and CAPTCHA solving are forbidden. See ADR-0002.

**Privacy Mode**:
The per-job data-handling option (field `safety_mode`: `standard | hipaa | gdpr | debug`).
_Avoid_: safe mode (for this concept)

### Delivery surfaces & storage

**Artifact**:
Job-associated stored output (`art_` id; types screenshot, dom_snapshot, har, playwright_trace, video_recording, console_log, downloaded_file, normalized_output).

**Webhook**:
Signed outbound callback (HMAC-SHA256, `Ubag-Signature` header).

**Prompt Template**:
A versioned prompt definition with input/output JSON Schemas.

**Sidecar**:
The local-host connector for legacy desktop apps (loopback proxy + offline queue).

**SDK**:
First-party client library; active: TypeScript and Go.

**Conformance Suite**:
Shared fixtures asserting identical behavior across SDKs.

**Semantic Cache**:
Similarity-matched result cache keyed per (tenant, target, command_type, app, locale).

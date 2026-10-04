# OET + UBAG RECOVERY — MASTER AGENT PROMPT

> **How to use:** paste this entire file as the first message to your coding agent
> (Claude Code / Codex / Cursor / DSH) running on the office PC. Nothing else is
> needed — all verified evidence and exact file targets are below.

---

## ROLE

You are a senior engineer working on **two related repos on this machine**:

| Repo | Path |
|---|---|
| OET learner platform | `D:\Projects\OET with Dr Hesham\OET Project Web App` |
| UBAG (browser AI gateway) | `D:\Projects\UBAG` |

You have SSH access to the production VPS via the host alias `vps`
(`ssh vps`, root, key already configured). Orca (Docker Desktop alternative) is
installed on this office PC.

Your objective: **restore AI letter checking for all 11 professions**, eliminate
the misleading "insufficient credits" errors, fix two billing defects, and
optionally stand UBAG up locally so the Cloudflare-blocked browser targets work.

---

## PART 0 — HARD RULES (do not violate)

1. **Read `AGENTS.md` in the OET repo first.** It is the always-on contract. Key
   constraints that override your defaults:
   - **GitHub Actions is the only authorized compute environment.** Do NOT run
     `pnpm build`, `pnpm test`, `dotnet build`, `dotnet test`, `tsc`, `lint`,
     Playwright, or `docker build` on this PC. Authorized loop:
     `inspect → edit → commit → push → compute on GitHub Actions → read logs`.
   - The ONLY allowed local command is `pnpm run ship:gate` (seconds-long
     syntax/parse gate).
   - Ship-it workflow: stage **explicit paths only** (never `git add -A`),
     commit, push to `main`, then **watch** `Build & Deploy (web + API)` until it
     succeeds for that SHA. Repo goes **public** before a run and **private**
     after. Never commit secrets or `.env*`.
   - Never claim something built/passed unless a real GitHub Actions run proves
     it. Quote the workflow, run, job and step.
2. **Never print, log, commit, or expose secret values.** When reporting on a
   key, report only its name and length.
3. **Do not make the production changes in PART 4 without owner approval** —
   they change AI provider routing and platform spend limits.
4. If you cannot complete a step, say so explicitly. Never fabricate a result.

---

## PART 1 — VERIFIED DIAGNOSIS (already done; do not re-investigate)

Everything below was confirmed live against production on **2026-09-21**.
Treat it as ground truth and start from it.

### 1.1 AI letter checking is 100% DOWN — root cause

The platform's Anthropic API key is invalid:

```
POST /v1/admin/ai/providers/anthropic/test
→ { "status": "auth", "errorMessage": "HTTP 401: API key is invalid." }

AiUsageRecords: writing.grade | provider_auth | "Provider request failed with HTTP 401"
                | provider=anthropic | model=claude-sonnet-5
```

Evidence of outage (production DB):
```
WritingGrades total ......... 66
Last successful grade ....... 2026-09-12   (9 days before this audit)
Grades in last 7 days ....... 0
Submissions last 30 days .... graded=66, failed=90   (latest failure: today)
```

### 1.2 The failure is MISREPORTED as a credits problem

`WritingSubmissionEvaluationPipeline.cs:1277-1290` catches
`AiQuotaDeniedException` and **unconditionally** rewrites it as:

```csharp
throw ApiException.PaymentRequired(
    "ai_credits_insufficient",
    "You have no AI grading credits remaining. Purchase an AI Credits package to continue.");
```

This collapses **all ten** `AiQuotaService` refusal codes
(`feature_disabled`, `kill_switch`, `global_budget_exhausted`, `user_disabled`,
`no_plan`, `feature_not_in_plan`, `quota_exhausted`, `overage_consent_required`,
`auto_upgrade_pending`, unknown) into "buy credits". The owner spent days
believing this was an RBAC/credit-matrix bug. It was not.

### 1.3 Dead feature routes

18 `AiFeatureRoute` rows point at provider `antigravity-gateway`, which is
**`IsActive = false`** in the DB (the container itself is healthy). Those routes
can never resolve, so the gateway silently falls through to the next active
text-chat provider — `anthropic` — with the dead key.

Dead routes include: `writing.grade`, `writing.sample_score`, `writing.coach.v1`,
`writing.drill.grade.v1`, `speaking.patient.turn.v1`, `conversation.*`,
`mock.full_grade`, `pronunciation.*`, `admin.*_draft`.

### 1.4 `feature_not_in_plan` also blocking

`AiUsageRecords`: `writing.grade | feature_not_in_plan | plan.feature_gate.writing.grade`

`AiQuotaPlan.AllowedFeaturesCsv` is an allow-list when non-empty. The `free` and
`companion-*` plans carry an explicit list that **omits** `writing.grade`; only
`starter` / `pro` / `enterprise` have an empty list (= allow all).

### 1.5 TWO confirmation defects found in the credit ledger

**(a) Each letter is charged TWICE.** Two independent debits on different
references for one letter:

| Reference | Charged at | Code |
|---|---|---|
| `writing-v2:{userId}:{scenarioId}:{n}` | page open / eligibility | `WritingEntitlementService.cs:204` |
| `writing-grade:{submissionId}` | submit for grading | `WritingSubmissionEvaluationPipeline.cs:921` |

Reproduced live: eligibility consumed 2 credits, then submit consumed 2 more.
Catalogue says **2 credits per letter**; production charges **4**.

**(b) A learner with exactly one letter's credits is hard-blocked.** With 2
credits: opening the page spends them, then submit returns
`402 ai_credits_insufficient` — the exact production complaint.

### 1.6 Daily AI budget is hard-coded and not exposed

`AiBudgetClasses.cs:29` — `PlatformDailyCapUsd = 5.00m`, plus class caps
`ScoringDailyLimitUsd = 3.50m`, `InteractiveDailyLimitUsd = 1.00m`,
`AdminDailyLimitUsd = 0.50m`. A Writing grade costs ≈ **$1.17** (230-rule
grounded prompt), so ~3-4 letters/day exhausts the platform. The admin
**Budget panel only exposes the monthly cap**, so the owner cannot raise this
from the UI. This has already caused refusals.

### 1.7 "Unlimited" can display without granting

`BuildBucket` (`AiPackageCreditService.cs:2956-2962`) shows **Unlimited** when any
live lot has `UnlimitedGrading = true`:

```csharp
"writing" or "speaking" or "flexible" => unlimitedGrading || liveLots.Any(lot => lot.UnlimitedGrading),
```

but the authorisation gate `HasActiveUnlimitedGradingAsync`
(`AiPackageCreditService.cs:1936`) **hard-codes** `item.ItemCode == "pkg_oet_mastery"`.
An admin who ticks the catalog editor's "Unlimited Writing/Speaking grading"
checkbox (`components/admin/billing/ai-package-editor.tsx:469`) on any *other*
`ai_package` code gets a lot that displays Unlimited while the server still
charges/denies.

### 1.8 Owner's own test account was simply out of credits

`mindreader420123@gmail.com` → `learner_4e348e63547646fba5cd7a625afe68e1`.
Zero `SubscriptionItems`, no `UnlimitedGrading` lot, all pools 0 at audit time.
Admin "Set exact" correctly refused with `credits_total_below_used`
(already used 7). Not a bug — but relevant context.

### 1.9 Content is complete — do not touch it

223 published Writing scenarios across 11 professions:
Nursing 87, Medicine 55, Pharmacy 29, Physiotherapy 12, Dietetics 10,
Radiography 5, Speech Pathology 5, Podiatry 5, Dentistry 5, Optometry 5,
Occupational Therapy 5.

### 1.10 UBAG status

- Provider `ubag` row exists in OET, PAT configured, `AllowedModelsCsv` populated,
  `DefaultModel = mock`, `FailoverPriority = 70`, `IsActive = true`.
- OET↔UBAG integration is complete: `Services/Seeding/UbagProviderSeeder.cs`.
  No feature routes are seeded by design; routing is admin-owned.
- **40 models** exposed. `mock`, `mock|mock-fast`, `mock|extended` work.
- All consumer-web targets fail:
  - **Cloudflare 403 `Cf-Mitigated: challenge`**: chatgpt.com, claude.ai,
    chat.mistral.ai (datacenter IP)
  - HTTP 202 soft-block: chat.deepseek.com
  - `generic_chat` / `generic_form`: `requires user-owned manual session context
    fields: account_binding_id, consent_ref, automation_scope`
  - Reachable (HTTP 200): **gemini.google.com**, **duck.ai**
- UBAG mislabels the Cloudflare block as `selector_drift_detected`
  (`apps/worker/ubag_worker/live/engine.py:379-405`) — the page never rendered,
  so no selector could match.
- UBAG's own design: `adapters/chatgpt_web/manifest.json` is `"status": "stub"`,
  `manual_login_required: true`, `automated_login: "forbidden"`.
  `page_driver.py:825` uses `launch_persistent_context`; `envelope.py:108`
  defaults `headless = False`. Human-in-the-loop is intentional, and
  `session.manual_action_required` alerts already email
  `mindreader420123@gmail.com`.
- Licensing: gateway + worker are **AGPL-3.0** (`licensing.md`) — running
  unmodified internally is fine; do not fork and serve it to students.

### 1.11 A LIVE Gemini key exists but is unused

Prod `.env.production` has `GEMINI_API_KEY` (len 39) and it **works** — it
listed `gemini-2.5-flash`, `gemini-2.5-pro` live. But **nothing in the OET
backend reads it**: the only Gemini consumer is `GeminiNativeProvider`
(`AiProviderRegistry.cs:623`), which throws unless `AudioAttachments` is
non-empty (`:637-638`) — audio-only, unusable for text grading. Its provider row
isn't seeded either. `PronunciationOptions.GeminiApiKey` binds
`Pronunciation:GeminiApiKey`, **not** `GEMINI_API_KEY`.

**This is the highest-value unexploited asset in the system.** A supported,
paid, working API key that could grade text and needs no proxy, no browser, no
CAPTCHA and no 24/7 machine.

---

## PART 2 — TASKS

Order matters. Complete each fully, verify, then move on.

### TASK A — OET code fixes (local edits, shipped via CI)

Repo: `D:\Projects\OET with Dr Hesham\OET Project Web App`

**A1. Stop mislabelling non-credit failures.**
File: `backend/src/OetLearner.Api/Services/Writing/WritingSubmissionEvaluationPipeline.cs`
(lines ~1277-1325)

Split the single blanket catch into distinct honest outcomes:

| `quotaEx.ErrorCode` | New code | HTTP | Meaning |
|---|---|---|---|
| `quota_exhausted` | `ai_plan_quota_exhausted` | 402 | learner's plan token cap |
| `feature_not_in_plan`, `feature_disabled` | `ai_feature_disabled` | 403 | not enabled for this plan |
| `global_budget_exhausted`, `kill_switch` | `ai_platform_budget_exhausted` | 503 | platform-wide, retryable |
| `user_disabled`, `no_plan` | `ai_account_unavailable` | 403 | account config |
| anything else | keep `writing_rubric_failed` | 503 | unknown provider failure |

Log `quotaDecision.PolicyTrace` on every branch. **Never** report a credit
shortfall unless the balance was actually short.

**A2. Distinguish infra failure from a genuine credit deny.**
File: `backend/src/OetLearner.Api/Services/Billing/AiCreditReservationService.cs`
(lines 67-77, 124-128)

Currently every exception becomes `credit_store_unavailable`, which the UI shows
as "not enough credits". Keep a real deny as 402 `ai_credits_insufficient`; map
infrastructure failure to **503 `writing_grading_unavailable` (retryable)**.
Do not change the existing refund-on-persist-failure behaviour.

**A3. Remove the double charge.**
Files: `Services/Writing/WritingEntitlementService.cs`,
`Services/Writing/WritingSubmissionService.cs`,
`Services/Writing/WritingSubmissionEvaluationPipeline.cs`

Make the start-charge and the grade-reservation share **one** business
reference: thread `BuildScenarioStartReferenceIdAsync(userId, scenarioId, ct)`
through `WritingSubmissionGradeContext` and use it as the reservation
reference. `ReserveWritingAsync` is already idempotent on
`BusinessReference` (`Services/Ai/AiCreditReservationService.cs:64-75`), so the
second debit becomes a no-op.

Invariants to preserve:
- A `retry-grade` on a `failed` submission must **never** re-charge.
- A genuine revision **must** charge again (pin this — see
  `PracticeThisAgain_AfterGradedSubmission_ChargesASecondNewCredit`).
- Mock submissions stay excluded by the existing `Mode != "mock"` guard.

**A4. Unify the "Unlimited" predicate.**
File: `backend/src/OetLearner.Api/Services/Billing/AiPackageCreditService.cs`

Replace the hard-coded `item.ItemCode == "pkg_oet_mastery"` at `:1936` with the
same eligibility rule the display already uses. **Do not simply trust
`lot.UnlimitedGrading`** — that would open a revocation hole, because
`OetMastery_BypassesWritingAndSpeakingOnlyWhilePurchaseItemIsActive`
(`AiPackageCreditServiceTests.cs:159`) locks in that cancelling the parent
subscription or the item must revoke immediately.

Correct approach: derive it from the **grant definition** via the existing
chokepoint `LoadEligibleAiPackageGrantsAsync` (`:1625-1649`) + `AiPackageGrant`
parsing (`:3025`), keeping the existing window
(`Active|Trial|FreezeRequested`, `StartedAt <= now`, `ExpiresAt null || > now`,
`item.Status == Active`, `item.StartsAt <= now`) and **adding** the
`item.EndsAt` check that sibling queries already use.

Then make the display agree: `BuildBucket` (`:2956-2962`) and
`BuildBucketDtos` (`:2255-2256`) must take their `unlimited` flags from that
same predicate, so **nothing can display Unlimited while a gate denies**.

Also extend the heal path `MaterializeMissingUnlimitedLotsAsync` (`:1674-1770`)
to cover `UnlimitedGrading` (today it heals Listening/Reading only).

**A5. Frontend honest copy.**
Files: `lib/writing/submit-keys.ts`, `components/domain/InsufficientCreditsModal.tsx`,
`lib/api/client.ts`

Add the new codes; add the missing `no_ai_package_credits` to the
candidate-safe writing map (`submit-keys.ts:49`). Route the new
non-credit codes to honest, **non-purchase** copy (retry / "temporarily
unavailable"), never the "Buy Credits" CTA. Keep the existing
`premium_required` behaviour — it is pinned by
`app/writing/practice/session/[scenarioId]/page.test.tsx:129`.

**A6. Tests.** Add a regression per fix (each must fail before, pass after):
1. `provider_auth`/401 → **not** `ai_credits_insufficient`.
2. `quota_exhausted` → `ai_plan_quota_exhausted`; `feature_not_in_plan` → `ai_feature_disabled`.
3. Throwing ledger → 503, not 402.
4. 2 credits → open scenario **and** submit exactly one letter, **one**
   `GradingDeduct` row; `retry-grade` adds none; `revise` adds exactly one more.
5. Unlimited lot + no item → snapshot unlimited flag **and** bucket `Unlimited`
   **both false** (display and gate agree).
6. Candidate's parent subscription cancelled → still **false** (revocation holds).

**Must stay green, unmodified** — these are the correctness proof:
`AiPackageCreditServiceTests` (`GrantPackage_*`, `OetMastery_Bypasses*`,
`ObjectiveOnlyPurchase_*`), `UserAccessAllocationServiceTests`
(`GrantAddon_OetMastery_MarksWritingAndSpeakingUnlimited`,
`RemoveAddon_OetMastery_ClearsUnlimitedWritingAndSpeaking`,
`GetAccess_StandaloneMasteryWithNullEndsAt_*`), `EntitlementRevocationTests`,
`AiCreditReservationFundingTests`, `WritingEntitlementAuthorizeStartTests`,
`WritingEntitlementServiceTests`, `WritingSubmitAsyncTests`,
`SpeakingExamServiceTests`, `SpeakingCreditReserveCommitTests`.

**A7. Ship it.** `pnpm run ship:gate` → stage **explicit paths** → commit → push
`main` → watch `Build & Deploy (web + API)` for that SHA → confirm live health →
flip repo private. Do not report done after the push.

### TASK B — Wire the live Gemini key for TEXT grading

This is the fastest route to real, 24/7 grading with no browser, no proxy, no CAPTCHA.

1. **Verify the key can serve text.** From the VPS:
   `POST https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent`
   with header `x-goog-api-key: $GEMINI_API_KEY` and a text-only body. Confirm a
   text reply. (Do not print the key.)
2. **Preferred implementation: OpenAI-compatible dialect.** Google exposes an
   OpenAI-compatible endpoint. Register an `AiProvider` row
   `Dialect = OpenAiCompatible`, `Category = TextChat`, with the Gemini base URL
   and the key encrypted via the existing
   `DataProtection` purpose `AiProvider.PlatformKey.v1` (see
   `Services/Seeding/UbagProviderSeeder.cs` for the exact pattern). No new
   dialect, no new `IAiModelProvider` — the registry already handles this shape.
3. **Alternative:** add a text path to `GeminiNativeProvider` rather than
   throwing at `AiProviderRegistry.cs:637-638`. Only do this if step 2 fails.
4. Add a text route: `AiFeatureRoute` for `writing.grade` →
   the new provider + model. Mirror the admin route helper.
5. Add the provider row to the seeder so a fresh environment reproduces it.
6. Prove it: submit a real letter for a real scenario and confirm
   `WritingGrades` gains a row and `AiUsageRecords` shows a success.

**Also fix the config gap:** `GEMINI_API_KEY` is read by nothing.
`PronunciationOptions.GeminiApiKey` binds `Pronunciation:GeminiApiKey`
(`Program.cs:1573-1574`). Either bind the flat env name or register the provider
row explicitly — document which you chose.

### TASK C — UBAG on the office PC (the Cloudflare work)

This is the only way the chatgpt.com / claude.ai / mistral targets
will ever work, because they are blocked by **Cloudflare bot-challenge on a
datacenter IP**. A residential/office IP plus a persistent logged-in profile is
the fix.

1. Install with the official installer:
   `deploy\installers\install.ps1 -Mode Compose` in `D:\Projects\UBAG`.
   Replace every placeholder in `deploy/small/env.local`; the helper refuses
   `up` until you do.
2. Confirm the engine: `.\deploy\small\small.ps1 -Action config` then `-Action up`.
   Health: `Invoke-RestMethod http://127.0.0.1:8080/v1/health`.
   Minimum footprint is **2 vCPU / 4 GB RAM**
   (`apps/docs/src/content/docs/operations/deployment.md:76`).
3. Open the noVNC dashboard and, **manually**, log into each target you want
   (chatgpt.com, claude.ai, etc.) inside the persistent browser profile, solving
   any Cloudflare challenge by hand. The profile is the
   `browser_profile:/profiles` volume, so this survives restarts.
4. Re-run the per-model probe and record which targets now pass. Use the
   existing script `scripts/qa/probe-ubag-models.ps1` in the OET repo, or
   `apps/worker` tests for UBAG.
5. Only then decide about routing OET features to UBAG.

**Let the owner solve the CAPTCHA — do not attempt to automate it.** UBAG's
`registry.json` forbids automated login and CAPTCHA solving.
**Expect expiry:** `cf_clearance` lasts roughly 30 min to a few hours and is
bound to the solving IP + User-Agent. One solve on a residential IP often holds
for a long time, but it is not permanent. That is why UBAG is fine for the
owner's own QA and *not* dependable for unattended candidate grading.

Optional improvements while there:
- Fix the mislabelling so a Cloudflare 403 reports `cloudflare_blocked` instead
  of `selector_drift_detected`
  (`apps/worker/ubag_worker/live/engine.py:379-405`).
- Only if a proxy is genuinely needed, add `HTTPS_PROXY`/`HTTP_PROXY` +
  correct `NO_PROXY` (must include `172.28.0.0/16` or the CDP link on
  `172.28.0.10:9223` breaks) to the `browser` service in
  `docker-compose.vps.yml:189-218`, using **sticky** sessions.
  Free trials to test first: Decodo (free trial, no card), Bright Data ($5
  credit), Webshare (free tier). Avoid $0.02/GB pools — they will fail Cloudflare.

### TASK D — VPS-side config (owner approval required)

Only with explicit approval. These are owner-owned production settings.

1. **Rotate the Anthropic key** (or retire the provider). This alone ends the
   outage — 5 minutes. The owner must supply the key; you cannot.
2. **Activate `antigravity-gateway`** (`IsActive = true`) so the 18 dead routes
   resolve, or repoint those routes at a working provider.
3. **Fix the plan allow-lists** so `feature_not_in_plan` stops refusing paid
   learners (`AiQuotaPlan.AllowedFeaturesCsv`).
4. **Raise the daily budget caps.** Either raise
   `AiBudgetClasses.PlatformDailyCapUsd` /
   `ScoringDailyLimitUsd`, or make them **admin-configurable** and add them to
   the Budget panel. At minimum, align them with real usage:
   observed avg ≈ $5.5/day against a $5.00 cap.

---

## PART 3 — VERIFICATION (mandatory, no exceptions)

For **every** claim, produce evidence:

- Code changes → the GitHub Actions run URL + job + step for that SHA.
- Provider health → the actual API/DB response.
- Letter grading → a real submission that produced a `WritingGrades` row.
- Never assert a local build/test passed. The repo forbids local compute.

End-to-end acceptance test (run **after** TASK B):

1. Pick one published scenario per profession (all 11).
2. Submit a realistic letter to each via `POST /v1/writing/submissions`.
3. Confirm **11/11** produce a grade, and that `AiUsageRecords` shows a
   successful `writing.grade` call for each.
4. Confirm the ledger shows **exactly 2 credits** consumed per letter — not 4.
5. Confirm no response contains a credit-related error code when the true cause
   was provider/auth/budget.

Report as a table: profession · scenario id · grade row · credits charged · pass/fail.

---

## PART 4 — REPORTING

Report back in this shape, and be honest about anything you could not do:

1. **What I changed** — file paths + one line each.
2. **What I verified** — evidence per claim (run/job/step, API response, DB row).
3. **What is now working** — per profession if the acceptance test ran.
4. **What is still broken** — with the exact blocking cause.
5. **What needs the owner** — actions only a human can take (new API key,
   approving production config, solving a CAPTCHA).

**Do not tell the owner "everything is operational" unless the 11/11 acceptance
test passed.** Writing was down for 9 days and was reported as a billing
complaint for most of it. Prefer a precise "here is what works and here is what
does not" over a reassuring summary.

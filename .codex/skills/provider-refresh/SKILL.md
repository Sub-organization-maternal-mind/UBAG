---
name: provider-refresh
description: "Re-aligns UBAG's AI-provider adapters with the providers' CURRENT web UIs. Use when the user asks to verify/fix/update provider selectors, model lists, or thinking/reasoning options for ChatGPT, Gemini, DeepSeek, DuckAI (or a new provider), when jobs fail with selector_drift_detected, when a provider renamed their models or rebuilt their composer, or when the user invokes `providers verify/rebase/add/remove`."
version: 1.0.0
---

# Provider Refresh

AI providers change their web UIs monthly: models get renamed or retired,
composers get rebuilt, menus get flattened. UBAG's adapter surface mirrors
those DOMs in two files that must stay in lockstep. This skill is the
repeatable procedure for re-aligning them — with live verification, never
guessing.

## The two files that must agree

| File | Owns | Read by |
|---|---|---|
| `adapters/<id>/manifest.json` → `model_catalog.settings` | WHAT jobs may set (choice values / toggles) | gateway validation + `GET /v1/openai/models` |
| `apps/worker/ubag_worker/live/selectors.py` → `ProviderSelectors` / `ProviderSetting` | HOW it is clicked in the real DOM + the operator-default `desired` | worker `page_driver.ensure_provider_config` |

Downstream touch points that must move with them (the checker below enforces
most of this): `apps/worker/ubag_worker/adapter_registry.py`
(`REQUIRED_ADAPTER_IDS`) + `adapters/registry.json` (must match each other),
`apps/worker/tests/test_provider_config.py` (pins every operator default),
`apps/gateway/internal/httpapi/server.go` (`targetCatalog`),
`apps/dashboard/src/lib/components/LiveBrowser.svelte` (`providerShortcuts`).
The adapters' Python packages are thin delegation shims — they never need
changes for a selector rebase.

## Hard rules (violating any of these fails the task)

1. **Logins are human-only, forever.** Never fill credentials, never read
   tokens/cookies, never solve CAPTCHAs, never click "Log in". If a provider
   shows a login wall, STOP that provider and ask the operator to sign in via
   the dashboard's Live Browser panel (Browser Sessions page), then continue.
2. **Verification interacts with provider menus ONLY** — open menus, read
   state, select settings, close. Never type into a composer, never submit a
   prompt, never send a message as the user.
3. **Never guess selectors.** Every new selector must be observed in the live
   DOM (probe/composer dumps) before it lands in selectors.py.
4. **Never rewrite PROGRESS.md history** — append a new dated entry only.
5. Per AGENTS.md: skip long builds/suites; run only the targeted tests listed
   below; commit and push once the work is done.

## How to use this skill

| Invocation | Behavior |
|---|---|
| `providers verify [id\|all]` | Read-only live check. No edits. Reports which declared values the live UI still shows and which selector groups still match. |
| `providers rebase <id>` | Full update for ONE provider: probe live DOM → edit manifest + selectors.py + pins → verify live → targeted tests → docs + ledger entry. |
| `providers add <id>` | New provider: copy the manifest pattern from `adapters/duckai_web/manifest.json`, add registry + REQUIRED_ADAPTER_IDS + targetCatalog + selectors block + pins + LiveBrowser shortcut, then rebase. |
| `providers remove <id>` | Delete a provider completely (the 2026-09-26 claude_web removal is the reference — `git log --grep "remove claude"`): adapter dir, registry pair, selectors block, gateway catalog, dashboard shortcut, tests, current-state docs. Never touch DB rows (harmless TEXT orphans). |

If the request does not clearly map to a verb, treat it as `providers verify
all` first and decide from the findings.

## `providers verify` procedure

```bash
# Live capture + diff (read-only; add --open-menus to enumerate closed menus):
node tools/provider-refresh/provider-probe.mjs <id> --open-menus
# Live canary of the click paths (menus only; needs the provider logged in):
node tools/provider-refresh/verify-settings.mjs <id>
# Static cross-file consistency:
node tools/provider-refresh/check-provider-selectors.mjs
```

CDP endpoint defaults to `http://127.0.0.1:15923` (the local deployment's
SSH-tunnelled production Chrome; override with `--cdp` / `UBAG_PROBE_CDP`).
`start-local.ps1` brings the tunnel up and `tunnel-watchdog.ps1` self-heals
it — if the probe says the endpoint is unreachable, run start-local.ps1 first.

Interpretation:
- `likely_login_wall: true` → ask the operator to log in via the Live Browser
  panel; do not proceed on that provider until they have.
- "best-effort unverified" in verify-settings → a setting whose control the
  current UI hides (e.g. ChatGPT's effort pill is model-dependent); expected,
  non-blocking.
- "UNVERIFIED (required)" → real breakage: jobs on this provider will fail
  with `selector_drift_detected` until rebased.

## `providers rebase <id>` procedure

1. **Capture ground truth** (all read-only or menu-only):
   `provider-probe.mjs <id> --open-menus`. Read the capture JSON in
   `tools/provider-refresh/captures/` — `raw_controls`,
   `composer_neighborhood`, and `menu_captures` show the live picker labels,
   testids, and aria states. For deeper structure use the probe's CDP helper
   pattern (`Runtime.evaluate` dumps of the composer tree — see
   `tools/provider-refresh/composer-dump.mjs`).
2. **Edit the manifest** (`adapters/<id>/manifest.json`): set
   `model_catalog.settings` to exactly what the live UI offers today
   (values = the menu item labels the `{value}` templates must match).
3. **Edit `selectors.py`**: update the provider's `ProviderSetting` blocks —
   `open_steps` (how to open the picker), `satisfied_when`/`on_when` (how to
   confirm), `apply_click`/`toggle_click`, `desired` defaults — bump
   `selector_version` to `YYYY-MM-DD-<what-changed>` and write a short
   `Re-baselined <date> against live <url>` comment describing the observed
   UI. Candidates are ORDERED FALLBACK lists: keep old candidates that still
   match (verified by the probe), add new ones first, and only delete
   candidates you confirmed dead.
4. **Defaults policy**: keep the operator's default intent. If a pinned
   default model no longer exists, map it to the closest current option
   (same tier; reasoning on where it was on) and report the change for veto.
   If a setting's control disappears from the UI entirely, remove the
   setting from BOTH files (declaring an unrenderable setting fails every
   job); mark a model-dependent control `required=False` with a comment.
5. **Update the pins** in `apps/worker/tests/test_provider_config.py` (they
   assert the exact desired values) and any selector-shape tests in
   `apps/worker/tests/test_live_adapters.py`.
6. **Verify live**: `verify-settings.mjs <id>` → must show "all settings
   verified" (or only best-effort unverified). Re-run the probe → all
   declared values seen.
7. **Targeted tests** (never the full suites):
   ```bash
   PYTHONPATH="apps/worker;adapters/mock" python -m unittest discover -s apps/worker/tests -p "test_provider_config.py"
   PYTHONPATH="apps/worker;adapters/mock" python -m unittest discover -s apps/worker/tests -p "test_live_adapters.py"
   node tools/provider-refresh/check-provider-selectors.mjs
   ```
8. **Ledger + docs**: append a `## <date> — <provider> rebase` entry to
   PROGRESS.md (symptom → live findings → changes → verification); update the
   adapter tables in `apps/docs/src/content/docs/adapters/` if lineups
   changed; rebuild the dashboard only if LiveBrowser.svelte changed
   (`pnpm --filter @ubag/dashboard build` with the three UBAG_DEV_DEFAULT_*
   envs — see start-local.ps1).

## `providers verify all` — closing checklist

- [ ] `check-provider-selectors.mjs` exits 0
- [ ] probe run for every live target (captures saved under
      `tools/provider-refresh/captures/`)
- [ ] `verify-settings.mjs` shows no required-unverified settings
- [ ] targeted worker tests green
- [ ] PROGRESS.md entry appended (findings + changes + verification)
- [ ] anything the operator must decide (default changes, removed settings)
      listed explicitly in the final report

## Report format (final message)

```
PROVIDER-REFRESH-STATUS: DONE | PARTIAL (login pending for <id>) | FAILED
PROVIDERS: <id>: verified | rebased (<n> selectors, <n> values) | login-blocked
DEFAULTS CHANGED: <provider>.<setting>: <old> -> <new> (reason) | none
REMOVED SETTINGS: <provider>.<setting> | none
TESTS: <modules run, all green | failures>
LEDGER: PROGRESS.md entry appended (Y/N)
OPERATOR ACTIONS: <logins needed, veto items> | none
```

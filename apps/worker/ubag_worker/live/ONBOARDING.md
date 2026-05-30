# Onboarding a new live manual-session provider

UBAG's live web adapters are a **ToS-safe orchestration layer**. They never log
in for the user, never ingest cookies/credentials/tokens, and never solve
CAPTCHAs. When a target is not authenticated, the engine emits
`session.manual_action_required` with a loopback noVNC placeholder URL and waits
for the human to act in a user-owned browser session.

This guide covers adding a brand-new provider to that engine.

## The fast path: `live_web_template`

`apps/worker/ubag_worker/live/selectors.py` exposes a factory that scaffolds a
fully-formed `ProviderSelectors` with safe placeholder selectors:

```python
from ubag_worker.live import live_web_template, LiveSessionEngine
from ubag_worker.live import MockPageDriver  # offline/deterministic driver

selectors = live_web_template(
    provider_id="acme_chat_web",
    display_name="Acme Chat",
    target_url="https://chat.acme.example/",
    # Override only the groups you have confirmed against the live DOM:
    prompt_input=("#acme-input", "textarea[name='message']"),
    submit_button=("button[data-testid='send']",),
)

# Drive it deterministically with no browser/network:
engine = LiveSessionEngine(selectors)
events = engine.run(payload, driver=MockPageDriver(authenticated=True, response_text="ok"))
```

Every group you do **not** override falls back to a conservative,
framework-agnostic placeholder so the manual-login flow works before the
selectors are tuned. Inputs are validated: `provider_id` is required and
`target_url` must be `https://`.

A ready-made `generic_live_web` template is already registered in
`PROVIDER_SELECTORS` as a copy-and-tune starting point.

## Wiring it permanently

1. **Define selectors.** Add a `ProviderSelectors` constant in
   `live/selectors.py` (either by hand like the existing providers, or via
   `live_web_template(...)`), then add it to the `PROVIDER_SELECTORS` tuple.
2. **Confirm the DOM.** Replace each placeholder selector group with confirmed,
   provider-specific CSS/ARIA selectors. Order fallbacks most-specific first.
   Bump `selector_version` and each group's `baseline_version` once verified.
3. **Add a manifest.** Create `adapters/<provider_id>/manifest.json` plus the
   `ubag_<provider_id>_adapter/` package, mirroring an existing live provider
   (e.g. `adapters/chatgpt_web`). The default `run()` must fail closed with the
   safe-mode message; `run_live()` delegates to `LiveSessionEngine`.
4. **Test offline.** Add cases to `apps/worker/tests/test_live_adapters.py`
   using `MockPageDriver` to cover: the happy path, the
   `manual_action_required -> authenticated` flow, selector-drift blocking, and
   the no-secrets invariant. Run `node tools/run-python-worker-tests.mjs`.

## Hard invariants (do not break)

- No credential, cookie, token, or `storage_state` ingestion. The engine
  rejects such payloads with `LiveSessionError`.
- Manual login / CAPTCHA / 2FA / consent are always the human's responsibility.
- User-owned persistent Chromium profiles only (`--user-data-dir`).
- Selector drift surfaces as `UBAG-ADAPTER-DRIFT-014` blocked events; it must
  never silently mis-read the DOM.
- `session_id` is sanitized before building the noVNC URL; never interpolate
  raw user input into paths.

## Offline / deterministic mode

Set `UBAG_ADAPTER_OFFLINE=1` (or `UBAG_WORKER_OFFLINE=true`) to force the
`MockPageDriver`, enabling deterministic unit tests and dry runs without a real
browser or network.

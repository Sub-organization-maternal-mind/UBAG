# Voice activation probe runbook (human-supervised)

Goal: populate `VoiceReadiness.ready_controls` in `apps/worker/ubag_worker/live/selectors.py` from real in-call DOM, so a voice session can reach `connected` (which requires the worker's `activated` state; today the runner returns `unverified_ready` and sessions end `activation_failed`).

Safe-mode applies: user-owned signed-in browser session, no automated login, no credential handling, no CAPTCHA solving. The probe never clicks, types, opens menus, grants mic permission or logs in. A HUMAN starts the call.

## Steps

1. Operator has a signed-in provider tab open in a Chrome with remote debugging (the same CDP endpoint the other probes use, `--cdp` or `UBAG_PROBE_CDP`). Do not run while an operator login/viewer session is active.
2. Pre-call baseline (read-only, no flags): `node tools/provider-refresh/voice-probe.mjs chatgpt_web`. Keep the capture path it prints.
3. The human clicks the provider's voice entry control themselves (ChatGPT "Start Voice"; Gemini "Listen", which has no capture backing it and may be absent or blocked) and grants any mic prompt themselves. Wait until the call is visibly up.
4. In-call capture (still read-only): `node tools/provider-refresh/voice-probe.mjs chatgpt_web --in-call --i-started-the-call --baseline <baseline capture.json>`. The tool refuses to run without `--i-started-the-call`. Long button text is dropped from the capture so conversation content is not committed.
5. The output lists `ready_controls_candidates` (controls visible in-call that were not in the baseline, with a stable `data-testid` or `aria-label`; entry, Dictate, attach and send controls are excluded) and a ready-to-paste Python tuple. Candidates are UNVERIFIED.
6. Human review: pick controls that exist ONLY while the call is up (for example an end or mute control). Also capture, if encountered, `setup_prompts` (onboarding / mic dialogs) and `unavailable` notices by running the probe at those moments.
7. Edit `selectors.py` (`VoiceReadiness(ready_controls=..., baseline_version="<date>-voice-incall-probe")`), update `test_shipped_selectors_carry_no_invented_ready_controls` in `apps/worker/tests/test_voice_runner.py`, commit the capture JSON (after checking it holds no personal text), then run `pytest apps/worker/tests/test_voice_runner.py`.
8. Run the human two-way demo (ChatGPT and Gemini) and record the probe output and the demo result in PROGRESS.md as the ledger's remaining gates. Until then the shipped selectors stay empty and the runner keeps reporting `unverified_ready`; guessing selectors is forbidden.

## What is and is not evidenced

- Evidenced (2026-10-05 pre-call captures): ChatGPT entry control `Start Voice` (and `Dictate`, STT only, never clicked). The Gemini capture never listed `Listen`; the runner reports drift against that capture (covered by `CapturedDomTests`).
- Not evidenced: any in-call DOM for either provider. This slice built the tooling only; no live probe was run.

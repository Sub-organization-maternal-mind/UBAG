---
title: AI Provider Rollout
description: Rollout plan for first-party web AI target adapters.
---

# AI Provider Rollout

The blueprint names multiple web AI targets, but Milestone 0 should prevent the project from treating them as identical. Each provider adapter must graduate through the same gates: mock parity, health detection, login state detection, core prompt flow, artifact capture, drift baseline, and canary metrics.

## Day 1 adapter set

| Adapter | Initial scope | Login posture | Notes |
|---|---|---|---|
| `mock` | Local deterministic target for tests. | None. | First adapter; validates the worker and contract. |
| `generic_chat` | Config-driven chat UI with selectors and URLs in YAML. | Manual login required when the target is not public. | Covers simple chat sites and helps prove manifest-driven behavior. |
| `generic_form` | Config-driven form fill and extraction. | Manual login required when the target is not public. | Supports non-AI portals and internal tools. |
| `deepseek_web` | Prompt run, prompt stream, conversation resume. | Manual login required. | First real AI web target for Phase 3 MVP. |
| `claude_web` | Prompt stream, conversation resume, file upload later. | Manual login required. | Useful for critique or second-pass workflows. |
| `chatgpt_web` | Prompt run, prompt stream, conversation resume. | Manual login required. | Requires careful account and policy handling. |
| `gemini_web` | Prompt run and prompt stream. | Manual login required. | Scope file and app integrations separately. |
| `mistral_lechat` | Prompt run and prompt stream. | Manual login required. | Keep model/provider UI variants explicit. |
| `perplexity_web` | Prompt run with citation-style output. | Manual login required. | Normalize answer and source sections separately. |

No adapter is considered production-ready because it exists in the list. Each one must pass rollout gates.

## Graduation stages

| Stage | Gate | Required evidence |
|---|---|---|
| `planned` | Manifest exists. | Supported commands, capabilities, resource policy, and safety limitations are declared. |
| `mocked` | Stub target passes. | Adapter can operate a deterministic local page with the same semantic actions. |
| `healthcheck` | Public target health is detectable. | `health_check` distinguishes reachable, changed, down, and blocked states. |
| `login-aware` | Login state is safe. | `ensure_logged_in` returns explicit states without credential logging. |
| `single-job` | One prompt completes. | Output is normalized and artifacts are captured under policy. |
| `streaming` | Incremental events work if capability is declared. | Tokens or chunks include ordering and final completion markers. |
| `drift-baselined` | Key DOM snapshots are stored. | Baselines exist for home, prompt-ready, running, completed, and error states. |
| `canary` | Limited live traffic. | Success rate, p50/p99, output length, selector fallback, and manual-action metrics are tracked. |
| `stable` | Rollback and runbook are ready. | Previous version is known, alerts are wired, and operator docs exist. |

## Rollout policy

```yaml
adapter_rollout:
  target: deepseek_web
  candidate_version: 0.2.0
  previous_stable_version: 0.1.0
  canary_percent: 5
  promote_after:
    min_jobs: 100
    min_duration_minutes: 60
    success_rate_at_least: 0.98
    selector_fallback_rate_below: 0.02
    manual_action_rate_below: 0.05
  rollback_on:
    terminal_failure_rate_at_least: 0.05
    drift_score_at_least: 0.7
    captcha_rate_at_least: 0.1
    p99_duration_seconds_at_least: 120
```

The exact thresholds can change by target. The shape is the contract: canary, compare, promote, rollback, and audit.

## Provider capability matrix

| Capability | Mock | Generic chat | DeepSeek | Claude | ChatGPT | Gemini | Mistral | Perplexity |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Prompt run | Yes | Yes | Yes | Yes | Yes | Yes | Yes | Yes |
| Streaming | Yes | Optional | Yes | Yes | Yes | Yes | Yes | Optional |
| Conversation resume | Yes | Optional | Yes | Yes | Yes | Later | Later | Later |
| File upload | Yes | Optional | Later | Later | Later | Later | Later | Later |
| Downloads | Yes | Optional | Later | Later | Later | Later | Later | Later |
| Manual login | No | Yes | Yes | Yes | Yes | Yes | Yes | Yes |
| Drift baseline | Yes | Yes | Yes | Yes | Yes | Yes | Yes | Yes |

`Later` means not part of the first stable adapter contract for that target. It must not be silently implemented outside the manifest.

## Safe rollout rules

- Start with `mock`, then `generic_chat`, then one real provider.
- A real provider adapter cannot enter canary without login detection and drift baseline.
- A provider adapter cannot be stable without rollback evidence.
- File upload and downloads require separate data classification and artifact handling review.
- Provider-specific account constraints are represented as policy, not hard-coded comments.
- Any target policy or UI change that affects allowed automation forces the adapter back to `login-aware` or `drift-baselined` review.

## Milestone 0 acceptance

- First implementation milestone creates `mock` before real provider adapters.
- `deepseek_web` is the first real adapter candidate unless product direction changes.
- Each provider ticket includes manifest, login, prompt flow, drift baseline, artifact capture, and canary metrics.

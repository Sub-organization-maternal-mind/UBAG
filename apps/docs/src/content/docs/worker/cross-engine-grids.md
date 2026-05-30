---
title: Cross-Engine And Remote Browser Grids
description: Pluggable browser engines (Chromium CDP, Firefox/WebKit BiDi), remote browser grids, and engine-portable selectors.
---

# Cross-Engine And Remote Browser Grids

UBAG is not tied to a single browser engine or a single host. The worker abstracts the engine and the location of the browser so the same user-owned automation runs on Chromium, Firefox, or WebKit, locally or on a remote grid.

This page covers blueprint sections §13.10–§13.12.

## Pluggable browser engine (§13.10)

The worker drives browsers through a thin engine abstraction rather than a single hard-coded driver.

| Engine | Protocol | Notes |
|---|---|---|
| Chromium | CDP (Chrome DevTools Protocol) | Default engine for most provider surfaces. |
| Firefox | WebDriver BiDi | Used where a provider behaves better on Gecko. |
| WebKit | WebDriver BiDi | Used for Safari-like surfaces and engine-diversity coverage. |

The engine is selected per provider context. The Browser Topology dashboard panel shows the active engine for each tab (for example `Chromium (CDP)`, `Firefox (BiDi)`, `WebKit (BiDi)`).

Engine selection never changes the ownership or safety contract — the same manual-action, drift, and consent rules apply on every engine.

## Remote browser grids (§13.11)

Browser instances can run locally inside the worker or on a remote grid (a pool of browser hosts reachable over the engine protocol).

- The worker connects to a remote engine endpoint instead of launching a local process.
- Provider contexts and channel-tab pools work identically against a remote instance.
- Remote grids let a deployment scale tab capacity horizontally without changing job contracts.
- Grid endpoints are operator-configured infrastructure; clients never supply a raw browser or noVNC endpoint in a job payload.

Storage state for remote instances is still treated as user-owned and isolated per context. It is never returned to clients as a URI — only the boolean `has_storage_state` indicator is exposed.

## Engine-portable selectors (§13.12)

Adapter selectors are written to survive an engine swap. The worker resolves elements through an engine-portable layer so the same adapter step works on CDP and BiDi.

- Selectors target stable, semantic anchors (roles, labels, accessible names, structural landmarks) rather than engine-specific internals.
- The engine abstraction normalizes locator behavior, waiting, and event dispatch across CDP and BiDi.
- When a selector cannot be resolved portably, the adapter raises a drift signal instead of guessing, and the tab moves to `quarantined` for review.

This keeps adapters portable: a provider adapter validated on Chromium can run on Firefox or WebKit without rewriting its selectors.

## Safety stance

Cross-engine and remote-grid execution does not relax any safety rule:

- per-account ownership and consent context still travel with every job,
- CAPTCHA, login, and 2FA still escalate to a human via manual-action alerts,
- adaptive concurrency (AIMD) ceilings still apply per provider and identity,
- no credential, cookie, or storage-state URI is ever exposed by the engine or grid layer.

"""Live manual-session browser automation engine for UBAG provider adapters.

This package contains the shared, provider-agnostic machinery that powers the
real Playwright-driven adapters (``chatgpt_web``, ``claude_web``,
``deepseek_web``, ``gemini_web``, ``mistral_lechat``, ``perplexity_web``).

Design constraints (see UBAG_World_Class_Blueprint_v2.md §13):

* Manual login only. The engine never fills credentials, reads cookies/tokens
  from the job payload, or solves CAPTCHAs. When a target is not authenticated
  it emits ``session.manual_action_required`` with a loopback noVNC placeholder
  URL and waits for a user-owned browser session.
* User-owned persistent profiles only (Chromium ``--user-data-dir``).
* Deterministic offline mode (``UBAG_ADAPTER_OFFLINE=1`` or an injected
  :class:`MockPageDriver`) so unit tests validate selector config, event
  emission, and the manual-session flow without a real browser or network.
"""

from __future__ import annotations

from .engine import LiveSessionEngine, LiveSessionError
from .orchestrator import ConcurrencyState, LiveLease, LiveOrchestrator
from .page_driver import (
    DriftDetectedError,
    ManualActionRequired,
    ManualLoginTimeout,
    MockPageDriver,
    PageDriver,
    PlaywrightPageDriver,
    create_default_driver,
    offline_mode_enabled,
)
from .selectors import ProviderSelectors, SelectorGroup, get_provider_selectors, live_web_template, GENERIC_LIVE_WEB

__all__ = [
    "ConcurrencyState",
    "DriftDetectedError",
    "LiveLease",
    "LiveOrchestrator",
    "LiveSessionEngine",
    "LiveSessionError",
    "ManualLoginTimeout",
    "ManualActionRequired",
    "MockPageDriver",
    "PageDriver",
    "PlaywrightPageDriver",
    "ProviderSelectors",
    "SelectorGroup",
    "GENERIC_LIVE_WEB",
    "create_default_driver",
    "get_provider_selectors",
    "live_web_template",
    "offline_mode_enabled",
]

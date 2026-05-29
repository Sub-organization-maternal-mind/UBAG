"""Perplexity Web adapter.

The default ``run``/``iter_events`` entrypoint stays fail-closed (it raises
``NotImplementedError``) so the gateway/registry safe path is preserved. Real
Playwright-driven manual-session automation lives behind ``run_live`` /
``iter_live_events`` and only executes when a :class:`PageDriver` is injected or
offline mode (``UBAG_ADAPTER_OFFLINE=1``) is enabled. UBAG never collects
credentials, scrapes cookies, or solves CAPTCHAs.
"""

from __future__ import annotations

from typing import Any, Iterable, List, Mapping, Optional

_PROVIDER_ID = "perplexity_web"
_SCAFFOLD_MESSAGE = (
    "Perplexity Web adapter is a safe-mode scaffold only. It requires a "
    "user-owned manual browser session runtime; UBAG will not collect "
    "credentials, scrape cookies, or solve CAPTCHA challenges. Use run_live() "
    "with an injected page driver (or UBAG_ADAPTER_OFFLINE=1) for live automation."
)


class PerplexityWebAdapter:
    """Perplexity Web manual-session adapter."""

    name = "perplexity_web"
    version = "0.1.0"
    status = "stub"

    def iter_events(self, payload: Mapping[str, Any]) -> Iterable[Mapping[str, Any]]:
        raise NotImplementedError(_SCAFFOLD_MESSAGE)

    def run(self, payload: Mapping[str, Any]) -> List[Mapping[str, Any]]:
        return list(self.iter_events(payload))

    def iter_live_events(
        self, payload: Mapping[str, Any], *, driver: Optional[Any] = None
    ) -> Iterable[Mapping[str, Any]]:
        from ubag_worker.live import LiveSessionEngine
        from ubag_worker.live.selectors import get_provider_selectors

        engine = LiveSessionEngine(get_provider_selectors(_PROVIDER_ID))
        return engine.iter_events(payload, driver=driver)

    def run_live(
        self, payload: Mapping[str, Any], *, driver: Optional[Any] = None
    ) -> List[Mapping[str, Any]]:
        return list(self.iter_live_events(payload, driver=driver))


def build_perplexity_web_events(payload: Mapping[str, Any]) -> List[Mapping[str, Any]]:
    return PerplexityWebAdapter().run(payload)


def build_perplexity_web_live_events(
    payload: Mapping[str, Any], *, driver: Optional[Any] = None
) -> List[Mapping[str, Any]]:
    return PerplexityWebAdapter().run_live(payload, driver=driver)

"""Voice runner — activates a provider's live-voice UI for a voice session.

The voice.release counterpart to the job engine: where the engine submits a
text prompt, the voice runner clicks the provider's voice entry control so
the provider's own voice mode starts listening on the browser container's
virtual microphone (fed by the gateway's audio relay) and speaking through
the monitored sink. Audio never flows through this process — it only
activates the UI and reports what happened, keeping the provider interaction
surface identical to the engine's safe-mode posture:

- never logs in, never types credentials, never solves CAPTCHAs;
- clicks ONLY the verified voice entry control (never "Dictate", which is
  speech-to-text, and never a text submission);
- a login wall fails closed with ``manual_login_required``.

Usage (worker-side, run by the voice session orchestrator):
    python -m ubag_worker.voice.voice_runner \
        --provider chatgpt_web --cdp http://172.28.0.10:9223 \
        [--json]

Exit codes: 0 activated, 3 manual login required, 4 voice control drifted,
2 usage/connection errors. ``--json`` prints one structured result object.
"""

from __future__ import annotations

import argparse
import json
import sys
import time
from dataclasses import dataclass, field
from typing import Any, List, Optional

from ..live.selectors import PROVIDER_SELECTORS, ProviderSelectors

# Providers whose manifests declare voice.live (mirrors adapters/*/manifest.json;
# the gateway's /v1/capabilities publishes the same set).
VOICE_PROVIDERS = ("chatgpt_web", "gemini_web")


@dataclass
class VoiceActivationResult:
    provider: str
    activated: bool
    reason: str
    clicked_selector: Optional[str] = None
    login_required: bool = False
    tab_url: Optional[str] = None
    elapsed_ms: int = 0
    details: dict = field(default_factory=dict)

    def as_dict(self) -> dict:
        return {
            "provider": self.provider,
            "activated": self.activated,
            "reason": self.reason,
            "clicked_selector": self.clicked_selector,
            "login_required": self.login_required,
            "tab_url": self.tab_url,
            "elapsed_ms": self.elapsed_ms,
            "details": self.details,
        }


class VoiceControlDrift(Exception):
    """Raised when no voice entry control candidate matched the live DOM."""


def resolve_selectors(provider_id: str) -> ProviderSelectors:
    try:
        return PROVIDER_SELECTORS[provider_id]
    except KeyError as exc:  # pragma: no cover - defensive
        raise ValueError(f"unknown provider {provider_id!r}") from exc


def find_provider_tab(cdp_client: Any, provider_id: str) -> Optional[Any]:
    """Return the provider's page/tab, or None when none is open."""
    for page in cdp_client.pages():
        if provider_id in (page.url or "") or _matches_home(page.url, provider_id):
            return page
    return None


_HOME_PREFIX = {
    "chatgpt_web": "https://chatgpt.com",
    "gemini_web": "https://gemini.google.com",
}


def _matches_home(url: Optional[str], provider_id: str) -> bool:
    if not url:
        return False
    home = _HOME_PREFIX.get(provider_id)
    return bool(home) and url.startswith(home)


def click_voice_control(page: Any, selectors: ProviderSelectors) -> str:
    """Click the provider's voice entry control; return the selector used.

    Only the first candidate whose element is visible AND whose aria-label is
    NOT a dictate control is clicked. ``VoiceControlDrift`` when none match.
    """
    group = selectors.voice_control
    if group is None:
        raise VoiceControlDrift(f"provider {selectors.provider_id} declares no voice control")
    for selector in group.as_list():
        element = page.query_visible_element(selector)
        if element is None:
            continue
        label = (element.aria_label or "").strip().lower()
        if "dictate" in label:
            continue  # speech-to-text, never the live-voice entry
        element.click()
        return selector
    raise VoiceControlDrift(
        "no voice control candidate matched; selectors=%s" % group.as_list()
    )


LOGIN_SIGNAL_SELECTORS = (
    "[aria-label*='Sign up']",
    "[aria-label*='Log in']",
    "button[data-testid='login-button']",
)


def login_wall_present(page: Any) -> bool:
    for selector in LOGIN_SIGNAL_SELECTORS:
        if page.query_visible_element(selector) is not None:
            return True
    return False


def activate_voice(
    cdp_client: Any,
    provider_id: str,
    settle_ms: int = 1500,
    now: Optional[float] = None,
) -> VoiceActivationResult:
    """Attach, verify the session, click the voice control, report.

    ``cdp_client`` is duck-typed (pages()/new_page(url)/query_visible_element)
    so tests inject a fake browser; the production implementation wraps the
    same CDP attach the live engine uses.
    """
    started = time.monotonic() if now is None else now
    selectors = resolve_selectors(provider_id)
    if provider_id not in VOICE_PROVIDERS:
        return VoiceActivationResult(
            provider=provider_id, activated=False,
            reason="voice_not_supported_by_target",
            elapsed_ms=int((time.monotonic() - started) * 1000),
        )

    page = find_provider_tab(cdp_client, provider_id)
    opened_tab = False
    if page is None:
        page = cdp_client.new_page(selectors.target_url)
        opened_tab = True

    result_details = {"opened_tab": opened_tab}
    if login_wall_present(page):
        return VoiceActivationResult(
            provider=provider_id, activated=False,
            reason="manual_login_required", login_required=True,
            tab_url=page.url,
            details=result_details,
            elapsed_ms=int((time.monotonic() - started) * 1000),
        )
    try:
        clicked = click_voice_control(page, selectors)
    except VoiceControlDrift as exc:
        return VoiceActivationResult(
            provider=provider_id, activated=False,
            reason="selector_drift_detected",
            tab_url=page.url,
            details={**result_details, "error": str(exc)},
            elapsed_ms=int((time.monotonic() - started) * 1000),
        )
    time.sleep(settle_ms / 1000.0)
    return VoiceActivationResult(
        provider=provider_id, activated=True,
        reason="voice_activated", clicked_selector=clicked,
        tab_url=page.url,
        details=result_details,
        elapsed_ms=int((time.monotonic() - started) * 1000),
    )


class PlaywrightCdpClient:
    """Duck-typed CDP client over Playwright's connect_over_cdp.

    Wraps the same attach path the live engine's page driver uses (http(s)
    endpoint => connect_over_cdp against the browser container's private CDP
    proxy). pages() yields already-open tabs; new_page() opens one at url.
    """

    def __init__(self, endpoint: str):
        from playwright.sync_api import sync_playwright

        self._pw = sync_playwright().start()
        self._browser = self._pw.chromium.connect_over_cdp(endpoint)
        self._context = self._browser.contexts[0] if self._browser.contexts else self._browser.new_context()

    def pages(self) -> List["PlaywrightPage"]:
        return [PlaywrightPage(page) for page in self._context.pages]

    def new_page(self, url: str) -> "PlaywrightPage":
        return PlaywrightPage(self._context.new_page(), url_override=url)

    def close(self) -> None:
        try:
            self._browser.close()
        finally:
            self._pw.stop()


class PlaywrightPage:
    def __init__(self, page: Any, url_override: Optional[str] = None):
        self._page = page
        self.url = url_override if url_override is not None else page.url

    def query_visible_element(self, selector: str) -> Optional[Any]:
        locator = self._page.locator(selector).first
        try:
            if locator.count() == 0 or not locator.is_visible():
                return None
        except Exception:
            return None
        return PlaywrightElement(locator)

    def snapshot(self) -> str:
        return self._page.url


class PlaywrightElement:
    def __init__(self, locator: Any):
        self._locator = locator
        try:
            self.aria_label = locator.get_attribute("aria-label") or ""
        except Exception:
            self.aria_label = ""

    def click(self) -> None:
        self._locator.click()


def main(argv: Optional[List[str]] = None) -> int:
    parser = argparse.ArgumentParser(description="Activate a provider's live voice UI")
    parser.add_argument("--provider", required=True, choices=VOICE_PROVIDERS)
    parser.add_argument("--cdp", required=True, help="CDP HTTP endpoint (browser container proxy)")
    parser.add_argument("--json", action="store_true", dest="as_json")
    args = parser.parse_args(argv)

    try:
        client = PlaywrightCdpClient(args.cdp)
    except Exception as exc:  # connection failure is a clean 2
        result = VoiceActivationResult(provider=args.provider, activated=False,
                                       reason=f"cdp_attach_failed: {exc}")
        if args.as_json:
            print(json.dumps(result.as_dict()))
        return 2

    try:
        result = activate_voice(client, args.provider)
    finally:
        client.close()
    if args.as_json:
        print(json.dumps(result.as_dict()))
    else:
        print(f"{args.provider}: {result.reason} ({result.elapsed_ms} ms)")
    if not result.activated:
        return 3 if result.login_required else 4
    return 0


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())

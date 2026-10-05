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
        [--action activate|deactivate] [--context INDEX] [--json]

``--context`` selects the browser context (zero-based index) holding the
authenticated session. Without it the first context is used ONLY when exactly
one exists; zero or several contexts fail closed with ``attach_failed``.

Result ``state`` and exit codes (``--json`` prints one structured object):

    activated         0   in-call controls observed (needs ready_controls selectors)
    deactivated       0   provider tab navigated home, voice-active state gone
    attach_failed     2   CDP attach / context choice / navigation failed (also usage)
    login_wall        3   manual login required
    selector_drift    4   voice entry control not found
    voice_unavailable 5   provider/account has no voice mode
    setup_prompt      6   onboarding or permission dialog blocks the call
    timeout           7   readiness (or teardown) not observed within the deadline
    unverified_ready  8   clicked, but no ready_controls evidence is configured
    no_tab            9   deactivate found no open provider tab

Deactivation never guesses selectors: it navigates the provider tab back to
its provider home URL, which tears down the page's media session.
"""

from __future__ import annotations

import argparse
import json
import sys
import time
from dataclasses import dataclass, field
from typing import Any, List, Optional
from urllib.parse import urlsplit

from ..live.selectors import PROVIDER_SELECTORS, ProviderSelectors

# Providers whose manifests declare voice.live (mirrors adapters/*/manifest.json;
# the gateway's /v1/capabilities publishes the same set).
VOICE_PROVIDERS = ("chatgpt_web", "gemini_web")

# Explicit result states (see module docstring for the exit-code mapping).
ACTIVATED = "activated"
DEACTIVATED = "deactivated"
LOGIN_WALL = "login_wall"
SELECTOR_DRIFT = "selector_drift"
VOICE_UNAVAILABLE = "voice_unavailable"
SETUP_PROMPT = "setup_prompt"
TIMEOUT = "timeout"
UNVERIFIED_READY = "unverified_ready"
ATTACH_FAILED = "attach_failed"
NO_TAB = "no_tab"

EXIT_CODES = {
    ACTIVATED: 0, DEACTIVATED: 0, ATTACH_FAILED: 2, LOGIN_WALL: 3,
    SELECTOR_DRIFT: 4, VOICE_UNAVAILABLE: 5, SETUP_PROMPT: 6,
    TIMEOUT: 7, UNVERIFIED_READY: 8, NO_TAB: 9,
}

NAV_TIMEOUT_MS = 15000


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
    state: str = ""

    def __post_init__(self) -> None:
        if not self.state:  # direct constructions that predate explicit states
            self.state = ACTIVATED if self.activated else ATTACH_FAILED

    def as_dict(self) -> dict:
        return {
            "provider": self.provider,
            "state": self.state,
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
        if _matches_home(page.url, provider_id):
            return page
    return None


# Exact https hosts per provider. No subdomain wildcard: neither provider's
# voice UI lives on a subdomain, and suffix matching is how look-alike hosts
# (chatgpt.com.evil.example) slip through.
_ALLOWED_HOSTS = {
    "chatgpt_web": frozenset({"chatgpt.com"}),
    "gemini_web": frozenset({"gemini.google.com"}),
}


def _matches_home(url: Optional[str], provider_id: str) -> bool:
    """True only for an https URL whose parsed host is the provider's own."""
    hosts = _ALLOWED_HOSTS.get(provider_id)
    if not url or not hosts:
        return False
    try:
        parts = urlsplit(url)
        port = parts.port
    except ValueError:
        return False
    return (
        parts.scheme == "https"
        and parts.hostname in hosts
        and port in (None, 443)
        and parts.username is None  # reject userinfo tricks (https://chatgpt.com@evil.example/)
        and parts.password is None
    )


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


def _any_visible(page: Any, selectors: Any) -> bool:
    return any(page.query_visible_element(sel) is not None for sel in selectors)


def _result(provider_id: str, state: str, reason: str, started: float, page: Any = None,
            **kwargs: Any) -> VoiceActivationResult:
    return VoiceActivationResult(
        provider=provider_id, activated=state == ACTIVATED, state=state, reason=reason,
        tab_url=page.url if page is not None else None,
        elapsed_ms=int((time.monotonic() - started) * 1000), **kwargs,
    )


def activate_voice(
    cdp_client: Any,
    provider_id: str,
    settle_ms: int = 1500,
    now: Optional[float] = None,
    ready_timeout_ms: int = 10000,
    poll_ms: int = 250,
) -> VoiceActivationResult:
    """Attach, verify the session, click the voice control, verify readiness.

    ``cdp_client`` is duck-typed (pages()/new_page(url)/query_visible_element)
    so tests inject a fake browser; the production implementation wraps the
    same CDP attach the live engine uses. Readiness is read from the provider's
    ``voice_readiness`` selectors: ``activated`` only when an in-call control is
    visible; with none configured the click is reported as ``unverified_ready``.
    """
    started = time.monotonic() if now is None else now
    selectors = resolve_selectors(provider_id)
    if provider_id not in VOICE_PROVIDERS:
        return _result(provider_id, VOICE_UNAVAILABLE, "voice_not_supported_by_target", started)

    page = find_provider_tab(cdp_client, provider_id)
    opened_tab = page is None
    if page is None:
        try:
            page = cdp_client.new_page(selectors.target_url)
        except Exception as exc:  # navigation timeout / CDP error: nothing was clicked
            return _result(provider_id, ATTACH_FAILED, "provider_tab_unavailable", started,
                           details={"error": str(exc)})
    result_details = {"opened_tab": opened_tab}
    if not _matches_home(page.url, provider_id):
        # e.g. a fresh tab redirected off-provider; never click on a foreign page.
        return _result(provider_id, ATTACH_FAILED, "provider_url_mismatch", started, page,
                       details=result_details)
    if login_wall_present(page):
        return _result(provider_id, LOGIN_WALL, "manual_login_required", started, page,
                       login_required=True, details=result_details)
    try:
        clicked = click_voice_control(page, selectors)
    except VoiceControlDrift as exc:
        return _result(provider_id, SELECTOR_DRIFT, "selector_drift_detected", started, page,
                       details={**result_details, "error": str(exc)})

    time.sleep(settle_ms / 1000.0)
    readiness = selectors.voice_readiness
    ready = tuple(readiness.ready_controls) if readiness else ()
    deadline = time.monotonic() + ready_timeout_ms / 1000.0
    while True:
        if readiness and _any_visible(page, readiness.setup_prompts):
            return _result(provider_id, SETUP_PROMPT, "voice_setup_prompt", started, page,
                           clicked_selector=clicked, details=result_details)
        if readiness and _any_visible(page, readiness.unavailable):
            return _result(provider_id, VOICE_UNAVAILABLE, "voice_unavailable_for_account", started,
                           page, clicked_selector=clicked, details=result_details)
        if not ready:
            return _result(
                provider_id, UNVERIFIED_READY, "no_ready_controls_configured", started, page,
                clicked_selector=clicked,
                details={**result_details,
                         "entry_control_still_visible": page.query_visible_element(clicked) is not None},
            )
        if _any_visible(page, ready):
            return _result(provider_id, ACTIVATED, "voice_activated", started, page,
                           clicked_selector=clicked, details=result_details)
        if time.monotonic() >= deadline:
            return _result(provider_id, TIMEOUT, "ready_controls_not_observed", started, page,
                           clicked_selector=clicked, details=result_details)
        time.sleep(poll_ms / 1000.0)


def deactivate_voice(
    cdp_client: Any,
    provider_id: str,
    now: Optional[float] = None,
    ready_timeout_ms: int = 5000,
    poll_ms: int = 250,
) -> VoiceActivationResult:
    """End live voice without guessing selectors: navigate the tab to provider home.

    Navigation tears down the page's media session. The voice-active state is
    then verified gone: provider home reached on the provider's own host and no
    ``ready_controls`` visible (when none are configured only the navigation
    itself can be verified, recorded in ``details.verified_by``).
    """
    started = time.monotonic() if now is None else now
    selectors = resolve_selectors(provider_id)
    if provider_id not in VOICE_PROVIDERS:
        return _result(provider_id, VOICE_UNAVAILABLE, "voice_not_supported_by_target", started)
    page = find_provider_tab(cdp_client, provider_id)
    if page is None:
        return _result(provider_id, NO_TAB, "no_provider_tab_open", started)
    try:
        page.goto(selectors.target_url)
    except Exception as exc:
        return _result(provider_id, ATTACH_FAILED, "navigation_failed", started, page,
                       details={"error": str(exc)})
    if not _matches_home(page.url, provider_id):
        return _result(provider_id, ATTACH_FAILED, "provider_url_mismatch", started, page)
    readiness = selectors.voice_readiness
    ready = tuple(readiness.ready_controls) if readiness else ()
    deadline = time.monotonic() + ready_timeout_ms / 1000.0
    while ready and _any_visible(page, ready):
        if time.monotonic() >= deadline:
            return _result(provider_id, TIMEOUT, "voice_still_active_after_navigation", started, page)
        time.sleep(poll_ms / 1000.0)
    return _result(provider_id, DEACTIVATED, "voice_deactivated", started, page,
                   details={"verified_by": "ready_controls_absent" if ready else "navigation_only"})


class ContextSelectionError(Exception):
    """No single, unambiguous browser context could be chosen (fail closed)."""


def pick_context(contexts: List[Any], context_id: Optional[str] = None) -> Any:
    """Choose the authenticated browser context.

    ``context_id`` is a zero-based index. Unspecified: the first context is used
    ONLY when exactly one exists; with several, guessing could drive the wrong
    identity's session, so that is an error.
    """
    if context_id is None:
        if len(contexts) == 1:
            return contexts[0]
        raise ContextSelectionError(
            "no browser context" if not contexts
            else f"context_ambiguous: {len(contexts)} contexts open; pass --context"
        )
    try:
        index = int(context_id)
    except ValueError as exc:
        raise ContextSelectionError(f"context_not_found: {context_id!r} is not an index") from exc
    if not 0 <= index < len(contexts):
        raise ContextSelectionError(f"context_not_found: index {index} of {len(contexts)}")
    return contexts[index]


class PlaywrightCdpClient:
    """Duck-typed CDP client over Playwright's connect_over_cdp.

    Wraps the same attach path the live engine's page driver uses (http(s)
    endpoint => connect_over_cdp against the browser container's private CDP
    proxy). pages() yields already-open tabs; new_page() opens one AND
    navigates it to url.
    """

    def __init__(self, endpoint: str, context_id: Optional[str] = None):
        from playwright.sync_api import sync_playwright

        self._pw = sync_playwright().start()
        try:
            self._browser = self._pw.chromium.connect_over_cdp(endpoint)
            self._context = pick_context(list(self._browser.contexts), context_id)
        except Exception:
            self._pw.stop()
            raise

    def pages(self) -> List["PlaywrightPage"]:
        return [PlaywrightPage(page) for page in self._context.pages]

    def new_page(self, url: str) -> "PlaywrightPage":
        page = self._context.new_page()
        try:
            page.goto(url, timeout=NAV_TIMEOUT_MS, wait_until="domcontentloaded")
        except Exception:
            page.close()  # don't leave an about:blank tab behind
            raise
        return PlaywrightPage(page)

    def close(self) -> None:
        try:
            self._browser.close()
        finally:
            self._pw.stop()


class PlaywrightPage:
    def __init__(self, page: Any):
        self._page = page

    @property
    def url(self) -> str:
        return self._page.url  # live: reflects redirects and later navigations

    def goto(self, url: str) -> None:
        self._page.goto(url, timeout=NAV_TIMEOUT_MS, wait_until="domcontentloaded")

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
    parser = argparse.ArgumentParser(description="Activate or deactivate a provider's live voice UI")
    parser.add_argument("--provider", required=True, choices=VOICE_PROVIDERS)
    parser.add_argument("--cdp", required=True, help="CDP HTTP endpoint (browser container proxy)")
    parser.add_argument("--action", choices=("activate", "deactivate"), default="activate")
    parser.add_argument("--context", default=None,
                        help="browser context index holding the authenticated session")
    parser.add_argument("--json", action="store_true", dest="as_json")
    args = parser.parse_args(argv)

    try:
        client = PlaywrightCdpClient(args.cdp, context_id=args.context)
    except Exception as exc:  # connection / context-choice failure is a clean 2
        result = VoiceActivationResult(provider=args.provider, activated=False,
                                       state=ATTACH_FAILED,
                                       reason=f"cdp_attach_failed: {exc}")
    else:
        try:
            run = deactivate_voice if args.action == "deactivate" else activate_voice
            result = run(client, args.provider)
        finally:
            client.close()
    if args.as_json:
        print(json.dumps(result.as_dict()))
    else:
        print(f"{args.provider}: {result.state} - {result.reason} ({result.elapsed_ms} ms)")
    return EXIT_CODES[result.state]


if __name__ == "__main__":  # pragma: no cover
    sys.exit(main())

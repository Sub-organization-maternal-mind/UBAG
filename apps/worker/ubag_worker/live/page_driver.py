"""Page driver abstraction for live manual-session automation.

The engine never talks to Playwright directly. It drives a :class:`PageDriver`,
which has two concrete implementations:

* :class:`PlaywrightPageDriver` - real Chromium automation over a *user-owned*
  persistent profile (``--user-data-dir``). Playwright is imported lazily so the
  module (and the whole worker test-suite) imports cleanly without Playwright
  installed.
* :class:`MockPageDriver` - a fully deterministic, scripted driver used by unit
  tests and by the offline execution path (``UBAG_ADAPTER_OFFLINE=1``). It never
  touches a browser or the network.

Security invariants enforced here:

* No credentials, cookies, tokens, or storage-state are ever read from the job
  payload or written anywhere. Authentication relies solely on the user's
  persistent browser profile.
* CAPTCHAs / 2FA / login are handled by the human via the live (noVNC) session.
  The driver only *detects* login state; it never fills credentials.
"""

from __future__ import annotations

import os
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import Iterator, List, Mapping, Optional, Sequence

from .selectors import ProviderSelectors

# Login-state constants.
AUTHENTICATED = "authenticated"
LOGIN_REQUIRED = "login_required"
UNKNOWN = "unknown"


@dataclass(frozen=True)
class _LaunchPlan:
    """Resolved, Playwright-agnostic browser launch parameters."""

    browser_type_name: str
    remote_endpoint: Optional[str]
    headless: bool


class ManualLoginTimeout(RuntimeError):
    """Raised when a user-owned session is not authenticated within the window."""


class ManualActionRequired(RuntimeError):
    """Raised when provider UI needs human consent/verification before automation."""

    def __init__(self, reason: str, message: str) -> None:
        super().__init__(message)
        self.reason = reason


class DriftDetectedError(RuntimeError):
    """Raised when every fallback selector for a required group fails.

    Maps to the blueprint error code ``UBAG-ADAPTER-DRIFT-014``.
    """

    error_code = "UBAG-ADAPTER-DRIFT-014"

    def __init__(self, group_name: str, selector_version: str) -> None:
        super().__init__(
            "selector drift detected for group %r (baseline %s); all fallbacks failed"
            % (group_name, selector_version)
        )
        self.group_name = group_name
        self.selector_version = selector_version


class PageDriver(ABC):
    """High-level, selector-aware page operations used by the engine."""

    @abstractmethod
    def open(self, *, target_url: str, user_data_dir: str, headless: bool) -> None:
        """Open a user-owned persistent browser context and navigate to target."""

    @abstractmethod
    def current_url(self) -> str:
        ...

    @abstractmethod
    def detect_login_state(self, selectors: ProviderSelectors) -> str:
        """Return AUTHENTICATED / LOGIN_REQUIRED / UNKNOWN. Never fills creds."""

    @abstractmethod
    def await_manual_login(self, selectors: ProviderSelectors, *, timeout_s: float) -> str:
        """Block until the human completes login in the live session, or timeout."""

    @abstractmethod
    def submit_prompt(self, selectors: ProviderSelectors, prompt: str) -> None:
        ...

    @abstractmethod
    def stream_response(self, selectors: ProviderSelectors, *, timeout_s: float) -> Iterator[str]:
        """Yield response token deltas as they appear."""

    @abstractmethod
    def read_final_response(
        self, selectors: ProviderSelectors, *, return_mode: str = "final"
    ) -> str:
        ...

    @abstractmethod
    def dom_signature(self, selectors: ProviderSelectors) -> str:
        """Return a structural-only DOM signature (no text) for drift baselining."""

    @abstractmethod
    def capture_screenshot(self, label: str) -> Optional[str]:
        """Capture a screenshot artifact, returning its path (or None)."""

    @abstractmethod
    def close(self) -> None:
        ...


# ---------------------------------------------------------------------------
# Deterministic mock driver
# ---------------------------------------------------------------------------


@dataclass
class MockPageDriver(PageDriver):
    """Scripted, deterministic page driver for tests and offline mode.

    Parameters
    ----------
    authenticated:
        Initial login state. When ``False`` the engine emits
        ``session.manual_action_required`` and calls :meth:`await_manual_login`.
    login_after_wait:
        Whether the (simulated) human completes login during the wait window.
    response_text:
        Final assistant text returned by :meth:`read_final_response`.
    tokens:
        Streamed deltas. Defaults to a word-split of ``response_text``.
    drift_group:
        If set, the named selector group is treated as drifted, raising
        :class:`DriftDetectedError` when the engine tries to use it.
    screenshot_path:
        Value returned by :meth:`capture_screenshot` (artifact placeholder).
    """

    authenticated: bool = True
    login_after_wait: bool = True
    response_text: str = "ready"
    tokens: Optional[Sequence[str]] = None
    drift_group: Optional[str] = None
    screenshot_path: Optional[str] = "artifacts/mock-screenshot.png"
    opened: bool = field(default=False, init=False)
    closed: bool = field(default=False, init=False)
    submitted_prompt: Optional[str] = field(default=None, init=False)
    _url: str = field(default="about:blank", init=False)

    def open(self, *, target_url: str, user_data_dir: str, headless: bool) -> None:
        self.opened = True
        self._url = target_url

    def current_url(self) -> str:
        return self._url

    def _guard_drift(self, group_name: str, selector_version: str) -> None:
        if self.drift_group == group_name:
            raise DriftDetectedError(group_name, selector_version)

    def detect_login_state(self, selectors: ProviderSelectors) -> str:
        self._guard_drift(
            selectors.authenticated_signal.name, selectors.selector_version
        )
        return AUTHENTICATED if self.authenticated else LOGIN_REQUIRED

    def await_manual_login(self, selectors: ProviderSelectors, *, timeout_s: float) -> str:
        if self.login_after_wait:
            self.authenticated = True
            return AUTHENTICATED
        return LOGIN_REQUIRED

    def submit_prompt(self, selectors: ProviderSelectors, prompt: str) -> None:
        self._guard_drift(selectors.prompt_input.name, selectors.selector_version)
        self._guard_drift(selectors.submit_button.name, selectors.selector_version)
        self.submitted_prompt = prompt

    def stream_response(self, selectors: ProviderSelectors, *, timeout_s: float) -> Iterator[str]:
        self._guard_drift(
            selectors.response_container.name, selectors.selector_version
        )
        for token in self._resolved_tokens():
            yield token

    def read_final_response(
        self, selectors: ProviderSelectors, *, return_mode: str = "final"
    ) -> str:
        self._guard_drift(
            selectors.response_container.name, selectors.selector_version
        )
        return self.response_text

    def dom_signature(self, selectors: ProviderSelectors) -> str:
        return "mock-dom-signature:%s" % selectors.provider_id

    def capture_screenshot(self, label: str) -> Optional[str]:
        return self.screenshot_path

    def close(self) -> None:
        self.closed = True

    def _resolved_tokens(self) -> List[str]:
        if self.tokens is not None:
            return list(self.tokens)
        if not self.response_text:
            return [""]
        parts = self.response_text.split(" ")
        return [
            part + (" " if index < len(parts) - 1 else "")
            for index, part in enumerate(parts)
        ]


# ---------------------------------------------------------------------------
# Real Playwright driver (lazy import; never required for tests/offline)
# ---------------------------------------------------------------------------


class PlaywrightPageDriver(PageDriver):
    """Chromium automation over a user-owned persistent profile.

    Playwright is imported lazily inside :meth:`open` so this module imports
    cleanly in environments without Playwright (e.g. CI unit tests). A live run
    requires ``pip install playwright`` and ``playwright install chromium``.
    """

    def __init__(
        self,
        *,
        login_poll_interval_s: float = 2.0,
        response_settle_s: float = 1.0,
        engine_spec: Optional[object] = None,
    ) -> None:
        self._login_poll_interval_s = login_poll_interval_s
        self._response_settle_s = response_settle_s
        # Optional config-driven engine selection (§13.10/§13.11). Duck-typed to
        # avoid importing ``ubag_worker.live.engines`` here (engines imports this
        # module). Only ``.kind.value``, ``.is_remote`` and ``.headed`` are read.
        self._engine_spec = engine_spec
        self._playwright = None
        self._context = None
        self._owns_context = True
        self._page = None
        self._artifacts_dir: Optional[str] = None

    @staticmethod
    def _resolve_launch_plan(
        engine_spec: Optional[object], headless: bool
    ) -> "_LaunchPlan":
        """Resolve the concrete launch parameters from an optional engine spec.

        Pure (no Playwright, no I/O) so it is fully unit-testable offline. The
        default (``engine_spec is None``) yields the historical behavior: a local
        Chromium persistent context honoring the per-job ``headless`` flag. A
        config-driven spec selects the Playwright browser-type name, forces a
        headed launch when requested, and surfaces a remote-grid endpoint so the
        driver attaches over CDP/BiDi instead of launching locally (§13.11).
        """

        browser_type_name = "chromium"
        remote_endpoint: Optional[str] = None
        effective_headless = headless

        if engine_spec is not None:
            kind = getattr(engine_spec, "kind", None)
            kind_value = getattr(kind, "value", None)
            # "bidi" (vendor-neutral) launches via chromium locally; remote BiDi
            # targets are handled through the remote endpoint below.
            if kind_value in ("firefox", "webkit", "chromium"):
                browser_type_name = kind_value
            if getattr(engine_spec, "is_remote", False):
                remote_endpoint = getattr(engine_spec, "remote_endpoint", None) or None
            if getattr(engine_spec, "headed", False):
                effective_headless = False

        return _LaunchPlan(
            browser_type_name=browser_type_name,
            remote_endpoint=remote_endpoint,
            headless=effective_headless,
        )

    # -- lifecycle -------------------------------------------------------
    def open(self, *, target_url: str, user_data_dir: str, headless: bool) -> None:
        try:
            from playwright.sync_api import sync_playwright
        except ImportError as exc:  # pragma: no cover - requires real browser
            raise RuntimeError(
                "Playwright is not installed. For live runs install it with "
                "'pip install playwright' and 'playwright install chromium'. "
                "For tests/offline use UBAG_ADAPTER_OFFLINE=1 or inject a MockPageDriver."
            ) from exc

        if not user_data_dir:
            raise ValueError(
                "a user-owned persistent profile (user_data_dir) is required; "
                "UBAG never stores credentials or cookies"
            )

        plan = self._resolve_launch_plan(self._engine_spec, headless)
        self._playwright = sync_playwright().start()
        browser_type = getattr(self._playwright, plan.browser_type_name)
        if plan.remote_endpoint:  # pragma: no cover - requires a remote grid
            # Remote browser grid (§13.11): attach over CDP/BiDi. No credential,
            # cookie, or storage-state material is ever sent to the endpoint.
            #
            # UBAG_REMOTE_BROWSER_ENDPOINT is a Chrome DevTools Protocol HTTP
            # endpoint (e.g. http://browser-viewer:9222). Playwright requires
            # ``connect_over_cdp`` for CDP HTTP endpoints; ``connect`` is the
            # Playwright server WebSocket protocol and will fail against a raw CDP
            # endpoint. We detect CDP endpoints by their http(s):// scheme.
            if plan.remote_endpoint.startswith(("http://", "https://")):
                # CDP endpoint (e.g. http://browser-viewer:9222). Reuse the
                # browser's existing default context so the operator's live
                # login session is inherited. Creating a new_context() here
                # would discard all cookies and force re-login every job.
                browser = browser_type.connect_over_cdp(plan.remote_endpoint)
                if browser.contexts:
                    self._context = browser.contexts[0]
                    self._owns_context = False
                else:
                    self._context = browser.new_context()
                    self._owns_context = True
            else:
                browser = browser_type.connect(plan.remote_endpoint)
                self._context = browser.new_context()
                self._owns_context = True
        else:
            # User-owned persistent context. We do NOT inject storage_state,
            # cookies, or credentials - authentication lives entirely in the
            # user profile.
            self._context = browser_type.launch_persistent_context(
                user_data_dir=user_data_dir,
                headless=plan.headless,
            )
            self._owns_context = True
        self._page = (
            self._context.pages[0]
            if self._context.pages
            else self._context.new_page()
        )
        try:
            self._page.goto(target_url, wait_until="domcontentloaded")
        except Exception as exc:  # pragma: no cover - requires real browser
            # Provider SPAs can abort the initial document navigation after
            # restoring an already-authenticated tab or redirecting within the
            # same app. Keep that narrow case non-fatal; selector/login checks
            # below still decide whether the session is usable.
            if not _is_tolerable_navigation_abort(exc, self._page.url, target_url):
                raise

    def current_url(self) -> str:  # pragma: no cover - requires real browser
        return self._page.url if self._page else ""

    # -- selector helpers ------------------------------------------------
    def _first_visible(self, group, *, timeout_ms: int = 4000):  # pragma: no cover
        last_error: Optional[Exception] = None
        for candidate in group.as_list():
            try:
                locator = self._page.locator(candidate).first
                locator.wait_for(state="visible", timeout=timeout_ms)
                return locator
            except Exception as exc:  # noqa: BLE001 - try next fallback
                last_error = exc
                continue
        raise DriftDetectedError(group.name, group.baseline_version) from last_error

    def _present(self, group, *, timeout_ms: int = 3000) -> bool:  # pragma: no cover
        for candidate in group.as_list():
            try:
                self._page.locator(candidate).first.wait_for(
                    state="visible", timeout=timeout_ms
                )
                return True
            except Exception:  # noqa: BLE001
                continue
        return False

    # -- login state -----------------------------------------------------
    def detect_login_state(self, selectors: ProviderSelectors) -> str:  # pragma: no cover
        if self._present(selectors.authenticated_signal, timeout_ms=4000):
            return AUTHENTICATED
        if self._present(selectors.login_signal, timeout_ms=2000):
            return LOGIN_REQUIRED
        return UNKNOWN

    def await_manual_login(  # pragma: no cover - requires real browser + human
        self, selectors: ProviderSelectors, *, timeout_s: float
    ) -> str:
        import time

        deadline = time.monotonic() + timeout_s
        while time.monotonic() < deadline:
            if self._present(selectors.authenticated_signal, timeout_ms=1500):
                return AUTHENTICATED
            time.sleep(self._login_poll_interval_s)
        return LOGIN_REQUIRED

    # -- interaction -----------------------------------------------------
    def submit_prompt(self, selectors: ProviderSelectors, prompt: str) -> None:  # pragma: no cover
        field = self._first_visible(selectors.prompt_input)
        try:
            field.click()
        except Exception as exc:  # noqa: BLE001 - classify provider overlays.
            if _looks_like_manual_overlay(exc):
                raise ManualActionRequired(
                    "manual_consent_or_overlay_required",
                    "Provider UI is blocking the prompt field with a consent, "
                    "verification, or overlay prompt. Clear it manually in noVNC.",
                ) from exc
            raise
        field.fill(prompt)
        try:
            button = self._first_visible(selectors.submit_button, timeout_ms=3000)
            button.click()
        except DriftDetectedError:
            # Fallback: many composers submit on Enter.
            field.press("Enter")

    def stream_response(  # pragma: no cover - requires real browser
        self, selectors: ProviderSelectors, *, timeout_s: float
    ) -> Iterator[str]:
        import time

        container = self._first_visible(selectors.response_container, timeout_ms=int(timeout_s * 1000))
        deadline = time.monotonic() + timeout_s
        seen = ""
        last_growth = time.monotonic()
        # Poll the container text and yield only the new suffix as a delta. The
        # response is treated as complete once it is non-empty AND has stopped
        # growing for `_response_settle_s` AND the provider is not signalling active
        # streaming. The settle window (rather than relying solely on the streaming
        # indicator) is essential for providers such as Gemini Web that expose no
        # detectable streaming indicator: without it the loop would satisfy the old
        # `current == seen` condition on the very first read and capture a partial,
        # label-only response (e.g. just "Gemini said").
        while time.monotonic() < deadline:
            try:
                current = container.inner_text(timeout=2000)
            except Exception:  # noqa: BLE001
                current = seen
            if len(current) > len(seen):
                yield current[len(seen):]
                seen = current
                last_growth = time.monotonic()
            still_streaming = self._present(selectors.streaming_indicator, timeout_ms=500)
            settled = (time.monotonic() - last_growth) >= self._response_settle_s
            if seen.strip() and settled and not still_streaming:
                break
            time.sleep(0.4)

    def read_final_response(  # pragma: no cover
        self, selectors: ProviderSelectors, *, return_mode: str = "final"
    ) -> str:
        # Reasoning models (e.g. DeepSeek R1) render the chain-of-thought and the final
        # answer as separate nodes; response_container (.first) can latch the thinking
        # pane. When the job requested return_mode="final" and the provider declares an
        # explicit final_answer_container, read that authoritative node; fall back to
        # response_container only if the answer selector has drifted.
        if return_mode == "final" and selectors.final_answer_container is not None:
            try:
                answer = self._first_visible(selectors.final_answer_container)
                return answer.inner_text(timeout=4000)
            except DriftDetectedError:
                pass
        container = self._first_visible(selectors.response_container)
        return container.inner_text(timeout=4000)

    def dom_signature(self, selectors: ProviderSelectors) -> str:  # pragma: no cover
        # Structural-only signature (tag presence), never page text content.
        present = []
        for node in selectors.drift_signature_nodes:
            try:
                count = self._page.locator(node).count()
            except Exception:  # noqa: BLE001
                count = 0
            present.append("%s=%d" % (node, count))
        return ";".join(present)

    def capture_screenshot(self, label: str) -> Optional[str]:  # pragma: no cover
        if self._page is None:
            return None
        path = self._screenshot_path(label)
        try:
            self._page.screenshot(path=path, full_page=False)
        except Exception:  # noqa: BLE001
            return None
        return path

    def _screenshot_path(self, label: str) -> str:  # pragma: no cover
        base = self._artifacts_dir or os.environ.get("UBAG_ARTIFACTS_DIR", "artifacts")
        os.makedirs(base, exist_ok=True)
        safe_label = "".join(c if c.isalnum() or c in ("-", "_") else "_" for c in label)
        return os.path.join(base, "%s.png" % safe_label)

    def close(self) -> None:  # pragma: no cover - requires real browser
        try:
            if self._context is not None and self._owns_context:
                self._context.close()
        finally:
            if self._playwright is not None:
                self._playwright.stop()
            self._context = None
            self._owns_context = True
            self._page = None
            self._playwright = None


def offline_mode_enabled() -> bool:
    """True when deterministic offline execution is requested."""

    flag = os.environ.get("UBAG_ADAPTER_OFFLINE", "").strip().lower()
    if flag in ("1", "true", "yes", "on"):
        return True
    worker_offline = os.environ.get("UBAG_WORKER_OFFLINE", "").strip().lower()
    return worker_offline in ("1", "true", "yes", "on")


def create_default_driver(options: Optional[Mapping[str, object]] = None) -> PageDriver:
    """Build a driver based on environment + payload options.

    Returns a deterministic :class:`MockPageDriver` in offline mode, otherwise a
    real :class:`PlaywrightPageDriver`. The offline mock honors a small set of
    payload ``options`` so end-to-end runs stay deterministic in tests:
    ``offline_authenticated``, ``offline_login_after_wait``, ``offline_response``,
    ``offline_tokens``, ``offline_drift_group``.
    """

    if offline_mode_enabled():
        opts = options or {}
        tokens = opts.get("offline_tokens")
        return MockPageDriver(
            authenticated=bool(opts.get("offline_authenticated", True)),
            login_after_wait=bool(opts.get("offline_login_after_wait", True)),
            response_text=str(opts.get("offline_response", "ready")),
            tokens=list(tokens) if isinstance(tokens, (list, tuple)) else None,
            drift_group=(
                str(opts["offline_drift_group"])
                if opts.get("offline_drift_group")
                else None
            ),
        )
    # Config-driven engine selection (§13.10/§13.11). Imported lazily to avoid a
    # circular import (``engines`` imports this module). With no engine env vars
    # set this resolves to a local Chromium spec, preserving historical behavior.
    from .engines import engine_spec_from_env

    return PlaywrightPageDriver(engine_spec=engine_spec_from_env())


def _is_tolerable_navigation_abort(exc: Exception, current_url: str, target_url: str) -> bool:
    message = str(exc)
    if "net::ERR_ABORTED" not in message and "NS_BINDING_ABORTED" not in message:
        return False
    return _same_origin(current_url, target_url)


def _looks_like_manual_overlay(exc: Exception) -> bool:
    message = str(exc).lower()
    if "intercepts pointer events" not in message:
        return False
    overlay_markers = (
        "cookie",
        "consent",
        "captcha",
        "verification",
        "cdk-overlay",
        "overlay-backdrop",
    )
    return any(marker in message for marker in overlay_markers)


def _same_origin(left: str, right: str) -> bool:
    try:
        from urllib.parse import urlparse

        left_url = urlparse(left)
        right_url = urlparse(right)
        return bool(
            left_url.scheme
            and left_url.netloc
            and left_url.scheme == right_url.scheme
            and left_url.netloc == right_url.netloc
        )
    except Exception:
        return False


__all__ = [
    "AUTHENTICATED",
    "LOGIN_REQUIRED",
    "UNKNOWN",
    "DriftDetectedError",
    "ManualActionRequired",
    "ManualLoginTimeout",
    "MockPageDriver",
    "PageDriver",
    "PlaywrightPageDriver",
    "_is_tolerable_navigation_abort",
    "create_default_driver",
    "offline_mode_enabled",
]

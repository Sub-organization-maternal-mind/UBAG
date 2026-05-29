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


class ManualLoginTimeout(RuntimeError):
    """Raised when a user-owned session is not authenticated within the window."""


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
    def read_final_response(self, selectors: ProviderSelectors) -> str:
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

    def read_final_response(self, selectors: ProviderSelectors) -> str:
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
    ) -> None:
        self._login_poll_interval_s = login_poll_interval_s
        self._response_settle_s = response_settle_s
        self._playwright = None
        self._context = None
        self._page = None
        self._artifacts_dir: Optional[str] = None

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

        self._playwright = sync_playwright().start()
        # User-owned persistent context. We do NOT inject storage_state, cookies,
        # or credentials - authentication lives entirely in the user profile.
        self._context = self._playwright.chromium.launch_persistent_context(
            user_data_dir=user_data_dir,
            headless=headless,
        )
        self._page = (
            self._context.pages[0]
            if self._context.pages
            else self._context.new_page()
        )
        self._page.goto(target_url, wait_until="domcontentloaded")

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
        field.click()
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
        # Poll the container text and yield only the new suffix as a delta.
        while time.monotonic() < deadline:
            try:
                current = container.inner_text(timeout=2000)
            except Exception:  # noqa: BLE001
                current = seen
            if len(current) > len(seen):
                yield current[len(seen):]
                seen = current
            still_streaming = self._present(selectors.streaming_indicator, timeout_ms=500)
            if not still_streaming and current == seen:
                time.sleep(self._response_settle_s)
                break
            time.sleep(0.4)

    def read_final_response(self, selectors: ProviderSelectors) -> str:  # pragma: no cover
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
            if self._context is not None:
                self._context.close()
        finally:
            if self._playwright is not None:
                self._playwright.stop()
            self._context = None
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
    return PlaywrightPageDriver()


__all__ = [
    "AUTHENTICATED",
    "LOGIN_REQUIRED",
    "UNKNOWN",
    "DriftDetectedError",
    "ManualLoginTimeout",
    "MockPageDriver",
    "PageDriver",
    "PlaywrightPageDriver",
    "create_default_driver",
    "offline_mode_enabled",
]

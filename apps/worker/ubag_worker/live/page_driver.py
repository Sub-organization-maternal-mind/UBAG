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
import re
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import Iterator, List, Mapping, Optional, Sequence

from .selectors import ProviderSelectors

# Login-state constants.
AUTHENTICATED = "authenticated"
LOGIN_REQUIRED = "login_required"
UNKNOWN = "unknown"

# A provider chat id is a URL path segment (e.g. a ChatGPT UUID). delete_chat
# interpolates it into id-addressed selectors, so anything outside this charset
# is REFUSED rather than escaped: a quote or bracket could widen the selector to
# match other chats, and the delete it drives is permanent on a real account.
_SAFE_CONV_ID_RE = re.compile(r"^[A-Za-z0-9_-]{6,128}$")

# Settle window (seconds of no growth) before a response is considered complete.
# Reasoning modes (DeepThink / Extended thinking) pause mid-thought for seconds
# with no visible streaming indicator, so they need a wider window than a plain
# reply to avoid latching a partial answer. Env-overridable for tuning.
_REASONING_SETTLE_S = 4.0
# Warm-reuse emptiness probe budget. A presence check on an already-loaded page,
# so it is deliberately short; a false "absent" is caught downstream by drift
# detection rather than by waiting longer here.
_EMPTINESS_PROBE_MS = 1500

# Resume-confirmation budget. UNLIKE the emptiness probe, this WAITS for a prior
# turn to appear after navigating to a bound thread: providers (ChatGPT, Gemini)
# hydrate a /c/<id> conversation's messages via async JS several seconds AFTER
# domcontentloaded (measured ~6.6s for ChatGPT). Too short a wait misreads a
# still-loading thread as "gone" and wrongly breaks the binding.
_RESUME_CONFIRM_MS = 20000


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

    def wait_until_authenticated(
        self, selectors: ProviderSelectors, *, timeout_s: float
    ) -> str:
        """Poll only the authenticated marker, WITHOUT simulating a human login.

        Used as a short grace re-check so a slow cold page load is not mistaken
        for a logged-out session. Concrete (NOT abstract) so existing drivers keep
        working; the default is a single :meth:`detect_login_state` probe.
        """
        return self.detect_login_state(selectors)

    def login_signal_present(self, selectors: ProviderSelectors) -> bool:
        """True when a login form/prompt is actually on screen.

        Distinguishes a *genuinely logged-out* session (a sign-in form is visible)
        from a page that simply has not finished rendering its authenticated
        markers yet (heavy account, slow cold load). The engine uses this to avoid
        surfacing manual_action_required for an already-authenticated user.
        Concrete; the default is derived from :meth:`detect_login_state`.
        """
        return self.detect_login_state(selectors) == LOGIN_REQUIRED

    def reset(self, target_url: str) -> None:
        """Re-navigate to a clean target page before retrying the interaction.

        Concrete no-op by default; the live driver reloads the page so a transient
        mid-interaction browser/CDP hiccup retries from a fresh state instead of
        failing the job. Never raises.
        """
        return None

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

    def attach_files(
        self,
        selectors: ProviderSelectors,
        file_paths: Sequence[str],
        *,
        timeout_ms: int = 15000,
    ) -> None:
        """Attach local files to the provider's hidden ``<input type=file>``.

        Concrete (NOT abstract) on purpose: existing drivers and the abstract
        method set are untouched, so nothing that subclasses :class:`PageDriver`
        breaks. Drivers that support uploads override this; the default refuses
        loudly rather than silently transcribing nothing.
        """
        raise NotImplementedError(
            "page driver %s does not support file attachment" % type(self).__name__
        )

    def start_new_chat(self, selectors: ProviderSelectors) -> bool:
        """Start a fresh conversation, if the provider exposes a New-chat control.

        Concrete (NOT abstract) so existing drivers keep working untouched.
        Best-effort: returns ``True`` when a control was clicked, ``False`` when
        the provider declares no ``new_chat`` group or the control could not be
        found. A missing/renamed New-chat control must NOT fail an otherwise-good
        job, so this never raises ``DriftDetectedError``.
        """
        return False

    def delete_chat(self, selectors: ProviderSelectors, conv_id: str) -> bool:
        """PERMANENTLY delete one exact chat by its provider conversation id.

        Concrete (NOT abstract) so existing drivers keep working untouched;
        returns ``False`` by default, meaning "did not delete". Only the chat
        reaper calls this, and only with an id read back from the chat ledger —
        i.e. a chat UBAG recorded itself as creating. It must never be reachable
        from a job path: deletion on these providers is permanent, the accounts
        are real and human-owned, and the sidebar mixes our throwaway job chats
        with the operator's own work.

        Returns True only when the chat is VERIFIED gone afterwards, never merely
        because a click was dispatched.
        """
        return False

    def current_thread_url(self, selectors: ProviderSelectors) -> str:
        """Return the canonical provider chat-thread URL for the current page.

        Conversation affinity binds a caller-owned conversation key to this URL so
        a later job can resume the same chat. Concrete (NOT abstract) so existing
        drivers keep working: the base cannot know a real URL, so it returns ``""``
        ("no bindable thread"), which the engine treats as "emit no binding".

        A chat URL ONLY — never cookies, storage state, session ids, or noVNC URLs.
        """
        return ""

    def resume_thread(self, selectors: ProviderSelectors, thread_ref: str) -> bool:
        """Navigate to a previously-bound chat thread and confirm it loaded.

        Concrete (NOT abstract) so existing drivers keep working. Best-effort,
        mirroring :meth:`start_new_chat`'s posture: returns ``True`` only when the
        thread URL settled AND a prior conversation turn is present
        (:meth:`response_container_present`); returns ``False`` when it cannot
        confirm the thread. It NEVER raises ``DriftDetectedError`` — a vanished or
        renamed thread must not fail the job here; the engine decides fail vs.
        restart based on ``on_missing``. The base has no page, so it cannot resume.
        """
        return False

    def response_container_present(self, selectors: ProviderSelectors) -> bool:
        """True when a prior conversation turn is visible on the page.

        The warm-reuse emptiness probe. It deliberately reuses the already
        drift-baselined ``response_container`` group rather than a purpose-made
        turn/emptiness selector: ``ProviderSelectors`` declares no such group,
        and a *guessed* one is the worst possible outcome here -- it would
        silently match nothing, report "empty", and let a prior patient's report
        bleed into the next job.

        Concrete (NOT abstract) so existing drivers keep working. The default is
        ``True`` = "cannot prove this page is empty", so a driver without a real
        probe is never reused.

        Must not raise on drift: a drifted selector matches nothing and reads
        "absent" -- see :meth:`prepare_for_next_job` for why that is survivable.
        """
        return True

    def prepare_for_next_job(self, selectors: ProviderSelectors) -> bool:
        """Make a REUSED page safe for the next job, or refuse.

        Returns ``True`` only when the page is healthy AND provably shows no
        prior conversation turn. ``False`` means the caller MUST discard this
        driver and open a cold one -- i.e. today's per-job behaviour, which is
        always safe.

        Never raises: a cold rebuild is always available, so any doubt is
        reported as ``False`` rather than failing an otherwise-good job.

        Safety in every branch:
        - prior turn visible -> ``False`` -> cold rebuild.
        - provably empty     -> ``True``  -> submit into a genuinely fresh chat.
        - selector drifted   -> probe reads "absent" -> ``True`` -> submit, and
          the later :meth:`read_final_response` raises ``DriftDetectedError``
          against the same drifted group, so the job fails LOUDLY instead of
          returning whatever the previous turn left on screen.
        """
        try:
            self.reset(selectors.target_url)
            self.start_new_chat(selectors)
            return not self.response_container_present(selectors)
        except Exception:
            return False

    def ensure_provider_config(
        self,
        selectors: ProviderSelectors,
        overrides: Optional[Mapping[str, object]] = None,
    ) -> List[Mapping[str, object]]:
        """Idempotently enforce the provider's pre-submit settings.

        Reads the current UI state for each declared :class:`ProviderSetting`
        (model picker, mode pills, reasoning toggles) and only acts when it
        differs from the desired value, then re-verifies. Returns a list of
        per-setting result dicts (``key`` / ``desired`` / ``state``). A required
        setting that cannot be confirmed raises ``DriftDetectedError`` rather
        than submitting into the wrong mode. The default applies nothing.
        """
        return []


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
    # Setting keys reported as "already_set" (rest are reported "set"); lets a
    # test assert idempotency without a browser.
    config_already: Optional[Sequence[str]] = None
    # Whether a prior conversation turn is on the page (warm-reuse probe).
    response_container_visible: bool = False
    # Simulates a dead/broken page: the probe raises instead of answering.
    explode_on_probe: bool = False
    # Conversation affinity: the canonical chat-thread URL current_thread_url
    # reports (settable fake), and whether a resume_thread navigation is confirmed
    # as loaded (the live driver checks URL-settled + response_container_present).
    thread_url: str = ""
    resume_succeeds: bool = True
    # Chat reaper: conv_ids whose delete is simulated as NOT landing (the live
    # driver verifies the chat is gone rather than trusting the click).
    undeletable_chats: Sequence[str] = ()
    opened: bool = field(default=False, init=False)
    closed: bool = field(default=False, init=False)
    submitted_prompt: Optional[str] = field(default=None, init=False)
    attached_files: List[str] = field(default_factory=list, init=False)
    started_new_chat: bool = field(default=False, init=False)
    #: conv_ids delete_chat was asked to remove (assertable without a browser).
    deleted_chats: List[str] = field(default_factory=list, init=False)
    resumed_thread_ref: Optional[str] = field(default=None, init=False)
    ensured_settings: List[Mapping[str, object]] = field(default_factory=list, init=False)
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

    def wait_until_authenticated(
        self, selectors: ProviderSelectors, *, timeout_s: float
    ) -> str:
        # Grace re-check reports the CURRENT state only — it never simulates a
        # human login, so a not-yet-authenticated mock still routes to the manual
        # flow (mirrors a genuinely logged-out live session).
        self._guard_drift(
            selectors.authenticated_signal.name, selectors.selector_version
        )
        return AUTHENTICATED if self.authenticated else LOGIN_REQUIRED

    def login_signal_present(self, selectors: ProviderSelectors) -> bool:
        # A not-yet-authenticated mock models a genuinely logged-out session, so
        # the manual flow still triggers (mirrors a visible sign-in form).
        return not self.authenticated

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

    def attach_files(
        self,
        selectors: ProviderSelectors,
        file_paths: Sequence[str],
        *,
        timeout_ms: int = 15000,
    ) -> None:
        # Honor a configured drift on the file_input group so tests can assert the
        # drift path; otherwise record the attach so unit tests can verify it.
        if selectors.file_input is not None:
            self._guard_drift(selectors.file_input.name, selectors.selector_version)
        self.attached_files = list(file_paths)

    def response_container_present(self, selectors: ProviderSelectors) -> bool:
        if self.explode_on_probe:
            raise RuntimeError("page is gone")
        if self.drift_group == selectors.response_container.name:
            # Model drift the way it really behaves: the selector matches
            # nothing, so the probe CANNOT see the prior turn and reports
            # "absent" rather than raising. This is the dangerous read the gate
            # must survive; read_final_response is what fails loudly.
            return False
        return self.response_container_visible

    def start_new_chat(self, selectors: ProviderSelectors) -> bool:
        if selectors.new_chat is None:
            return False
        self._guard_drift(selectors.new_chat.name, selectors.selector_version)
        self.started_new_chat = True
        return True

    def delete_chat(self, selectors: ProviderSelectors, conv_id: str) -> bool:
        if selectors.delete_chat is None:
            return False
        self._guard_drift("delete_chat", selectors.selector_version)
        self.deleted_chats.append(conv_id)
        return conv_id not in self.undeletable_chats

    def current_thread_url(self, selectors: ProviderSelectors) -> str:
        return self.thread_url

    def resume_thread(self, selectors: ProviderSelectors, thread_ref: str) -> bool:
        # Model a navigation: the page URL settles on the bound thread. Whether the
        # thread is confirmed loaded is scripted via ``resume_succeeds`` (the live
        # driver checks URL-settled + response_container_present). Never raises, so
        # a scripted "gone" thread is reported as unresumable rather than failing.
        self.resumed_thread_ref = thread_ref
        self._url = thread_ref
        if self.resume_succeeds and thread_ref:
            self.thread_url = thread_ref
            return True
        return False

    def ensure_provider_config(
        self,
        selectors: ProviderSelectors,
        overrides: Optional[Mapping[str, object]] = None,
    ) -> List[Mapping[str, object]]:
        overrides = overrides or {}
        already = set(self.config_already or ())
        results: List[Mapping[str, object]] = []
        for setting in selectors.settings:
            # A configured drift on "setting:<key>" lets tests assert the blocked
            # path; otherwise the setting is reported set / already-set.
            self._guard_drift("setting:" + setting.key, selectors.selector_version)
            desired = overrides.get(setting.key, setting.desired)
            state = "already_set" if setting.key in already else "set"
            record = {"key": setting.key, "desired": desired, "state": state}
            results.append(record)
            self.ensured_settings.append(record)
        return results

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
        # True when we created a dedicated page inside a context we do NOT own
        # (the operator's shared CDP browser) and must close it ourselves.
        self._owns_page = False
        self._artifacts_dir: Optional[str] = None
        # Per-candidate count of response containers present just BEFORE the
        # current prompt was submitted. On a resumed multi-turn thread the page
        # already shows prior assistant turns; this baseline lets the response
        # reader wait for THIS turn's node and skip the earlier ones. Captured in
        # submit_prompt, consumed by stream_response. Empty on a fresh chat.
        self._response_baseline: dict[str, int] = {}

    @staticmethod
    def _cdp_attach_attempts() -> int:
        """How many times to retry the CDP attach (rides out a watchdog restart)."""

        raw = os.environ.get("UBAG_CDP_ATTACH_ATTEMPTS", "").strip()
        if raw.isdigit() and int(raw) > 0:
            return int(raw)
        return 5

    def _connect_over_cdp_resilient(self, browser_type, endpoint):  # pragma: no cover - requires browser
        """Attach over CDP, retrying briefly if the browser is mid-restart.

        The live browser-viewer supervises Chromium and relaunches it on crash;
        during that ~few-second window a CDP attach fails. Retrying turns a
        transient relaunch into a short delay instead of a hard job failure.
        """

        import time as _time

        attempts = self._cdp_attach_attempts()
        last_exc: Optional[Exception] = None
        for index in range(attempts):
            try:
                return browser_type.connect_over_cdp(endpoint)
            except Exception as exc:  # noqa: BLE001 - browser may be relaunching
                last_exc = exc
                if index == attempts - 1:
                    break
                _time.sleep(min(2.0, 0.5 * (index + 1)))
        raise RuntimeError(
            "could not attach to the live browser over CDP at %s after %d attempts; "
            "the browser-viewer Chromium may be restarting" % (endpoint, attempts)
        ) from last_exc

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
    def _page_is_live(self) -> bool:
        """True when this driver already holds a usable page.

        Any doubt (no page, closed page, a CDP call that throws) is reported as
        NOT live, so the caller opens a cold page rather than driving a corpse.
        """
        page = self._page
        if page is None:
            return False
        try:
            return not page.is_closed()
        except Exception:  # noqa: BLE001 - a page we cannot query is not usable
            return False

    def open(self, *, target_url: str, user_data_dir: str, headless: bool) -> None:
        # Idempotent: the warm-reuse daemon holds ONE driver across jobs, but the
        # engine calls open() on every job. Without this guard each job would
        # re-attach over CDP and open a NEW page, discarding the warm page and
        # the entire point of reuse. Getting a reused page ready for the next job
        # is prepare_for_next_job()'s responsibility, not open()'s -- open() must
        # not navigate here, or it would clear a page the caller has not yet
        # proven empty.
        if self._page_is_live():
            return

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
                # would discard all cookies and force re-login every job. The
                # attach is retried so a watchdog-driven Chromium relaunch is a
                # short delay rather than a hard failure.
                browser = self._connect_over_cdp_resilient(browser_type, plan.remote_endpoint)
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
            # user profile. Chromium gets STEALTH flags that hide the automation
            # fingerprint from the provider (not from UBAG) so interactive sign-in
            # (especially Google) is not blocked as an "automated" browser. UBAG
            # keeps full control over CDP and never types credentials.
            launch_kwargs = {
                "user_data_dir": user_data_dir,
                "headless": plan.headless,
            }
            if plan.browser_type_name == "chromium":
                launch_kwargs["args"] = ["--disable-blink-features=AutomationControlled"]
                launch_kwargs["ignore_default_args"] = ["--enable-automation"]
            self._context = browser_type.launch_persistent_context(**launch_kwargs)
            self._owns_context = True
        if not self._owns_context:
            # Reusing the operator's already-authenticated CDP context: open a
            # DEDICATED page for this job instead of hijacking the human's login
            # tab (pages[0]). We close it on teardown so pages never accumulate.
            self._page = self._context.new_page()
            self._owns_page = True
        else:
            self._page = (
                self._context.pages[0]
                if self._context.pages
                else self._context.new_page()
            )
            self._owns_page = False
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

    def current_thread_url(self, selectors: ProviderSelectors) -> str:  # pragma: no cover - requires real browser
        # The provider's own chat URL is the thread ref. It is a plain page URL —
        # no cookies, storage state, session ids, or noVNC URLs are ever exposed.
        return self._page.url if self._page else ""

    def resume_thread(self, selectors: ProviderSelectors, thread_ref: str) -> bool:  # pragma: no cover - requires real browser
        # Navigate to the bound chat and confirm it actually loaded. Best-effort,
        # mirroring start_new_chat: any failure to confirm returns False (the
        # engine then decides fail vs. restart). Never raises DriftDetectedError.
        if not thread_ref:
            return False
        try:
            self._page.goto(thread_ref, wait_until="domcontentloaded")
            self._page.wait_for_timeout(800)
        except Exception:  # noqa: BLE001 - a thread that will not load is unresumable
            return False
        # URL settled: the page committed a navigation to a non-empty URL.
        try:
            if not self._page.url:
                return False
        except Exception:  # noqa: BLE001 - a page we cannot query is not resumable
            return False
        # Prior conversation turn present. WAIT for it to hydrate rather than a
        # one-shot probe: a bound thread is KNOWN to have earlier turns, but the
        # provider renders them via async JS seconds after domcontentloaded, so a
        # short check races the load and misreads a good thread as "gone" (which
        # would wrongly break the binding). _await_prior_turn never raises.
        try:
            return self._await_prior_turn(selectors, timeout_ms=_resume_confirm_ms())
        except Exception:  # noqa: BLE001 - best-effort; never raise from resume
            return False

    def _await_prior_turn(self, selectors: ProviderSelectors, *, timeout_ms: int) -> bool:  # pragma: no cover - requires real browser
        """Wait (bounded) for a resumed thread's prior turn to render.

        Polls every response_container candidate for a match, sharing ONE overall
        deadline across candidates (so a thread that never rehydrates fails in
        ``timeout_ms``, not ``timeout_ms`` per candidate). Returns True as soon as
        any prior turn is present; False if none appears within the budget. Never
        raises: an unqueryable candidate is skipped, and a genuinely empty/broken
        thread reads False so the engine can fail or restart per on_missing.
        """
        import time

        deadline = time.monotonic() + max(timeout_ms, 0) / 1000.0
        group = selectors.response_container
        while True:
            for candidate in group.as_list():
                try:
                    if self._page.locator(candidate).count() > 0:
                        return True
                except Exception:  # noqa: BLE001 - skip an uncountable candidate
                    continue
            if time.monotonic() >= deadline:
                return False
            try:
                self._page.wait_for_timeout(200)
            except Exception:  # noqa: BLE001 - pacing only; keep polling to the deadline
                time.sleep(0.2)

    # -- selector helpers ------------------------------------------------
    def _first_visible(self, group, *, timeout_ms: int = 4000):  # pragma: no cover
        import time

        candidates = group.as_list()
        # Race every candidate selector against ONE overall deadline instead of
        # paying the full timeout on each in series. Previously a drifted primary
        # selector burned the entire budget before a working fallback was even
        # tried — and stream_response passes the *full* response timeout here (up
        # to the reasoning floor), so a single drifted response_container selector
        # could stall a job for minutes. Probing each candidate briefly in a loop
        # returns as soon as ANY becomes visible, bounded by the overall deadline.
        # This only changes how the element is *found*; reading the response is
        # unchanged, so response completeness is unaffected.
        deadline = time.monotonic() + max(timeout_ms, 0) / 1000.0
        probe_ms = 250 if timeout_ms > 250 else timeout_ms
        last_error: Optional[Exception] = None
        while True:
            for candidate in candidates:
                try:
                    locator = self._page.locator(candidate).first
                    locator.wait_for(state="visible", timeout=probe_ms)
                    return locator
                except Exception as exc:  # noqa: BLE001 - try next fallback
                    last_error = exc
                    continue
            if time.monotonic() >= deadline:
                raise DriftDetectedError(group.name, group.baseline_version) from last_error

    def _snapshot_counts(self, group) -> "dict[str, int]":  # pragma: no cover - requires real browser
        """Per-candidate match counts for a selector group, right now.

        Captured before submit so the read path can tell a NEWLY rendered turn
        from the prior turns already on a resumed thread. Per-candidate because
        the fallback selectors in a group match different node sets and therefore
        have different baselines. Never raises: a candidate we cannot count reads
        as 0, so at worst the reader waits for the container to (re)appear rather
        than skipping the turn.
        """
        counts: dict[str, int] = {}
        for candidate in group.as_list():
            try:
                counts[candidate] = self._page.locator(candidate).count()
            except Exception:  # noqa: BLE001 - an uncountable candidate is treated as absent
                counts[candidate] = 0
        return counts

    def _await_new_response(self, group, baseline, *, timeout_ms: int):  # pragma: no cover - requires real browser
        """Wait for THIS turn's response container and return its NEWEST locator.

        A resumed multi-turn thread already shows prior assistant turns; reading
        ``.first`` (as :meth:`_first_visible` does) latches the OLDEST turn's
        answer -- already rendered and stable -- so streaming settles instantly on
        the wrong text. Instead we wait until some candidate's match count exceeds
        its pre-submit ``baseline`` (this turn's node has appeared) and return that
        candidate's ``.last`` (newest) element. On a fresh chat every baseline is
        0 and the first assistant node makes ``.last`` == ``.first`` -- byte-
        identical to the previous behaviour. Times out into ``DriftDetectedError``
        (the job fails loudly) rather than ever returning a prior turn's answer.
        """
        import time

        deadline = time.monotonic() + max(timeout_ms, 0) / 1000.0
        probe_ms = 250 if timeout_ms > 250 else timeout_ms
        poll_s = 0.25
        last_error: Optional[Exception] = None
        while True:
            for candidate in group.as_list():
                base = baseline.get(candidate, 0)
                try:
                    locator = self._page.locator(candidate)
                    if locator.count() <= base:
                        continue
                    newest = locator.last
                    newest.wait_for(state="visible", timeout=probe_ms)
                    return newest
                except Exception as exc:  # noqa: BLE001 - try next fallback / keep waiting
                    last_error = exc
                    continue
            if time.monotonic() >= deadline:
                raise DriftDetectedError(group.name, group.baseline_version) from last_error
            # No new turn yet. Pace the wait: unlike _first_visible, the count<=base
            # branch above has no blocking wait_for, so without this the loop would
            # busy-spin a CPU core for the whole (up to reasoning-length) timeout
            # while the provider is still generating the reply.
            time.sleep(poll_s)

    def _newest_visible(self, group, *, timeout_ms: int = 4000):  # pragma: no cover - requires real browser
        """Like :meth:`_first_visible` but returns the NEWEST (``.last``) match.

        Chat transcripts are chronological, so the newest matching container is
        the turn that just completed. Reading ``.last`` (rather than ``.first``)
        is what makes :meth:`read_final_response` return THIS turn's answer on a
        resumed multi-turn thread instead of the oldest turn's. On a single-turn
        chat the two resolve to the same element.
        """
        import time

        deadline = time.monotonic() + max(timeout_ms, 0) / 1000.0
        probe_ms = 250 if timeout_ms > 250 else timeout_ms
        last_error: Optional[Exception] = None
        while True:
            for candidate in group.as_list():
                try:
                    locator = self._page.locator(candidate).last
                    locator.wait_for(state="visible", timeout=probe_ms)
                    return locator
                except Exception as exc:  # noqa: BLE001 - try next fallback
                    last_error = exc
                    continue
            if time.monotonic() >= deadline:
                raise DriftDetectedError(group.name, group.baseline_version) from last_error

    def response_container_present(self, selectors: ProviderSelectors) -> bool:
        """Warm-reuse emptiness probe against the drift-baselined container.

        Reuses ``_present``, which returns a bool and never raises: a container
        that matches nothing -- whether the chat is genuinely fresh OR the
        selector has drifted -- reads "absent". See
        :meth:`PageDriver.prepare_for_next_job` for why the drift case stays safe
        (the later read fails loudly against the same group).

        The probe runs on a short budget: it is a presence check on an already
        loaded page, not a wait for something to appear.
        """
        return self._present(
            selectors.response_container,
            timeout_ms=_emptiness_probe_ms(),
        )

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

    def _present_any(self, candidates: Sequence[str], *, timeout_ms: int = 2000) -> bool:  # pragma: no cover
        for candidate in candidates:
            try:
                self._page.locator(candidate).first.wait_for(
                    state="visible", timeout=timeout_ms
                )
                return True
            except Exception:  # noqa: BLE001
                continue
        return False

    def _click_any(self, candidates: Sequence[str], *, timeout_ms: int = 4000) -> bool:  # pragma: no cover
        for candidate in candidates:
            try:
                locator = self._page.locator(candidate).first
                locator.wait_for(state="visible", timeout=timeout_ms)
                locator.click(timeout=timeout_ms)
                return True
            except Exception:  # noqa: BLE001 - try next fallback
                continue
        return False

    def _dismiss_menus(self) -> None:  # pragma: no cover
        """Close any open picker/menu so the next step starts from a clean state.

        The prompt field is still empty at config time, so Escape can never drop a
        partially-typed prompt; it just collapses an open Material/overlay menu.
        """
        try:
            self._page.keyboard.press("Escape")
            self._page.wait_for_timeout(200)
            self._page.keyboard.press("Escape")
            self._page.wait_for_timeout(150)
        except Exception:  # noqa: BLE001
            pass

    # -- chat reaper (PERMANENT delete of one exact UBAG-created chat) ---
    def delete_chat(self, selectors: ProviderSelectors, conv_id: str) -> bool:  # pragma: no cover
        """Delete exactly the chat with ``conv_id``; verify it is gone.

        Safety posture (this is the only irreversible thing the worker can do):
          * ``conv_id`` comes from the chat ledger — a chat UBAG recorded itself
            as creating. It is substituted into id-addressed selectors, so this
            cannot express "delete the oldest/any chat".
          * The id is validated before it reaches a selector: provider chat ids
            are URL path segments, so anything outside [A-Za-z0-9_-] is rejected
            rather than interpolated (a quote/bracket could otherwise widen the
            selector to match — and thus delete — OTHER chats).
          * The options button is clicked via element.click(): the sidebar rows
            sit under overlays that intercept a positional click, which would
            silently dispatch to the wrong element.
          * Returns True only when the chat is verified ABSENT afterwards.
        """

        flow = selectors.delete_chat
        if flow is None or not conv_id:
            return False
        if not _SAFE_CONV_ID_RE.match(conv_id):
            return False
        options = flow.open_options.format(conv_id=conv_id)
        try:
            if not self._present_any([options], timeout_ms=4000):
                # Already gone (or never in this sidebar): nothing to delete.
                return not self._present_any(
                    [flow.still_present.format(conv_id=conv_id)], timeout_ms=1000
                )
            self._page.eval_on_selector(options, "el => el.click()")
            self._page.wait_for_timeout(900)
            if not self._click_any([flow.delete_item], timeout_ms=4000):
                self._dismiss_menus()
                return False
            self._page.wait_for_timeout(600)
            if not self._click_any([flow.confirm], timeout_ms=4000):
                self._dismiss_menus()
                return False
            self._page.wait_for_timeout(1500)
        except Exception:  # noqa: BLE001 - never raise into the reaper loop
            self._dismiss_menus()
            return False
        # Verify rather than trust the click.
        return not self._present_any(
            [flow.still_present.format(conv_id=conv_id)], timeout_ms=2000
        )

    # -- pre-submit configuration (new chat + model/mode/reasoning) ------
    def start_new_chat(self, selectors: ProviderSelectors) -> bool:  # pragma: no cover
        group = selectors.new_chat
        if group is None:
            return False
        # Wait for the composer to be interactive before clicking New chat, so the
        # click never lands on a half-loaded page (right after auth or a retry
        # reset) — a common source of transient failures under load.
        try:
            self._first_visible(selectors.prompt_input, timeout_ms=10000)
        except Exception:  # noqa: BLE001 - best-effort settle
            pass
        clicked = self._click_any(group.as_list(), timeout_ms=4000)
        if clicked:
            try:
                self._page.wait_for_timeout(900)
            except Exception:  # noqa: BLE001
                pass
        return clicked

    def ensure_provider_config(  # pragma: no cover
        self,
        selectors: ProviderSelectors,
        overrides: Optional[Mapping[str, object]] = None,
    ) -> List[Mapping[str, object]]:
        overrides = overrides or {}
        results: List[Mapping[str, object]] = []
        # Wait for the composer to settle before touching any picker/menu: a
        # New-chat click triggers an SPA route change, and interacting with a
        # half-navigated page is the main source of transient config flakes.
        try:
            self._first_visible(selectors.prompt_input, timeout_ms=10000)
            self._page.wait_for_timeout(400)
        except Exception:  # noqa: BLE001 - best-effort settle; setting logic still guards
            pass
        for setting in selectors.settings:
            desired = overrides.get(setting.key, setting.desired)
            results.append(self._ensure_setting(selectors, setting, desired))
        return results

    def _open_control(self, open_steps: Sequence[Sequence[str]]) -> None:  # pragma: no cover
        for step in open_steps:
            if not self._click_any(list(step), timeout_ms=4000):
                # The control to reveal the menu is not present; stop opening.
                # The satisfied check then fails and the retry/drift path decides.
                break
            try:
                self._page.wait_for_timeout(650)
            except Exception:  # noqa: BLE001
                pass

    def _setting_satisfied(self, setting, desired) -> bool:  # pragma: no cover
        if setting.kind == "toggle":
            is_on = self._present_any(list(setting.on_when), timeout_ms=1500)
            return is_on == bool(desired)
        selector = setting.satisfied_when.format(value=desired)
        return self._present_any([selector], timeout_ms=2000)

    def _apply_setting(self, setting, desired) -> bool:  # pragma: no cover
        if setting.kind == "toggle":
            is_on = self._present_any(list(setting.on_when), timeout_ms=1200)
            if is_on == bool(desired):
                return True
            return self._click_any(list(setting.toggle_click), timeout_ms=4000)
        selector = setting.apply_click.format(value=desired)
        return self._click_any([selector], timeout_ms=4000)

    def _ensure_setting(self, selectors, setting, desired):  # pragma: no cover
        # Read -> act-if-different -> re-verify. The first pass detects an
        # already-correct setting (no-op); later passes confirm an applied change.
        # Three attempts (with a short escalating settle) absorb a transient menu
        # race so a flaky open/click self-heals within the same job instead of
        # failing it.
        attempts = (1, 2, 3)
        for attempt in attempts:
            self._open_control(setting.open_steps)
            if self._setting_satisfied(setting, desired):
                self._dismiss_menus()
                return {
                    "key": setting.key,
                    "desired": desired,
                    "state": "already_set" if attempt == 1 else "set",
                }
            if attempt == attempts[-1]:
                self._dismiss_menus()
                if setting.required:
                    raise DriftDetectedError(
                        "setting:" + setting.key, selectors.selector_version
                    )
                return {"key": setting.key, "desired": desired, "state": "unverified"}
            self._apply_setting(setting, desired)
            self._dismiss_menus()
            try:
                self._page.wait_for_timeout(300 * attempt)
            except Exception:  # noqa: BLE001
                pass
        return {"key": setting.key, "desired": desired, "state": "unknown"}

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

    def wait_until_authenticated(  # pragma: no cover - requires real browser
        self, selectors: ProviderSelectors, *, timeout_s: float
    ) -> str:
        # Identical mechanism to await_manual_login for the live driver: poll the
        # authenticated marker only; no credentials are ever typed. Kept as a
        # distinct method so the engine's intent (grace re-check vs. manual wait)
        # is explicit and the mock can diverge.
        return self.await_manual_login(selectors, timeout_s=timeout_s)

    def login_signal_present(  # pragma: no cover - requires real browser
        self, selectors: ProviderSelectors
    ) -> bool:
        # A visible sign-in affordance means the session is genuinely logged out;
        # its absence on a not-yet-authenticated page means it is still rendering.
        return self._present(selectors.login_signal, timeout_ms=1500)

    def reset(self, target_url: str) -> None:  # pragma: no cover - requires real browser
        # Reload the page to a clean target before a retry. Stays within the same
        # authenticated context (no credentials typed), so login persists.
        if self._page is None:
            return
        try:
            self._page.goto(target_url, wait_until="domcontentloaded")
            self._page.wait_for_timeout(800)
        except Exception:  # noqa: BLE001 - best-effort reset; the retry guards itself
            pass

    # -- interaction -----------------------------------------------------
    def submit_prompt(self, selectors: ProviderSelectors, prompt: str) -> None:  # pragma: no cover
        # Turn-aware read baseline: snapshot how many assistant turns are already
        # on the page BEFORE we submit, so stream_response/read_final_response can
        # target the NEW turn and never latch a prior turn's answer on a resumed
        # thread. Captured per-candidate because a group's fallback selectors match
        # different node sets. Must run before the reply node can render.
        self._response_baseline = self._snapshot_counts(selectors.response_container)
        field = self._first_visible(selectors.prompt_input)
        try:
            field.click()
        except Exception as exc:  # noqa: BLE001 - classify provider overlays.
            if _is_human_verification_overlay(exc):
                # CAPTCHA / "verify it's you" identity challenge — like the
                # provider login, this is the ONE thing only the human can clear.
                # Auto-solving bot-detection would risk the account and is out of
                # scope; everything that is NOT identity verification is handled
                # automatically below.
                raise ManualActionRequired(
                    "human_verification_required",
                    "Provider is showing a CAPTCHA / identity verification. Clear "
                    "it once in the live (noVNC) session; UBAG never solves it.",
                ) from exc
            if _looks_like_manual_overlay(exc):
                # Benign cookie / consent / onboarding popup — the SYSTEM dismisses
                # it automatically (full automation; the only manual step is login)
                # and retries the field. Only if it truly cannot be cleared do we
                # fall back to the human.
                if not self._auto_dismiss_overlay():
                    raise ManualActionRequired(
                        "overlay_not_auto_dismissable",
                        "An overlay is blocking the prompt and could not be "
                        "dismissed automatically; clear it once in noVNC.",
                    ) from exc
                try:
                    field = self._first_visible(selectors.prompt_input)
                    field.click()
                except Exception as exc2:  # noqa: BLE001 - still blocked after dismiss
                    raise ManualActionRequired(
                        "overlay_not_auto_dismissable",
                        "An overlay kept blocking the prompt after an automatic "
                        "dismiss attempt; clear it once in noVNC.",
                    ) from exc2
            else:
                raise
        field.fill(prompt)
        try:
            button = self._first_visible(selectors.submit_button, timeout_ms=3000)
            button.click()
        except DriftDetectedError:
            # Fallback: many composers submit on Enter.
            field.press("Enter")

    def _auto_dismiss_overlay(self) -> bool:  # pragma: no cover - requires real browser
        """Best-effort: clear a benign cookie/consent/onboarding popup so the
        system proceeds without a human (full automation; login is the only
        manual step). Provider-agnostic, matched by the accessible name of common
        accept/continue controls. CAPTCHA / identity verification is handled
        separately and never auto-clicked here. Returns True if a control was
        clicked or Escape pressed; the caller re-checks the prompt field, so a
        wrong guess simply falls through to the manual path instead of proceeding
        in a bad state.
        """
        consent_labels = (
            "Accept all", "Accept All", "Accept", "I agree", "Agree",
            "Allow all", "Allow", "Got it", "Continue", "OK", "Okay",
            "No thanks", "Reject all", "Reject",
        )
        for label in consent_labels:
            try:
                button = self._page.get_by_role("button", name=label).first
                button.wait_for(state="visible", timeout=800)
                button.click(timeout=1500)
                self._page.wait_for_timeout(300)
                return True
            except Exception:  # noqa: BLE001 - try the next candidate label
                continue
        # Many lightweight overlays close on Escape; harmless if nothing is open.
        try:
            self._page.keyboard.press("Escape")
            self._page.wait_for_timeout(200)
            return True
        except Exception:  # noqa: BLE001
            return False

    def attach_files(  # pragma: no cover - requires real browser
        self,
        selectors: ProviderSelectors,
        file_paths: Sequence[str],
        *,
        timeout_ms: int = 15000,
    ) -> None:
        group = selectors.file_input
        if group is None:
            raise DriftDetectedError("file_input", selectors.selector_version)
        paths = list(file_paths)
        last_error: Optional[Exception] = None
        for candidate in group.as_list():
            try:
                locator = self._page.locator(candidate).first
                # set_input_files settles on the attached (often hidden) <input>,
                # which is exactly how chat UIs expose their upload control — so we
                # deliberately do NOT wait for visibility here.
                locator.set_input_files(paths, timeout=timeout_ms)
                return
            except Exception as exc:  # noqa: BLE001 - try next fallback
                last_error = exc
                continue
        raise DriftDetectedError(group.name, group.baseline_version) from last_error

    def stream_response(  # pragma: no cover - requires real browser
        self, selectors: ProviderSelectors, *, timeout_s: float
    ) -> Iterator[str]:
        import time

        # Bind to THIS turn's container, not a prior turn's. On a resumed thread
        # the earlier answers are already on screen; _await_new_response waits for
        # a node beyond the pre-submit baseline and returns the newest one, so the
        # poll below tracks the reply we just triggered.
        container = self._await_new_response(
            selectors.response_container,
            self._response_baseline,
            timeout_ms=int(timeout_s * 1000),
        )
        deadline = time.monotonic() + timeout_s
        seen = ""
        last_growth = time.monotonic()
        # Reasoning modes pause (often >1s) while "thinking" with no streaming
        # indicator; widen the settle window so a mid-thought pause is not mistaken
        # for completion and the partial answer captured.
        settle_s = self._response_settle_s
        if getattr(selectors, "reasoning", False):
            settle_s = max(settle_s, _reasoning_settle_s())
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
            # Short probe: the streaming indicator, when present, is already on
            # screen, so a long timeout only wastes wall-clock every poll (and is
            # pure waste for providers such as Gemini that expose no indicator).
            # A false "not streaming" here is harmless — completion still requires
            # `settled` (no text growth for settle_s), which stays false while the
            # response is actively growing.
            still_streaming = self._present(selectors.streaming_indicator, timeout_ms=150)
            settled = (time.monotonic() - last_growth) >= settle_s
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
        # Read the NEWEST match (.last), not the oldest (.first): on a resumed
        # multi-turn thread .first is a prior turn's answer. streaming already
        # waited for this turn's node, so the newest container is this turn's.
        if return_mode == "final" and selectors.final_answer_container is not None:
            try:
                answer = self._newest_visible(selectors.final_answer_container)
                return answer.inner_text(timeout=4000)
            except DriftDetectedError:
                pass
        container = self._newest_visible(selectors.response_container)
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
            # Close our dedicated job page first when the context is the
            # operator's shared CDP browser (which we must NOT close — that would
            # tear down the user's logged-in session for every job).
            if self._owns_page and self._page is not None and not self._owns_context:
                try:
                    self._page.close()
                except Exception:  # noqa: BLE001 - best-effort page cleanup
                    pass
            if self._context is not None and self._owns_context:
                self._context.close()
        finally:
            if self._playwright is not None:
                self._playwright.stop()
            self._context = None
            self._owns_context = True
            self._page = None
            self._owns_page = False
            self._playwright = None


def _emptiness_probe_ms() -> int:
    """Budget (ms) for the warm-reuse emptiness probe; env-overridable.

    Short by design: this asks "is a prior turn already rendered on this loaded
    page?", not "wait for one to appear". Erring long only delays the cold-tab
    fallback; it never trades away response completeness, which is decided
    later by stream_response/read_final_response.
    """

    raw = os.environ.get("UBAG_EMPTINESS_PROBE_MS", "").strip()
    try:
        value = int(raw)
        if value > 0:
            return value
    except (TypeError, ValueError):
        pass
    return _EMPTINESS_PROBE_MS


def _resume_confirm_ms() -> int:
    """Budget (ms) to wait for a resumed thread's prior turn to render; env-overridable.

    Long by design (see :data:`_RESUME_CONFIRM_MS`): confirming a bound thread
    means waiting for the provider to hydrate its earlier messages after
    navigation, not a one-shot presence check.
    """

    raw = os.environ.get("UBAG_RESUME_CONFIRM_MS", "").strip()
    try:
        value = int(raw)
        if value > 0:
            return value
    except (TypeError, ValueError):
        pass
    return _RESUME_CONFIRM_MS


def _reasoning_settle_s() -> float:
    """Settle window (s) for reasoning responses; env-overridable for tuning."""

    raw = os.environ.get("UBAG_REASONING_SETTLE_S", "").strip()
    try:
        value = float(raw)
        if value > 0:
            return value
    except (TypeError, ValueError):
        pass
    return _REASONING_SETTLE_S


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


def _is_human_verification_overlay(exc: Exception) -> bool:
    """True when the blocking overlay is a CAPTCHA / identity challenge — the one
    thing (besides the initial provider login) that only a human may clear. UBAG
    never attempts to solve these (bot-detection bypass is out of scope)."""
    message = str(exc).lower()
    if "intercepts pointer events" not in message:
        return False
    verification_markers = (
        "captcha",
        "recaptcha",
        "hcaptcha",
        "verification",
        "verify you",
        "verify it",
        "challenge",
        "are you human",
        "not a robot",
    )
    return any(marker in message for marker in verification_markers)


def _looks_like_manual_overlay(exc: Exception) -> bool:
    """True for a benign, auto-dismissable overlay (cookie / consent / onboarding
    popup). CAPTCHA / identity verification is classified separately by
    :func:`_is_human_verification_overlay` and is intentionally NOT included, so
    the system auto-dismisses benign popups but still defers real human
    verification to the operator."""
    message = str(exc).lower()
    if "intercepts pointer events" not in message:
        return False
    overlay_markers = (
        "cookie",
        "consent",
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
    "_is_human_verification_overlay",
    "_is_tolerable_navigation_abort",
    "_looks_like_manual_overlay",
    "create_default_driver",
    "offline_mode_enabled",
]

"""Pluggable, cross-engine browser-driver abstraction for the worker.

This module implements the **ToS-safe** abstraction layer described in the
UBAG v2.1 blueprint:

* §13.10 Pluggable browser engine - the same adapter logic targets
  Chromium/Firefox/WebKit and the W3C **WebDriver BiDi** standard, selectable by
  *config only* (no code change).
* §13.12 Engine-portable selectors - a pure helper that orders selector
  strategies so accessibility-role/ARIA and BiDi locators (portable across
  engines) are preferred, with ``test-id``/CSS as engine-specific fast paths and
  the ML vision model as a last resort.

Hard constraints (mirrored from :mod:`ubag_worker.live.page_driver`):

* **No scraping logic, no CAPTCHA solving, no credential ingestion.** This file
  only selects and constructs a driver; the actual page operations live in the
  existing :class:`~ubag_worker.live.page_driver.PageDriver` implementations.
* Any ``playwright``/``selenium`` import is **lazy** (performed inside a method),
  so importing this module and running the offline test-suite never requires a
  browser to be installed.

The :class:`Engine` lifecycle is intentionally parallel to ``PageDriver`` so an
engine can produce ``PageDriver``-compatible drivers via :meth:`Engine.new_context`.
"""

from __future__ import annotations

import enum
import os
from abc import ABC, abstractmethod
from dataclasses import dataclass, field
from typing import Mapping, Optional

from .page_driver import MockPageDriver, PageDriver, PlaywrightPageDriver
from .selectors import SelectorGroup


# ---------------------------------------------------------------------------
# Engine + protocol enums
# ---------------------------------------------------------------------------


class EngineKind(enum.Enum):
    """Browser engine family (per the §13.10 cross-engine table)."""

    CHROMIUM = "chromium"
    FIREFOX = "firefox"
    WEBKIT = "webkit"
    BIDI = "bidi"


class EngineProtocol(enum.Enum):
    """Wire protocol used to drive the engine."""

    CDP = "cdp"
    PLAYWRIGHT = "playwright"
    WEBDRIVER_BIDI = "webdriver_bidi"


# Default kind -> protocol mapping per the §13.10 table:
#   chromium = CDP, firefox = Playwright/BiDi, webkit = Playwright,
#   bidi (any W3C browser) = WebDriver BiDi.
# Firefox defaults to Playwright (its first-listed driver); switch to BiDi via
# config when second-engine diversity over the standard wire is desired.
DEFAULT_PROTOCOL_FOR_KIND: Mapping[EngineKind, EngineProtocol] = {
    EngineKind.CHROMIUM: EngineProtocol.CDP,
    EngineKind.FIREFOX: EngineProtocol.PLAYWRIGHT,
    EngineKind.WEBKIT: EngineProtocol.PLAYWRIGHT,
    EngineKind.BIDI: EngineProtocol.WEBDRIVER_BIDI,
}


def default_protocol_for(kind: EngineKind) -> EngineProtocol:
    """Return the default wire protocol for an engine kind."""

    return DEFAULT_PROTOCOL_FOR_KIND[kind]


# ---------------------------------------------------------------------------
# Engine specification
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class EngineSpec:
    """Declarative, config-driven engine selection.

    Attributes
    ----------
    kind:
        Engine family (:class:`EngineKind`).
    protocol:
        Wire protocol. When ``None`` it is resolved from
        :data:`DEFAULT_PROTOCOL_FOR_KIND` based on ``kind``.
    remote_endpoint:
        Remote browser-grid URL. ``None`` means a local engine (§13.11).
    headed:
        Whether to launch with a visible UI (default headless).
    stealth:
        Advisory flag for stealth-friendly launch ergonomics. This layer only
        records the intent; it performs **no** anti-bot or CAPTCHA logic.
    extra:
        Opaque, engine-specific options forwarded to the concrete engine.
    """

    kind: EngineKind = EngineKind.CHROMIUM
    protocol: Optional[EngineProtocol] = None
    remote_endpoint: Optional[str] = None
    headed: bool = False
    stealth: bool = False
    extra: Mapping[str, object] = field(default_factory=dict)

    def __post_init__(self) -> None:
        if self.protocol is None:
            # frozen dataclass: bypass the immutability guard to fill the default.
            object.__setattr__(self, "protocol", default_protocol_for(self.kind))

    @property
    def is_remote(self) -> bool:
        return bool(self.remote_endpoint)

    @property
    def supports_bidi(self) -> bool:
        return self.protocol == EngineProtocol.WEBDRIVER_BIDI


# ---------------------------------------------------------------------------
# Engine interface + concrete engines
# ---------------------------------------------------------------------------


class Engine(ABC):
    """Abstract browser engine that produces ``PageDriver``-compatible drivers.

    The lifecycle mirrors :class:`~ubag_worker.live.page_driver.PageDriver`:
    ``launch`` -> ``new_context`` -> ... -> ``close``.
    """

    @property
    @abstractmethod
    def engine_kind(self) -> EngineKind:
        ...

    @property
    @abstractmethod
    def protocol(self) -> EngineProtocol:
        ...

    @property
    @abstractmethod
    def is_remote(self) -> bool:
        ...

    @property
    def supports_bidi(self) -> bool:
        return self.protocol == EngineProtocol.WEBDRIVER_BIDI

    @abstractmethod
    def launch(self) -> None:
        """Prepare the engine. Real browser launching is lazy/optional."""

    @abstractmethod
    def new_context(self, options: Optional[Mapping[str, object]] = None) -> PageDriver:
        """Create a fresh ``PageDriver`` bound to this engine."""

    @abstractmethod
    def close(self) -> None:
        ...


class MockEngine(Engine):
    """Dependency-free engine for tests and offline mode.

    Produces deterministic :class:`MockPageDriver` instances; never imports a
    browser library. Also aliased as ``NullEngine``.
    """

    def __init__(
        self,
        *,
        kind: EngineKind = EngineKind.CHROMIUM,
        protocol: Optional[EngineProtocol] = None,
    ) -> None:
        self._kind = kind
        self._protocol = protocol or default_protocol_for(kind)
        self._launched = False
        self._closed = False

    @property
    def engine_kind(self) -> EngineKind:
        return self._kind

    @property
    def protocol(self) -> EngineProtocol:
        return self._protocol

    @property
    def is_remote(self) -> bool:
        return False

    def launch(self) -> None:
        self._launched = True

    def new_context(self, options: Optional[Mapping[str, object]] = None) -> PageDriver:
        opts = dict(options or {})
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

    def close(self) -> None:
        self._closed = True


# Backwards/forwards-friendly alias requested by the spec.
NullEngine = MockEngine


class LocalPlaywrightEngine(Engine):
    """Local Playwright-driven engine (Chromium/Firefox/WebKit).

    Playwright is imported lazily inside :meth:`launch`, so constructing the
    engine (and running offline tests) never requires Playwright. The produced
    driver is the existing :class:`PlaywrightPageDriver`.
    """

    def __init__(self, spec: Optional[EngineSpec] = None) -> None:
        self._spec = spec or EngineSpec()
        self._playwright = None
        self._launched = False
        self._closed = False

    @property
    def engine_kind(self) -> EngineKind:
        return self._spec.kind

    @property
    def protocol(self) -> EngineProtocol:
        assert self._spec.protocol is not None  # filled by EngineSpec.__post_init__
        return self._spec.protocol

    @property
    def is_remote(self) -> bool:
        return False

    @property
    def browser_type_name(self) -> str:
        """Playwright browser-type attribute for this engine kind."""

        if self._spec.kind == EngineKind.WEBKIT:
            return "webkit"
        if self._spec.kind == EngineKind.FIREFOX:
            return "firefox"
        # Chromium and the vendor-neutral BiDi kind both launch via chromium
        # locally; remote BiDi targets are handled by RemoteGridEngine.
        return "chromium"

    def launch(self) -> None:
        try:
            from playwright.sync_api import sync_playwright
        except ImportError as exc:  # pragma: no cover - requires real browser
            raise RuntimeError(
                "Playwright is not installed. For live runs install it with "
                "'pip install playwright' and 'playwright install'. For tests/"
                "offline use a MockEngine or UBAG_ADAPTER_OFFLINE=1."
            ) from exc
        self._playwright = sync_playwright().start()  # pragma: no cover
        self._launched = True  # pragma: no cover

    def new_context(self, options: Optional[Mapping[str, object]] = None) -> PageDriver:
        # The concrete page operations live in PlaywrightPageDriver, which
        # performs its own lazy Playwright import in PageDriver.open().
        return PlaywrightPageDriver()

    def close(self) -> None:
        if self._playwright is not None:  # pragma: no cover - requires browser
            self._playwright.stop()
        self._playwright = None
        self._closed = True


# ---------------------------------------------------------------------------
# Engine-portable selector strategy ordering (§13.12)
# ---------------------------------------------------------------------------

# Strategy labels in portability priority order. Accessibility-role / ARIA and
# BiDi locators behave identically across Chromium/Firefox/WebKit, so they rank
# highest; test-id and CSS are engine-specific fast paths; the ML vision model
# is always the last resort.
_STRATEGY_PRIORITY = {
    "accessibility-role": 0,
    "aria": 1,
    "bidi": 2,
    "text": 3,
    "test-id": 4,
    "css": 5,
    "ml-vision": 99,
}


def classify_selector(candidate: str) -> str:
    """Classify a single selector string into a strategy label (pure)."""

    value = candidate.strip()
    lowered = value.lower()
    if lowered.startswith("bidi:") or "::bidi" in lowered:
        return "bidi"
    if lowered.startswith("role=") or "[role=" in lowered or "[role~=" in lowered:
        return "accessibility-role"
    if "aria-" in lowered:
        return "aria"
    if "data-testid" in lowered or "data-test-id" in lowered or "data-test=" in lowered:
        return "test-id"
    if lowered.startswith("text=") or ":has-text(" in lowered:
        return "text"
    return "css"


def portable_strategy_order(group: SelectorGroup) -> list:
    """Return ordered selector-strategy labels for a :class:`SelectorGroup`.

    Pure function. ARIA/accessibility-role and BiDi locators (portable across
    engines) come first, then ``test-id``/CSS engine-specific fast paths, and
    ``ml-vision`` is always appended as the last resort.
    """

    labels = {classify_selector(candidate) for candidate in group.as_list()}
    labels.add("ml-vision")  # ML vision model is always the documented fallback.
    return sorted(labels, key=lambda label: _STRATEGY_PRIORITY.get(label, 50))


# ---------------------------------------------------------------------------
# Config-driven engine selection
# ---------------------------------------------------------------------------


def _env_bool(name: str) -> bool:
    return os.environ.get(name, "").strip().lower() in ("1", "true", "yes", "on")


def _engine_kind_from_env(value: str) -> EngineKind:
    try:
        return EngineKind(value.strip().lower())
    except ValueError as exc:
        raise ValueError(
            "unknown UBAG_BROWSER_ENGINE %r (expected one of: %s)"
            % (value, ", ".join(k.value for k in EngineKind))
        ) from exc


def _engine_protocol_from_env(value: str) -> EngineProtocol:
    token = value.strip().lower()
    aliases = {
        "cdp": EngineProtocol.CDP,
        "playwright": EngineProtocol.PLAYWRIGHT,
        "bidi": EngineProtocol.WEBDRIVER_BIDI,
        "webdriver_bidi": EngineProtocol.WEBDRIVER_BIDI,
        "webdriver-bidi": EngineProtocol.WEBDRIVER_BIDI,
    }
    if token not in aliases:
        raise ValueError(
            "unknown UBAG_BROWSER_PROTOCOL %r (expected one of: cdp, playwright, bidi)"
            % value
        )
    return aliases[token]


def engine_spec_from_env() -> EngineSpec:
    """Build an :class:`EngineSpec` purely from environment variables.

    Recognized variables (config-only engine switch, §13.10):

    * ``UBAG_BROWSER_ENGINE`` - chromium | firefox | webkit | bidi
    * ``UBAG_BROWSER_PROTOCOL`` - cdp | playwright | bidi
    * ``UBAG_REMOTE_BROWSER_ENDPOINT`` - remote grid URL (§13.11)
    * ``UBAG_BROWSER_HEADED`` - truthy to launch headed
    """

    kind_env = os.environ.get("UBAG_BROWSER_ENGINE", "").strip()
    kind = _engine_kind_from_env(kind_env) if kind_env else EngineKind.CHROMIUM

    protocol_env = os.environ.get("UBAG_BROWSER_PROTOCOL", "").strip()
    protocol = _engine_protocol_from_env(protocol_env) if protocol_env else None

    endpoint = os.environ.get("UBAG_REMOTE_BROWSER_ENDPOINT", "").strip() or None

    return EngineSpec(
        kind=kind,
        protocol=protocol,
        remote_endpoint=endpoint,
        headed=_env_bool("UBAG_BROWSER_HEADED"),
    )


def select_engine(spec_or_env: Optional[EngineSpec] = None) -> Engine:
    """Choose an :class:`Engine` from an :class:`EngineSpec` or the environment.

    * ``None`` -> read configuration from environment variables.
    * A remote endpoint (in the spec or via ``UBAG_REMOTE_BROWSER_ENDPOINT``)
      yields a :class:`~ubag_worker.live.remote.RemoteGridEngine` (§13.11).
    * Otherwise a local :class:`LocalPlaywrightEngine` is returned (default:
      chromium / CDP). The Playwright import stays lazy until ``launch()``.
    """

    spec = spec_or_env if spec_or_env is not None else engine_spec_from_env()

    if spec.is_remote:
        # Imported lazily to avoid a circular import (remote imports engines).
        from .remote import RemoteGridEngine

        return RemoteGridEngine(spec)

    return LocalPlaywrightEngine(spec)


__all__ = [
    "DEFAULT_PROTOCOL_FOR_KIND",
    "Engine",
    "EngineKind",
    "EngineProtocol",
    "EngineSpec",
    "LocalPlaywrightEngine",
    "MockEngine",
    "NullEngine",
    "classify_selector",
    "default_protocol_for",
    "engine_spec_from_env",
    "portable_strategy_order",
    "select_engine",
]

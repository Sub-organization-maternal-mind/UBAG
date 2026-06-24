"""Remote browser-grid engine (UBAG v2.1 blueprint §13.11).

Workers may drive **remote** browsers over CDP / WebDriver BiDi instead of (or
alongside) local ones - e.g. a Playwright server, ``browserless``, a self-hosted
grid, **Selenium Grid 4**, or Docker ``seleniarm`` images - so browser capacity
scales independently in a hardened, network-isolated subnet.

ToS-safe boundary: this module only *connects to* and *manages the lifecycle of*
a remote browser endpoint. It contains **no** scraping logic, **no** CAPTCHA
solving, and **no** credential ingestion. Any ``playwright``/``selenium`` import
is lazy, so the module imports cleanly with no browser libraries installed.
"""

from __future__ import annotations

from typing import Mapping, NamedTuple, Optional
from urllib.parse import urlsplit

from .engines import (
    Engine,
    EngineKind,
    EngineProtocol,
    EngineSpec,
    default_protocol_for,
)
from .page_driver import PageDriver, PlaywrightPageDriver

# Endpoint schemes we consider safe for a browser grid connection.
_ALLOWED_SCHEMES = ("ws", "wss", "http", "https")


class GridEndpoint(NamedTuple):
    """Parsed, validated remote-grid endpoint."""

    scheme: str
    host: str
    port: Optional[int]
    protocol: EngineProtocol


def _redact_endpoint(url: str) -> str:
    """Strip any embedded userinfo so credentials never reach logs/reprs."""

    try:
        parts = urlsplit(url)
    except ValueError:
        return "<invalid-endpoint>"
    netloc = parts.hostname or ""
    if parts.port:
        netloc = "%s:%d" % (netloc, parts.port)
    redacted = parts._replace(netloc=netloc)
    return redacted.geturl() or "<invalid-endpoint>"


def parse_grid_endpoint(url: str) -> GridEndpoint:
    """Parse and validate a remote browser-grid endpoint URL.

    Returns a :class:`GridEndpoint` ``(scheme, host, port, protocol)``. Raises
    :class:`ValueError` for empty input, an unsupported scheme, or a missing
    host. The inferred protocol is WebDriver BiDi when the path/URL mentions
    ``bidi``; ``ws``/``wss`` endpoints otherwise default to CDP and
    ``http``/``https`` endpoints to Playwright-server connect semantics.
    """

    if not url or not url.strip():
        raise ValueError("remote browser endpoint must be a non-empty URL")

    raw = url.strip()
    try:
        parts = urlsplit(raw)
    except ValueError as exc:
        raise ValueError("invalid remote browser endpoint URL: %r" % url) from exc

    scheme = parts.scheme.lower()
    if scheme not in _ALLOWED_SCHEMES:
        raise ValueError(
            "unsupported remote browser endpoint scheme %r (allowed: %s)"
            % (parts.scheme, ", ".join(_ALLOWED_SCHEMES))
        )

    host = parts.hostname
    if not host:
        raise ValueError("remote browser endpoint must include a host: %r" % url)

    lowered = raw.lower()
    if "bidi" in lowered:
        protocol = EngineProtocol.WEBDRIVER_BIDI
    elif scheme in ("ws", "wss"):
        protocol = EngineProtocol.CDP
    else:
        protocol = EngineProtocol.PLAYWRIGHT

    return GridEndpoint(scheme=scheme, host=host, port=parts.port, protocol=protocol)


class RemoteGridEngine(Engine):
    """Engine that drives a remote browser over CDP / WebDriver BiDi.

    Construction validates the endpoint URL eagerly (cheap, no network). The
    actual ``playwright.connect()``-style attach happens lazily in
    :meth:`launch`, so the module never requires a browser library at import or
    in offline tests.
    """

    def __init__(
        self,
        spec: EngineSpec,
        *,
        endpoint: Optional[str] = None,
    ) -> None:
        resolved_endpoint = endpoint or spec.remote_endpoint
        if not resolved_endpoint:
            raise ValueError("RemoteGridEngine requires a remote endpoint")

        self._spec = spec
        self._endpoint = resolved_endpoint
        # Validate eagerly; this also derives a protocol from the URL.
        self._parsed = parse_grid_endpoint(resolved_endpoint)
        # Spec protocol wins when explicitly provided; else use the URL-derived
        # protocol, falling back to the kind default.
        self._protocol = (
            spec.protocol
            if spec.protocol is not None
            else self._parsed.protocol or default_protocol_for(spec.kind)
        )
        self._browser = None
        self._launched = False
        self._closed = False

    @property
    def engine_kind(self) -> EngineKind:
        return self._spec.kind

    @property
    def protocol(self) -> EngineProtocol:
        return self._protocol

    @property
    def is_remote(self) -> bool:
        return True

    @property
    def endpoint(self) -> GridEndpoint:
        return self._parsed

    @property
    def safe_endpoint(self) -> str:
        """Endpoint string with any embedded credentials redacted."""

        return _redact_endpoint(self._endpoint)

    def launch(self) -> None:
        try:
            from playwright.sync_api import sync_playwright
        except ImportError as exc:  # pragma: no cover - requires playwright
            raise RuntimeError(
                "Playwright is not installed; remote grid connect requires "
                "'pip install playwright'. For tests/offline use a MockEngine."
            ) from exc

        playwright = sync_playwright().start()  # pragma: no cover
        browser_type = getattr(  # pragma: no cover
            playwright,
            "chromium" if self._spec.kind == EngineKind.CHROMIUM else self._spec.kind.value,
        )
        # connect() attaches over CDP/BiDi to the remote endpoint; never logs creds.
        self._browser = browser_type.connect(self._endpoint)  # pragma: no cover
        self._launched = True  # pragma: no cover

    def new_context(self, options: Optional[Mapping[str, object]] = None) -> PageDriver:
        # Page operations are delegated to the shared PlaywrightPageDriver, which
        # performs its own lazy Playwright import.
        return PlaywrightPageDriver()

    def close(self) -> None:
        if self._browser is not None:  # pragma: no cover - requires playwright
            self._browser.close()
        self._browser = None
        self._closed = True

    def __repr__(self) -> str:
        return "RemoteGridEngine(kind=%s, protocol=%s, endpoint=%s)" % (
            self._spec.kind.value,
            self._protocol.value,
            self.safe_endpoint,
        )


__all__ = [
    "GridEndpoint",
    "RemoteGridEngine",
    "parse_grid_endpoint",
]

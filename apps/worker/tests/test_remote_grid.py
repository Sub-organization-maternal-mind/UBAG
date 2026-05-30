"""Unit tests for the remote browser-grid engine (remote.py).

These tests run fully offline (no playwright/selenium, no network). They cover
endpoint parsing/validation (valid and invalid, rejecting unsafe schemes),
RemoteGridEngine construction/flags, credential redaction, and selection of a
RemoteGridEngine when a remote endpoint is configured.
"""

import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.live.engines import (  # noqa: E402
    Engine,
    EngineKind,
    EngineProtocol,
    EngineSpec,
)
from ubag_worker.live.page_driver import PageDriver  # noqa: E402
from ubag_worker.live.remote import (  # noqa: E402
    GridEndpoint,
    RemoteGridEngine,
    parse_grid_endpoint,
)


class ParseGridEndpointTests(unittest.TestCase):
    def test_valid_ws_endpoint_defaults_to_cdp(self) -> None:
        parsed = parse_grid_endpoint("ws://grid.example:9222/devtools")
        self.assertIsInstance(parsed, GridEndpoint)
        self.assertEqual(parsed.scheme, "ws")
        self.assertEqual(parsed.host, "grid.example")
        self.assertEqual(parsed.port, 9222)
        self.assertEqual(parsed.protocol, EngineProtocol.CDP)

    def test_valid_wss_bidi_endpoint(self) -> None:
        parsed = parse_grid_endpoint("wss://grid.example:4444/session/bidi")
        self.assertEqual(parsed.scheme, "wss")
        self.assertEqual(parsed.protocol, EngineProtocol.WEBDRIVER_BIDI)

    def test_valid_http_endpoint_defaults_to_playwright(self) -> None:
        parsed = parse_grid_endpoint("https://browserless.example/playwright")
        self.assertEqual(parsed.scheme, "https")
        self.assertEqual(parsed.host, "browserless.example")
        self.assertIsNone(parsed.port)
        self.assertEqual(parsed.protocol, EngineProtocol.PLAYWRIGHT)

    def test_empty_endpoint_raises(self) -> None:
        with self.assertRaises(ValueError):
            parse_grid_endpoint("")
        with self.assertRaises(ValueError):
            parse_grid_endpoint("   ")

    def test_bad_scheme_is_rejected(self) -> None:
        for bad in ("ftp://grid.example", "file:///etc/passwd", "javascript:alert(1)"):
            with self.assertRaises(ValueError):
                parse_grid_endpoint(bad)

    def test_missing_host_is_rejected(self) -> None:
        with self.assertRaises(ValueError):
            parse_grid_endpoint("ws://")


class RemoteGridEngineTests(unittest.TestCase):
    def test_construct_from_spec(self) -> None:
        spec = EngineSpec(kind=EngineKind.BIDI, remote_endpoint="wss://grid.example:4444/bidi")
        engine = RemoteGridEngine(spec)
        self.assertIsInstance(engine, Engine)
        self.assertTrue(engine.is_remote)
        self.assertTrue(engine.supports_bidi)
        self.assertEqual(engine.engine_kind, EngineKind.BIDI)
        self.assertEqual(engine.protocol, EngineProtocol.WEBDRIVER_BIDI)
        self.assertEqual(engine.endpoint.host, "grid.example")

    def test_explicit_spec_protocol_wins(self) -> None:
        spec = EngineSpec(
            kind=EngineKind.CHROMIUM,
            protocol=EngineProtocol.CDP,
            remote_endpoint="ws://grid.example:9222/cdp",
        )
        engine = RemoteGridEngine(spec)
        self.assertEqual(engine.protocol, EngineProtocol.CDP)

    def test_missing_endpoint_raises(self) -> None:
        with self.assertRaises(ValueError):
            RemoteGridEngine(EngineSpec(kind=EngineKind.CHROMIUM))

    def test_bad_endpoint_scheme_raises_on_construction(self) -> None:
        spec = EngineSpec(kind=EngineKind.CHROMIUM, remote_endpoint="ftp://grid.example")
        with self.assertRaises(ValueError):
            RemoteGridEngine(spec)

    def test_credentials_are_redacted(self) -> None:
        spec = EngineSpec(
            kind=EngineKind.CHROMIUM,
            remote_endpoint="wss://user:secret@grid.example:4444/cdp",
        )
        engine = RemoteGridEngine(spec)
        safe = engine.safe_endpoint
        self.assertNotIn("secret", safe)
        self.assertNotIn("user", safe)
        self.assertIn("grid.example", safe)
        self.assertNotIn("secret", repr(engine))

    def test_new_context_returns_page_driver_without_browser(self) -> None:
        spec = EngineSpec(kind=EngineKind.CHROMIUM, remote_endpoint="ws://grid.example:9222/cdp")
        engine = RemoteGridEngine(spec)
        driver = engine.new_context()
        self.assertIsInstance(driver, PageDriver)
        # close() with no launched browser must be a no-op (no deps required).
        engine.close()


if __name__ == "__main__":  # pragma: no cover
    unittest.main()

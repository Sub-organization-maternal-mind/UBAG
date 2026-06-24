"""Unit tests for the cross-engine browser-driver abstraction (engines.py).

These tests run fully offline: no browser, no network, no playwright/selenium
installed. They validate the EngineSpec default protocol mapping, config-driven
engine selection (from an EngineSpec and from environment variables), the
dependency-free MockEngine, and the engine-portable selector ordering (§13.12).
"""

import os
import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.live.engines import (  # noqa: E402
    DEFAULT_PROTOCOL_FOR_KIND,
    Engine,
    EngineKind,
    EngineProtocol,
    EngineSpec,
    LocalPlaywrightEngine,
    MockEngine,
    NullEngine,
    classify_selector,
    default_protocol_for,
    engine_spec_from_env,
    portable_strategy_order,
    select_engine,
)
from ubag_worker.live.page_driver import MockPageDriver  # noqa: E402
from ubag_worker.live.selectors import SelectorGroup  # noqa: E402

_ENGINE_ENV_VARS = (
    "UBAG_BROWSER_ENGINE",
    "UBAG_BROWSER_PROTOCOL",
    "UBAG_REMOTE_BROWSER_ENDPOINT",
    "UBAG_BROWSER_HEADED",
)


class EngineEnvCleanupMixin(unittest.TestCase):
    """Snapshot and restore the engine-related environment variables."""

    def setUp(self) -> None:
        self._saved_env = {name: os.environ.get(name) for name in _ENGINE_ENV_VARS}
        for name in _ENGINE_ENV_VARS:
            os.environ.pop(name, None)

    def tearDown(self) -> None:
        for name, value in self._saved_env.items():
            if value is None:
                os.environ.pop(name, None)
            else:
                os.environ[name] = value


class EngineSpecDefaultProtocolTests(unittest.TestCase):
    def test_default_protocol_mapping_per_kind(self) -> None:
        expected = {
            EngineKind.CHROMIUM: EngineProtocol.CDP,
            EngineKind.FIREFOX: EngineProtocol.PLAYWRIGHT,
            EngineKind.WEBKIT: EngineProtocol.PLAYWRIGHT,
            EngineKind.BIDI: EngineProtocol.WEBDRIVER_BIDI,
        }
        self.assertEqual(DEFAULT_PROTOCOL_FOR_KIND, expected)
        for kind, protocol in expected.items():
            self.assertEqual(default_protocol_for(kind), protocol)

    def test_spec_fills_protocol_from_kind(self) -> None:
        self.assertEqual(EngineSpec(kind=EngineKind.CHROMIUM).protocol, EngineProtocol.CDP)
        self.assertEqual(EngineSpec(kind=EngineKind.BIDI).protocol, EngineProtocol.WEBDRIVER_BIDI)
        self.assertEqual(
            EngineSpec(kind=EngineKind.WEBKIT).protocol, EngineProtocol.PLAYWRIGHT
        )

    def test_explicit_protocol_is_preserved(self) -> None:
        spec = EngineSpec(kind=EngineKind.CHROMIUM, protocol=EngineProtocol.WEBDRIVER_BIDI)
        self.assertEqual(spec.protocol, EngineProtocol.WEBDRIVER_BIDI)
        self.assertTrue(spec.supports_bidi)

    def test_spec_remote_and_bidi_flags(self) -> None:
        local = EngineSpec(kind=EngineKind.CHROMIUM)
        self.assertFalse(local.is_remote)
        self.assertFalse(local.supports_bidi)

        remote = EngineSpec(kind=EngineKind.BIDI, remote_endpoint="wss://grid/bidi")
        self.assertTrue(remote.is_remote)
        self.assertTrue(remote.supports_bidi)


class SelectEngineFromSpecTests(EngineEnvCleanupMixin):
    def test_default_is_local_chromium_cdp(self) -> None:
        engine = select_engine()
        self.assertIsInstance(engine, LocalPlaywrightEngine)
        self.assertIsInstance(engine, Engine)
        self.assertEqual(engine.engine_kind, EngineKind.CHROMIUM)
        self.assertEqual(engine.protocol, EngineProtocol.CDP)
        self.assertFalse(engine.is_remote)

    def test_select_from_spec_local_firefox(self) -> None:
        engine = select_engine(EngineSpec(kind=EngineKind.FIREFOX))
        self.assertIsInstance(engine, LocalPlaywrightEngine)
        self.assertEqual(engine.engine_kind, EngineKind.FIREFOX)
        self.assertEqual(engine.protocol, EngineProtocol.PLAYWRIGHT)
        self.assertEqual(engine.browser_type_name, "firefox")

    def test_select_from_spec_webkit(self) -> None:
        engine = select_engine(EngineSpec(kind=EngineKind.WEBKIT))
        self.assertEqual(engine.browser_type_name, "webkit")

    def test_remote_spec_returns_remote_grid_engine(self) -> None:
        from ubag_worker.live.remote import RemoteGridEngine

        spec = EngineSpec(kind=EngineKind.BIDI, remote_endpoint="wss://grid.example/bidi")
        engine = select_engine(spec)
        self.assertIsInstance(engine, RemoteGridEngine)
        self.assertTrue(engine.is_remote)
        self.assertTrue(engine.supports_bidi)


class SelectEngineFromEnvTests(EngineEnvCleanupMixin):
    def test_env_unset_defaults_to_chromium_cdp(self) -> None:
        spec = engine_spec_from_env()
        self.assertEqual(spec.kind, EngineKind.CHROMIUM)
        self.assertEqual(spec.protocol, EngineProtocol.CDP)
        self.assertIsNone(spec.remote_endpoint)
        self.assertFalse(spec.headed)

    def test_env_engine_and_protocol(self) -> None:
        os.environ["UBAG_BROWSER_ENGINE"] = "firefox"
        os.environ["UBAG_BROWSER_PROTOCOL"] = "bidi"
        os.environ["UBAG_BROWSER_HEADED"] = "true"
        engine = select_engine()
        self.assertIsInstance(engine, LocalPlaywrightEngine)
        self.assertEqual(engine.engine_kind, EngineKind.FIREFOX)
        self.assertEqual(engine.protocol, EngineProtocol.WEBDRIVER_BIDI)

    def test_env_remote_endpoint_returns_remote_grid_engine(self) -> None:
        from ubag_worker.live.remote import RemoteGridEngine

        os.environ["UBAG_REMOTE_BROWSER_ENDPOINT"] = "wss://grid.example:4444/bidi"
        engine = select_engine()
        self.assertIsInstance(engine, RemoteGridEngine)
        self.assertTrue(engine.is_remote)

    def test_invalid_engine_env_raises(self) -> None:
        os.environ["UBAG_BROWSER_ENGINE"] = "netscape"
        with self.assertRaises(ValueError):
            select_engine()

    def test_invalid_protocol_env_raises(self) -> None:
        os.environ["UBAG_BROWSER_PROTOCOL"] = "carrier-pigeon"
        with self.assertRaises(ValueError):
            select_engine()


class MockEngineTests(unittest.TestCase):
    def test_mock_engine_needs_no_dependencies(self) -> None:
        engine = MockEngine()
        engine.launch()  # must not import any browser library
        driver = engine.new_context()
        self.assertIsInstance(driver, MockPageDriver)
        self.assertFalse(engine.is_remote)
        engine.close()

    def test_null_engine_is_mock_engine_alias(self) -> None:
        self.assertIs(NullEngine, MockEngine)
        self.assertIsInstance(NullEngine(), MockEngine)

    def test_mock_engine_new_context_honors_options(self) -> None:
        engine = MockEngine()
        driver = engine.new_context(
            {
                "offline_authenticated": False,
                "offline_response": "hello world",
                "offline_tokens": ["he", "llo"],
            }
        )
        self.assertIsInstance(driver, MockPageDriver)
        self.assertFalse(driver.authenticated)
        self.assertEqual(driver.response_text, "hello world")
        self.assertEqual(list(driver.tokens), ["he", "llo"])

    def test_mock_engine_kind_and_protocol(self) -> None:
        engine = MockEngine(kind=EngineKind.WEBKIT)
        self.assertEqual(engine.engine_kind, EngineKind.WEBKIT)
        self.assertEqual(engine.protocol, EngineProtocol.PLAYWRIGHT)


class PortableSelectorOrderTests(unittest.TestCase):
    def test_classify_selector(self) -> None:
        self.assertEqual(classify_selector("role=button"), "accessibility-role")
        self.assertEqual(classify_selector("[role='textbox']"), "accessibility-role")
        self.assertEqual(classify_selector("button[aria-label='Send']"), "aria")
        self.assertEqual(classify_selector("bidi:role/button"), "bidi")
        self.assertEqual(classify_selector("button[data-testid='send']"), "test-id")
        self.assertEqual(classify_selector("text=Log in"), "text")
        self.assertEqual(classify_selector("div.markdown.prose"), "css")

    def test_aria_role_bidi_ordered_before_css_and_ml(self) -> None:
        group = SelectorGroup(
            "mixed",
            (
                "div.markdown.prose",  # css
                "button[data-testid='send']",  # test-id
                "button[aria-label='Send']",  # aria
                "role=button",  # accessibility-role
                "bidi:role/button",  # bidi
            ),
        )
        order = portable_strategy_order(group)
        # ML vision is always present and always last.
        self.assertEqual(order[-1], "ml-vision")
        # Portable strategies precede css and the ML fallback.
        for portable in ("accessibility-role", "aria", "bidi"):
            self.assertIn(portable, order)
            self.assertLess(order.index(portable), order.index("css"))
            self.assertLess(order.index(portable), order.index("ml-vision"))
        # test-id / css are after the portable strategies.
        self.assertLess(order.index("aria"), order.index("test-id"))
        self.assertLess(order.index("test-id"), order.index("css"))

    def test_css_only_group_still_appends_ml_last(self) -> None:
        group = SelectorGroup("css_only", ("div.a", "#b", "section > p"))
        order = portable_strategy_order(group)
        self.assertEqual(order, ["css", "ml-vision"])


if __name__ == "__main__":  # pragma: no cover
    unittest.main()

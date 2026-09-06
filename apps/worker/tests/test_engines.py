"""Unit tests for engines.py: env-driven EngineSpec parsing.

These tests run fully offline: no browser, no network, no playwright installed.
They cover what production actually consumes: UBAG_BROWSER_ENGINE /
UBAG_REMOTE_BROWSER_ENDPOINT / UBAG_BROWSER_HEADED parsing and validation.
"""

import os
import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.live.engines import (  # noqa: E402
    EngineKind,
    EngineSpec,
    engine_spec_from_env,
)

_ENGINE_ENV_VARS = (
    "UBAG_BROWSER_ENGINE",
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


class EngineSpecTests(unittest.TestCase):
    def test_defaults_are_local_chromium_headless(self) -> None:
        spec = EngineSpec()
        self.assertEqual(spec.kind, EngineKind.CHROMIUM)
        self.assertIsNone(spec.remote_endpoint)
        self.assertFalse(spec.is_remote)
        self.assertFalse(spec.headed)

    def test_remote_endpoint_sets_is_remote(self) -> None:
        spec = EngineSpec(remote_endpoint="http://grid:9222")
        self.assertTrue(spec.is_remote)


class EngineSpecFromEnvTests(EngineEnvCleanupMixin):
    def test_env_unset_defaults_to_chromium(self) -> None:
        spec = engine_spec_from_env()
        self.assertEqual(spec.kind, EngineKind.CHROMIUM)
        self.assertIsNone(spec.remote_endpoint)
        self.assertFalse(spec.headed)

    def test_env_parses_engine_endpoint_headed(self) -> None:
        os.environ["UBAG_BROWSER_ENGINE"] = "firefox"
        os.environ["UBAG_REMOTE_BROWSER_ENDPOINT"] = "http://grid:9222"
        os.environ["UBAG_BROWSER_HEADED"] = "true"
        spec = engine_spec_from_env()
        self.assertEqual(spec.kind, EngineKind.FIREFOX)
        self.assertEqual(spec.remote_endpoint, "http://grid:9222")
        self.assertTrue(spec.headed)
        self.assertTrue(spec.is_remote)

    def test_invalid_engine_env_raises(self) -> None:
        os.environ["UBAG_BROWSER_ENGINE"] = "netscape"
        with self.assertRaises(ValueError):
            engine_spec_from_env()


if __name__ == "__main__":  # pragma: no cover
    unittest.main()

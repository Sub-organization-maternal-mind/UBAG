"""Unit tests for the operator-configurable noVNC base URL (live-browser viewer).

These tests pin the security contract: the worker advertises a loopback noVNC URL
that the gateway will forward to operators, and falls back to the safe default
whenever the configured base is not a loopback ``http://host:port`` value. No
browser or network is touched.
"""

import os
import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.live.engine import _is_loopback_novnc_base, _novnc_url  # noqa: E402


class NoVNCBaseUrlTests(unittest.TestCase):
    def setUp(self) -> None:
        self._saved = os.environ.pop("UBAG_NOVNC_BASE_URL", None)

    def tearDown(self) -> None:
        if self._saved is None:
            os.environ.pop("UBAG_NOVNC_BASE_URL", None)
        else:
            os.environ["UBAG_NOVNC_BASE_URL"] = self._saved

    def test_default_is_unchanged(self) -> None:
        self.assertEqual(_novnc_url("sess_abc"), "http://127.0.0.1:7900/session/sess_abc")

    def test_env_override_loopback_host(self) -> None:
        os.environ["UBAG_NOVNC_BASE_URL"] = "http://127.0.0.1:6080"
        self.assertEqual(_novnc_url("sess_1"), "http://127.0.0.1:6080/session/sess_1")

    def test_env_override_localhost(self) -> None:
        os.environ["UBAG_NOVNC_BASE_URL"] = "http://localhost:7000/"
        self.assertEqual(_novnc_url("s"), "http://localhost:7000/session/s")

    def test_non_loopback_falls_back_to_default(self) -> None:
        os.environ["UBAG_NOVNC_BASE_URL"] = "http://example.invalid:7900"
        self.assertEqual(_novnc_url("s"), "http://127.0.0.1:7900/session/s")

    def test_https_falls_back_to_default(self) -> None:
        os.environ["UBAG_NOVNC_BASE_URL"] = "https://127.0.0.1:7900"
        self.assertEqual(_novnc_url("s"), "http://127.0.0.1:7900/session/s")

    def test_url_with_path_falls_back_to_default(self) -> None:
        os.environ["UBAG_NOVNC_BASE_URL"] = "http://127.0.0.1:7900/evil"
        self.assertEqual(_novnc_url("s"), "http://127.0.0.1:7900/session/s")

    def test_loopback_predicate(self) -> None:
        self.assertTrue(_is_loopback_novnc_base("http://127.0.0.1:7900"))
        self.assertTrue(_is_loopback_novnc_base("http://127.5.5.5:1"))
        self.assertTrue(_is_loopback_novnc_base("http://localhost:6080"))
        self.assertFalse(_is_loopback_novnc_base("http://10.0.0.1:7900"))
        self.assertFalse(_is_loopback_novnc_base("http://127.0.0.1"))
        self.assertFalse(_is_loopback_novnc_base("ftp://127.0.0.1:7900"))
        self.assertFalse(_is_loopback_novnc_base("http://127.0.0.1:70000"))


if __name__ == "__main__":
    unittest.main()

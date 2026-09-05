"""T10 wiring pins: the orchestrator flag path is inert by default and wires
LiveOrchestrator when enabled (ADR-0005)."""

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from ubag_worker.live.daemon import WarmWorkerDaemon  # noqa: E402


class OrchestrationWiringTests(unittest.TestCase):
    def test_daemon_default_has_no_orchestrator(self):
        daemon = WarmWorkerDaemon()
        self.assertIsNone(daemon._orchestrator)

    def test_daemon_accepts_an_orchestrator(self):
        sentinel = object()
        daemon = WarmWorkerDaemon(orchestrator=sentinel)
        self.assertIs(daemon._orchestrator, sentinel)

    def test_flag_helper_is_inert_by_default(self):
        import os

        from run_worker_daemon import _orchestrator_if_enabled

        for raw in ("", "0", "false", "no", "off", "  "):
            os.environ["UBAG_ORCHESTRATOR_ENABLED"] = raw
            try:
                self.assertIsNone(_orchestrator_if_enabled())
            finally:
                os.environ.pop("UBAG_ORCHESTRATOR_ENABLED", None)

    def test_flag_helper_wires_live_orchestrator_when_enabled(self):
        import os

        os.environ["UBAG_ORCHESTRATOR_ENABLED"] = "1"
        try:
            from run_worker_daemon import _orchestrator_if_enabled

            orch = _orchestrator_if_enabled()
            self.assertIsNotNone(orch)
            from ubag_worker.live.orchestrator import LiveOrchestrator

            self.assertIsInstance(orch, LiveOrchestrator)
        finally:
            os.environ.pop("UBAG_ORCHESTRATOR_ENABLED", None)


if __name__ == "__main__":
    unittest.main()

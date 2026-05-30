"""Tests for the AIMD adaptive concurrency controller (§12.9).

Covers additive increase after a success window, multiplicative decrease with
cooldown, cooldown suppression of increases, and cap-change record emission.
"""

import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.orchestration import AIMDController, NegativeSignal  # noqa: E402


class _FakeClock:
    def __init__(self, value=0.0):
        self.value = value

    def __call__(self):
        return self.value

    def advance(self, seconds):
        self.value += seconds


class AIMDTests(unittest.TestCase):
    def test_starts_at_conservative_cap(self):
        ctrl = AIMDController(start=2)
        self.assertEqual(ctrl.cap, 2)

    def test_additive_increase_after_success_window(self):
        clock = _FakeClock()
        ctrl = AIMDController(start=2, success_window=3, clock=clock)
        self.assertIsNone(ctrl.record_success())
        self.assertIsNone(ctrl.record_success())
        change = ctrl.record_success()
        self.assertIsNotNone(change)
        self.assertEqual((change.old, change.new), (2, 3))
        self.assertEqual(change.reason, "additive_increase")
        self.assertEqual(ctrl.cap, 3)

    def test_multiplicative_decrease_and_record(self):
        clock = _FakeClock()
        ctrl = AIMDController(start=6, clock=clock)
        change = ctrl.record_signal(NegativeSignal.HTTP_429)
        self.assertEqual((change.old, change.new), (6, 3))
        self.assertEqual(change.reason, NegativeSignal.HTTP_429.value)
        self.assertEqual(ctrl.cap, 3)

    def test_decrease_floor_is_respected(self):
        ctrl = AIMDController(start=2, floor=1)
        ctrl.record_signal(NegativeSignal.CAPTCHA)
        self.assertEqual(ctrl.cap, 1)
        # Already at floor; still records the cooldown entry observably.
        change = ctrl.record_signal(NegativeSignal.CAPTCHA)
        self.assertEqual((change.old, change.new), (1, 1))
        self.assertEqual(len(ctrl.cap_changes), 2)

    def test_cooldown_suppresses_increase(self):
        clock = _FakeClock()
        ctrl = AIMDController(
            start=4, success_window=1, cooldown=10.0, clock=clock
        )
        ctrl.record_signal(NegativeSignal.LATENCY_GUARD)
        self.assertTrue(ctrl.in_cooldown())
        # During cooldown a single success does not raise the cap.
        self.assertIsNone(ctrl.record_success())
        self.assertEqual(ctrl.cap, 2)
        # After cooldown elapses, increases resume.
        clock.advance(11.0)
        self.assertFalse(ctrl.in_cooldown())
        change = ctrl.record_success()
        self.assertIsNotNone(change)
        self.assertEqual(ctrl.cap, 3)

    def test_ceiling_max_caps_increase(self):
        ctrl = AIMDController(start=2, success_window=1, ceiling_max=2)
        self.assertIsNone(ctrl.record_success())
        self.assertEqual(ctrl.cap, 2)

    def test_cap_changes_accumulate_for_observability(self):
        clock = _FakeClock()
        ctrl = AIMDController(start=2, success_window=1, cooldown=0.0, clock=clock)
        ctrl.record_success()  # -> 3
        ctrl.record_signal(NegativeSignal.ERROR_RATE_SPIKE)  # -> 1
        reasons = [c.reason for c in ctrl.cap_changes]
        self.assertEqual(
            reasons, ["additive_increase", NegativeSignal.ERROR_RATE_SPIKE.value]
        )


if __name__ == "__main__":
    unittest.main()

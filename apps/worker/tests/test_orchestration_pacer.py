"""Tests for the global submit pacer (§12.9).

Covers minimum-gap spacing, deterministic jitter via an injected RNG, the
allow()/acquire() contract, and per-(provider, identity) registry isolation.
"""

import random
import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.orchestration import SubmitPacer, SubmitPacerRegistry  # noqa: E402


class _FakeClock:
    def __init__(self, value=0.0):
        self.value = value

    def __call__(self):
        return self.value

    def advance(self, seconds):
        self.value += seconds


class PacerTests(unittest.TestCase):
    def test_first_submit_allowed_immediately(self):
        clock = _FakeClock()
        pacer = SubmitPacer(base_gap=0.8, jitter=0.0, clock=clock)
        self.assertTrue(pacer.allow())
        self.assertEqual(pacer.acquire(), 0.0)

    def test_spacing_without_jitter(self):
        clock = _FakeClock()
        pacer = SubmitPacer(base_gap=0.8, jitter=0.0, clock=clock)
        first = pacer.acquire()
        self.assertEqual(first, 0.0)
        # Immediately afterwards a submit is blocked until the gap elapses.
        self.assertFalse(pacer.allow())
        clock.advance(0.8)
        self.assertTrue(pacer.allow())
        self.assertEqual(pacer.acquire(), 0.8)

    def test_jitter_is_deterministic_with_seeded_rng(self):
        clock_a = _FakeClock()
        clock_b = _FakeClock()
        pacer_a = SubmitPacer(
            base_gap=0.8, jitter=0.4, rng=random.Random(42), clock=clock_a
        )
        pacer_b = SubmitPacer(
            base_gap=0.8, jitter=0.4, rng=random.Random(42), clock=clock_b
        )
        gaps_a = []
        gaps_b = []
        for _ in range(5):
            before = pacer_a.next_allowed
            pacer_a.acquire(now=10_000.0)  # large now so each acquire advances gate
            gaps_a.append(round(pacer_a.next_allowed - 10_000.0, 9))
        for _ in range(5):
            pacer_b.acquire(now=10_000.0)
            gaps_b.append(round(pacer_b.next_allowed - 10_000.0, 9))
        self.assertEqual(gaps_a, gaps_b)

    def test_jitter_within_bounds(self):
        clock = _FakeClock()
        pacer = SubmitPacer(
            base_gap=0.8, jitter=0.4, rng=random.Random(7), clock=clock
        )
        prev = 0.0
        pacer.acquire(now=0.0)
        for _ in range(50):
            now = pacer.next_allowed
            gate_before = pacer.next_allowed
            pacer.acquire(now=now)
            gap = pacer.next_allowed - gate_before
            self.assertGreaterEqual(gap, 0.8 - 0.4 - 1e-9)
            self.assertLessEqual(gap, 0.8 + 0.4 + 1e-9)

    def test_registry_isolates_keys(self):
        clock = _FakeClock()
        registry = SubmitPacerRegistry(base_gap=0.8, jitter=0.0, clock=clock)
        p1 = registry.get("chatgpt_web", "acct_a")
        p2 = registry.get("chatgpt_web", "acct_b")
        same = registry.get("chatgpt_web", "acct_a")
        self.assertIs(p1, same)
        self.assertIsNot(p1, p2)
        p1.acquire(now=0.0)
        # p2 is independent: still immediately allowed.
        self.assertTrue(p2.allow(now=0.0))


if __name__ == "__main__":
    unittest.main()

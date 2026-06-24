"""Tests for humanized browser interaction helpers."""
import random

from ubag_worker.live.humanized import (
    HumanizedConfig,
    Point,
    bezier_path,
    should_block_url,
    type_with_delays,
    typing_delays,
)


class TestTypingDelays:
    def test_returns_one_delay_per_char_no_typos(self):
        cfg = HumanizedConfig(cps_base=10.0, cps_jitter=0.0, typo_probability=0.0)
        delays = typing_delays("hello", cfg)
        assert len(delays) == 5

    def test_delays_are_positive(self):
        delays = typing_delays("abc", HumanizedConfig(cps_base=5.0, typo_probability=0.0))
        assert all(d > 0 for d in delays)

    def test_jitter_produces_variation(self):
        cfg = HumanizedConfig(cps_base=10.0, cps_jitter=0.5, typo_probability=0.0)
        rng = random.Random(42)
        delays = typing_delays("abcdefgh", cfg, rng=rng)
        # With 50% jitter the delays must not all be identical
        assert len(set(round(d, 6) for d in delays)) > 1

    def test_typo_injects_extra_delays(self):
        # Force typo probability = 1.0: every char gets +2 extra delays
        cfg = HumanizedConfig(cps_base=10.0, cps_jitter=0.0, typo_probability=1.0)
        delays = typing_delays("ab", cfg)
        # 2 chars × (1 typo + 1 backspace + 1 real) = 6
        assert len(delays) == 6

    def test_empty_text_returns_no_delays(self):
        assert typing_delays("") == []


class TestTypeWithDelays:
    def test_calls_type_fn_for_each_char(self):
        typed = []
        sleeping = []

        def fake_type(ch):
            typed.append(ch)

        cfg = HumanizedConfig(cps_base=100.0, cps_jitter=0.0, typo_probability=0.0)
        type_with_delays("hi", fake_type, cfg, clock=sleeping.append)
        assert typed == ["h", "i"]
        assert len(sleeping) == 2

    def test_no_real_sleep_with_injected_clock(self):
        import time
        slept = []
        cfg = HumanizedConfig(cps_base=1000.0, typo_probability=0.0)
        start = time.monotonic()
        type_with_delays("x" * 20, lambda c: None, cfg, clock=slept.append)
        elapsed = time.monotonic() - start
        assert elapsed < 0.1, "should not have slept with injected clock"
        assert len(slept) == 20


class TestBezierPath:
    def test_start_and_end_match(self):
        start = Point(0, 0)
        end = Point(100, 200)
        path = bezier_path(start, end, HumanizedConfig(bezier_steps=10))
        # First and last points should be within floating-point tolerance
        assert abs(path[0].x - start.x) < 1e-6
        assert abs(path[-1].x - end.x) < 1e-6
        assert abs(path[-1].y - end.y) < 1e-6

    def test_correct_number_of_steps(self):
        cfg = HumanizedConfig(bezier_steps=15)
        path = bezier_path(Point(0, 0), Point(50, 50), cfg)
        assert len(path) == 15

    def test_path_has_non_linear_points(self):
        cfg = HumanizedConfig(bezier_steps=20, bezier_jitter=0.5)
        rng = random.Random(99)
        path = bezier_path(Point(0, 0), Point(100, 0), cfg, rng=rng)
        # With jitter, some y-values should be non-zero (off the straight line)
        assert any(abs(p.y) > 0.1 for p in path[1:-1])

    def test_same_start_end_produces_stable_path(self):
        p = Point(50, 50)
        path = bezier_path(p, p, HumanizedConfig(bezier_steps=5))
        assert len(path) == 5


class TestShouldBlockUrl:
    def test_blocks_known_tracker_domains(self):
        blocked = [
            "https://www.googletagmanager.com/gtm.js",
            "https://doubleclick.net/pixel",
            "http://www.google-analytics.com/analytics.js",
            "https://hotjar.com/c/hotjar-123.js",
            "https://cdn.taboola.com/libtrc.min.js",
        ]
        for url in blocked:
            assert should_block_url(url), f"should have blocked: {url}"

    def test_allows_provider_urls(self):
        allowed = [
            "https://chatgpt.com/",
            "https://claude.ai/new",
            "https://chat.deepseek.com/",
            "https://gemini.google.com/app",
        ]
        for url in allowed:
            assert not should_block_url(url), f"should not have blocked: {url}"

    def test_case_insensitive(self):
        assert should_block_url("https://DoubleClick.Net/tracker")


class TestPatchrightEngineImport:
    def test_patchright_engine_is_exported(self):
        # Without env var, should return LocalPlaywrightEngine
        import os

        from ubag_worker.live.engines import select_engine
        os.environ.pop("UBAG_USE_PATCHRIGHT", None)
        engine = select_engine()
        from ubag_worker.live.engines import LocalPlaywrightEngine
        assert isinstance(engine, LocalPlaywrightEngine)

    def test_patchright_engine_selected_when_env_set(self):
        import os

        from ubag_worker.live.engines import PatchrightEngine, select_engine
        os.environ["UBAG_USE_PATCHRIGHT"] = "1"
        try:
            engine = select_engine()
            assert isinstance(engine, PatchrightEngine)
        finally:
            os.environ.pop("UBAG_USE_PATCHRIGHT", None)

    def test_patchright_engine_selected_when_stealth_spec(self):
        from ubag_worker.live.engines import EngineSpec, PatchrightEngine, select_engine
        spec = EngineSpec(stealth=True)
        engine = select_engine(spec)
        assert isinstance(engine, PatchrightEngine)

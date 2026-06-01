"""Tests for the steady-state hypothesis evaluator."""
from __future__ import annotations

import sys
import os

# Ensure harness package is importable when running from repo root
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", ".."))

import pytest
from chaos.harness.steady_state import (
    JobSample,
    SteadyStateConfig,
    compute_success_rate,
    evaluate_steady_state,
)


# ---------------------------------------------------------------------------
# compute_success_rate
# ---------------------------------------------------------------------------

def test_compute_success_rate_all_success():
    samples = [JobSample(succeeded=True)] * 10
    assert compute_success_rate(samples) == 1.0


def test_compute_success_rate_eight_of_ten():
    samples = [JobSample(succeeded=True)] * 8 + [JobSample(succeeded=False)] * 2
    assert compute_success_rate(samples) == pytest.approx(0.8)


def test_compute_success_rate_empty():
    assert compute_success_rate([]) == 0.0


# ---------------------------------------------------------------------------
# evaluate_steady_state
# ---------------------------------------------------------------------------

def test_evaluate_healthy():
    samples = [JobSample(succeeded=True)] * 10
    config = SteadyStateConfig(success_rate_threshold=0.95)
    result = evaluate_steady_state(ready_ok=True, samples=samples, config=config)
    assert result.is_healthy is True


def test_evaluate_gateway_not_ready():
    samples = [JobSample(succeeded=True)] * 10
    config = SteadyStateConfig(success_rate_threshold=0.95)
    result = evaluate_steady_state(ready_ok=False, samples=samples, config=config)
    assert result.is_healthy is False
    assert "ready" in result.reason


def test_evaluate_rate_exactly_at_threshold():
    # 19/20 = 0.95 exactly — inclusive threshold
    samples = [JobSample(succeeded=True)] * 19 + [JobSample(succeeded=False)] * 1
    config = SteadyStateConfig(success_rate_threshold=0.95, window_size=20)
    result = evaluate_steady_state(ready_ok=True, samples=samples, config=config)
    assert result.is_healthy is True


def test_evaluate_rate_below_threshold():
    samples = [JobSample(succeeded=True)] * 8 + [JobSample(succeeded=False)] * 2
    config = SteadyStateConfig(success_rate_threshold=0.95, window_size=10)
    result = evaluate_steady_state(ready_ok=True, samples=samples, config=config)
    assert result.is_healthy is False
    assert "success rate" in result.reason


def test_evaluate_window_uses_last_n_samples():
    # First 10 all succeed, last 10 all fail — window_size=10 should use last 10
    samples = (
        [JobSample(succeeded=True)] * 10
        + [JobSample(succeeded=False)] * 10
    )
    config = SteadyStateConfig(success_rate_threshold=0.95, window_size=10)
    result = evaluate_steady_state(ready_ok=True, samples=samples, config=config)
    assert result.success_rate == 0.0
    assert result.is_healthy is False


def test_evaluate_verdict_flips_at_threshold():
    config_90 = SteadyStateConfig(success_rate_threshold=0.9, window_size=10)

    # 9/10 successes = 0.9 >= 0.9 → healthy
    samples_9 = [JobSample(succeeded=True)] * 9 + [JobSample(succeeded=False)] * 1
    result_9 = evaluate_steady_state(ready_ok=True, samples=samples_9, config=config_90)
    assert result_9.is_healthy is True

    # 8/10 successes = 0.8 < 0.9 → not healthy
    samples_8 = [JobSample(succeeded=True)] * 8 + [JobSample(succeeded=False)] * 2
    result_8 = evaluate_steady_state(ready_ok=True, samples=samples_8, config=config_90)
    assert result_8.is_healthy is False

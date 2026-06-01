"""Steady-state hypothesis evaluator for UBAG chaos experiments.

Evaluates two conditions:
  1. Gateway /v1/ready returns HTTP 200.
  2. Rolling job success rate over a window of samples >= threshold.
"""
from __future__ import annotations

import statistics
from dataclasses import dataclass, field
from typing import Sequence


@dataclass
class JobSample:
    """One observation of job outcome."""
    succeeded: bool


@dataclass
class SteadyStateConfig:
    gateway_url: str = "http://localhost:8080"
    success_rate_threshold: float = 0.95  # 95%
    window_size: int = 10  # rolling window


@dataclass
class SteadyStateResult:
    ready_ok: bool = False
    success_rate: float = 0.0
    sample_count: int = 0
    threshold: float = 0.0
    is_healthy: bool = False
    reason: str = ""


def compute_success_rate(samples: Sequence[JobSample]) -> float:
    """Compute the fraction of succeeded samples in the window.

    Returns 0.0 for an empty window (not 1.0 — conservative default).
    """
    if not samples:
        return 0.0
    return sum(1 for s in samples if s.succeeded) / len(samples)


def evaluate_steady_state(
    ready_ok: bool,
    samples: Sequence[JobSample],
    config: SteadyStateConfig,
) -> SteadyStateResult:
    """Evaluate whether the system is in steady state.

    healthy = ready_ok AND success_rate >= threshold.
    Uses only the last window_size samples.
    """
    window = list(samples)[-config.window_size:]
    rate = compute_success_rate(window)

    reasons = []
    if not ready_ok:
        reasons.append("gateway /v1/ready not 200")
    if rate < config.success_rate_threshold:
        reasons.append(f"success rate {rate:.2%} < threshold {config.success_rate_threshold:.2%}")

    healthy = ready_ok and rate >= config.success_rate_threshold
    return SteadyStateResult(
        ready_ok=ready_ok,
        success_rate=rate,
        sample_count=len(window),
        threshold=config.success_rate_threshold,
        is_healthy=healthy,
        reason="; ".join(reasons) if reasons else "healthy",
    )

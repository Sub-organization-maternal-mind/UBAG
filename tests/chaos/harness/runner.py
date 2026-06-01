"""Chaos experiment runner (skeleton).

The actual fault injection uses toxiproxy/chaos-toolkit in integration mode.
This module provides the Python-testable structure.
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Callable, Optional, Sequence

from .steady_state import SteadyStateConfig, SteadyStateResult, JobSample, evaluate_steady_state


@dataclass
class ExperimentResult:
    title: str
    pre_steady_state: SteadyStateResult
    post_steady_state: SteadyStateResult
    fault_applied: bool = False
    recovered: bool = False

    @property
    def success(self) -> bool:
        """Experiment succeeded if pre was healthy and post recovered."""
        return self.pre_steady_state.is_healthy and self.post_steady_state.is_healthy


# Expose ubag_chaos_* metric names (for Prometheus in integration mode)
METRIC_EXPERIMENT_TOTAL = "ubag_chaos_experiment_total"
METRIC_EXPERIMENT_RECOVERED = "ubag_chaos_experiment_recovered"
METRIC_STEADY_STATE_HEALTHY = "ubag_chaos_steady_state_healthy"

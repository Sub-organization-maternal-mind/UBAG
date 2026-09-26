"""Quota and usage telemetry for Antigravity SDK adapter.

Tracks API usage metrics. Does NOT use agy-quota (that is CLI-only).
"""

from __future__ import annotations

import time
from collections import deque
from dataclasses import dataclass
from typing import Any, Dict, List


@dataclass
class UsageSnapshot:
    timestamp: float
    input_tokens: int
    output_tokens: int
    thinking_tokens: int
    cached_tokens: int
    total_tokens: int
    model: str
    latency_ms: float


@dataclass
class RateLimitEvent:
    timestamp: float
    retry_after_seconds: float
    concurrency_at_time: int


class UsageTracker:
    def __init__(self, max_history: int = 1000) -> None:
        self._history: deque = deque(maxlen=max_history)
        self._rate_limits: deque = deque(maxlen=100)
        self._total_input_tokens = 0
        self._total_output_tokens = 0
        self._total_thinking_tokens = 0
        self._total_cached_tokens = 0
        self._total_requests = 0
        self._total_failures = 0
        self._start_time = time.monotonic()

    def record_usage(
        self,
        input_tokens: int,
        output_tokens: int,
        thinking_tokens: int,
        cached_tokens: int,
        model: str,
        latency_ms: float,
    ) -> None:
        total = input_tokens + output_tokens + thinking_tokens
        snapshot = UsageSnapshot(
            timestamp=time.monotonic(),
            input_tokens=input_tokens,
            output_tokens=output_tokens,
            thinking_tokens=thinking_tokens,
            cached_tokens=cached_tokens,
            total_tokens=total,
            model=model,
            latency_ms=latency_ms,
        )
        self._history.append(snapshot)
        self._total_input_tokens += input_tokens
        self._total_output_tokens += output_tokens
        self._total_thinking_tokens += thinking_tokens
        self._total_cached_tokens += cached_tokens
        self._total_requests += 1

    def record_rate_limit(self, retry_after_seconds: float, concurrency: int) -> None:
        self._rate_limits.append(RateLimitEvent(
            timestamp=time.monotonic(),
            retry_after_seconds=retry_after_seconds,
            concurrency_at_time=concurrency,
        ))

    def record_failure(self) -> None:
        self._total_failures += 1

    def get_summary(self) -> Dict[str, Any]:
        elapsed = time.monotonic() - self._start_time
        hours = elapsed / 3600.0
        return {
            "total_requests": self._total_requests,
            "total_failures": self._total_failures,
            "total_input_tokens": self._total_input_tokens,
            "total_output_tokens": self._total_output_tokens,
            "total_thinking_tokens": self._total_thinking_tokens,
            "total_cached_tokens": self._total_cached_tokens,
            "total_tokens": self._total_input_tokens + self._total_output_tokens + self._total_thinking_tokens,
            "tokens_per_hour": (self._total_input_tokens + self._total_output_tokens + self._total_thinking_tokens) / hours if hours > 0 else 0,
            "requests_per_hour": self._total_requests / hours if hours > 0 else 0,
            "rate_limit_events": len(self._rate_limits),
            "elapsed_seconds": elapsed,
        }

    def get_recent_history(self, limit: int = 100) -> List[Dict[str, Any]]:
        recent = list(self._history)[-limit:]
        return [
            {
                "timestamp": s.timestamp,
                "input_tokens": s.input_tokens,
                "output_tokens": s.output_tokens,
                "thinking_tokens": s.thinking_tokens,
                "cached_tokens": s.cached_tokens,
                "total_tokens": s.total_tokens,
                "model": s.model,
                "latency_ms": s.latency_ms,
            }
            for s in recent
        ]


__all__ = ["UsageSnapshot", "RateLimitEvent", "UsageTracker"]

"""Health manager for Antigravity SDK adapter."""

from __future__ import annotations

import time
from dataclasses import dataclass
from typing import Any, Dict, List


@dataclass
class HealthStatus:
    healthy: bool = True
    last_check: float = 0.0
    consecutive_failures: int = 0
    total_requests: int = 0
    successful_requests: int = 0
    failed_requests: int = 0
    average_latency_ms: float = 0.0
    last_error: str = ""
    circuit_state: str = "closed"
    rate_limiter_concurrency: int = 0

    @property
    def success_rate(self) -> float:
        if self.total_requests == 0:
            return 1.0
        return self.successful_requests / self.total_requests

    def to_dict(self) -> Dict[str, Any]:
        return {
            "healthy": self.healthy,
            "last_check": self.last_check,
            "consecutive_failures": self.consecutive_failures,
            "total_requests": self.total_requests,
            "successful_requests": self.successful_requests,
            "failed_requests": self.failed_requests,
            "success_rate": self.success_rate,
            "average_latency_ms": self.average_latency_ms,
            "last_error": self.last_error,
            "circuit_state": self.circuit_state,
            "rate_limiter_concurrency": self.rate_limiter_concurrency,
        }


class HealthManager:
    def __init__(self, failure_threshold: int = 5, recovery_timeout: float = 60.0) -> None:
        self._failure_threshold = failure_threshold
        self._recovery_timeout = recovery_timeout
        self._status = HealthStatus()
        self._latencies: List[float] = []

    @property
    def status(self) -> HealthStatus:
        return self._status

    def record_request_start(self) -> None:
        self._status.total_requests += 1

    def record_success(self, latency_ms: float) -> None:
        self._status.successful_requests += 1
        self._status.consecutive_failures = 0
        self._status.healthy = True
        self._status.last_error = ""
        self._latencies.append(latency_ms)
        if len(self._latencies) > 100:
            self._latencies = self._latencies[-100:]
        self._status.average_latency_ms = sum(self._latencies) / len(self._latencies)
        self._status.last_check = time.monotonic()

    def record_failure(self, error: str, retryable: bool = True) -> None:
        self._status.failed_requests += 1
        self._status.consecutive_failures += 1
        self._status.last_error = error
        self._status.last_check = time.monotonic()
        if self._status.consecutive_failures >= self._failure_threshold:
            self._status.healthy = False
            self._status.circuit_state = "open"

    def update_circuit_state(self, state: str) -> None:
        self._status.circuit_state = state
        if state == "closed":
            self._status.healthy = True

    def update_rate_limiter(self, concurrency: int) -> None:
        self._status.rate_limiter_concurrency = concurrency

    def check_health(self) -> HealthStatus:
        if self._status.circuit_state == "open":
            if time.monotonic() - self._status.last_check >= self._recovery_timeout:
                self._status.circuit_state = "half_open"
        return self._status

    def get_metrics(self) -> Dict[str, Any]:
        return self._status.to_dict()


__all__ = ["HealthStatus", "HealthManager"]

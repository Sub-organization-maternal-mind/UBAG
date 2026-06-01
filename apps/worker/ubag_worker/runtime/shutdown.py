"""Graceful shutdown drainer for the UBAG worker process.

Mirrors the gateway's signal.NotifyContext + http.Server.Shutdown(10s) pattern:
  1. Install SIGTERM/SIGINT handler → set _shutdown_event
  2. Stop accepting new leases (_accepting = False)
  3. Drain in-flight tabs (wait up to grace_window)
  4. Requeue unfinished jobs via requeue_callback
  5. Close browsers/contexts

Usage:
    drainer = GracefulDrainer(
        orchestrator=live_orchestrator,   # provides set_accepting() + all_inflight_job_ids()
        requeue_callback=lambda job_id: ...,
        grace_window=30.0,
    )
    install_shutdown_handler(drainer)
    # ... main run loop checks drainer.accepting ...
    # On signal: drainer.drain() is called automatically
"""
from __future__ import annotations

import signal
import threading
import time
from dataclasses import dataclass, field
from typing import Callable, Optional, Protocol


class OrchestratorProtocol(Protocol):
    """Minimal interface the drainer needs from the orchestrator."""

    def all_inflight_job_ids(self) -> list[str]:
        """Returns all currently in-flight job IDs across all tenants/targets."""
        ...

    def set_accepting(self, accepting: bool) -> None:
        """Tell the orchestrator to stop accepting new leases."""
        ...


@dataclass
class ShutdownSummary:
    """Report produced by drain()."""

    drained_jobs: int = 0
    requeued_jobs: list[str] = field(default_factory=list)  # job IDs requeued
    timed_out: bool = False       # True if grace window expired before all jobs finished
    elapsed_seconds: float = 0.0


class GracefulDrainer:
    """Coordinates graceful worker shutdown.

    Thread-safe. install_shutdown_handler() wires this to OS signals.
    """

    def __init__(
        self,
        orchestrator: OrchestratorProtocol,
        requeue_callback: Optional[Callable[[str], None]] = None,
        grace_window: float = 30.0,
        poll_interval: float = 0.5,
    ) -> None:
        self._orchestrator = orchestrator
        self._requeue_callback = requeue_callback
        self._grace_window = grace_window
        self._poll_interval = poll_interval
        self._shutdown_event = threading.Event()
        self._summary: Optional[ShutdownSummary] = None

    @property
    def should_shutdown(self) -> bool:
        return self._shutdown_event.is_set()

    def request_shutdown(self) -> None:
        """Signal that shutdown should begin. Safe to call multiple times."""
        self._shutdown_event.set()

    def drain(self) -> ShutdownSummary:
        """Execute the shutdown sequence. Blocks until draining is complete.

        Phase 1: Stop accepting new leases.
        Phase 2: Wait for in-flight jobs (up to grace_window).
        Phase 3: Requeue any remaining in-flight jobs.
        Phase 4: Report summary.
        """
        start = time.monotonic()
        summary = ShutdownSummary()

        # Phase 1: stop accepting new work
        self._orchestrator.set_accepting(False)

        # Phase 2: drain in-flight jobs
        deadline = start + self._grace_window
        initial_inflight = self._orchestrator.all_inflight_job_ids()
        initial_count = len(initial_inflight)

        while time.monotonic() < deadline:
            remaining = self._orchestrator.all_inflight_job_ids()
            if not remaining:
                break
            time.sleep(self._poll_interval)
        else:
            summary.timed_out = True

        # Phase 3: requeue unfinished jobs
        still_inflight = self._orchestrator.all_inflight_job_ids()
        summary.drained_jobs = initial_count - len(still_inflight)
        if still_inflight and self._requeue_callback is not None:
            for job_id in still_inflight:
                self._requeue_callback(job_id)
                summary.requeued_jobs.append(job_id)

        summary.elapsed_seconds = time.monotonic() - start
        self._summary = summary
        return summary


def install_shutdown_handler(drainer: GracefulDrainer) -> None:
    """Install SIGTERM and SIGINT handlers that trigger drainer.drain().

    The drain runs in a daemon thread so the signal handler returns immediately.
    A guard ensures only one drain thread is ever started, even if the signal
    fires multiple times.
    """
    _drain_started = threading.Event()

    def _handler(signum: int, _frame: object) -> None:
        drainer.request_shutdown()
        if not _drain_started.is_set():
            _drain_started.set()
            t = threading.Thread(target=drainer.drain, daemon=True, name="shutdown-drain")
            t.start()

    signal.signal(signal.SIGTERM, _handler)
    signal.signal(signal.SIGINT, _handler)

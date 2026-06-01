"""Tests for ubag_worker.runtime.shutdown (Task 2.3: Graceful worker shutdown)."""

from __future__ import annotations

import signal
import threading
import time
from typing import List

import pytest

from ubag_worker.runtime.shutdown import (
    GracefulDrainer,
    ShutdownSummary,
    install_shutdown_handler,
)


# ---------------------------------------------------------------------------
# Mock orchestrator
# ---------------------------------------------------------------------------

class MockOrchestrator:
    """Satisfies OrchestratorProtocol without inheriting from it."""

    def __init__(self, initial_job_ids: List[str] | None = None) -> None:
        self._job_ids: List[str] = list(initial_job_ids or [])
        self._accepting: bool = True
        self._accepting_calls: List[bool] = []
        self._lock = threading.Lock()

    # --- protocol methods ---

    def concurrency_state(self, tenant_id: str = "") -> object:
        class _State:
            inflight = 0
        return _State()

    def all_inflight_job_ids(self) -> List[str]:
        with self._lock:
            return list(self._job_ids)

    def set_accepting(self, accepting: bool) -> None:
        with self._lock:
            self._accepting = accepting
            self._accepting_calls.append(accepting)

    # --- test helpers ---

    def clear_jobs_after(self, delay: float) -> None:
        """Clear all inflight job IDs after *delay* seconds (runs in daemon thread)."""
        def _clear() -> None:
            time.sleep(delay)
            with self._lock:
                self._job_ids.clear()

        t = threading.Thread(target=_clear, daemon=True)
        t.start()


# ---------------------------------------------------------------------------
# Test 1: request_shutdown + drain sets accepting=False and should_shutdown=True
# ---------------------------------------------------------------------------

def test_request_shutdown_sets_flag_and_stops_accepting() -> None:
    orch = MockOrchestrator(initial_job_ids=[])
    drainer = GracefulDrainer(orchestrator=orch, grace_window=2.0, poll_interval=0.1)

    assert not drainer.should_shutdown

    drainer.request_shutdown()
    assert drainer.should_shutdown

    drainer.drain()

    assert False in orch._accepting_calls or True in orch._accepting_calls
    assert orch._accepting is False
    assert False in orch._accepting_calls


# ---------------------------------------------------------------------------
# Test 2: all in-flight jobs drain within grace window → clean summary
# ---------------------------------------------------------------------------

def test_drain_within_grace_window() -> None:
    job_ids = ["job-1", "job-2", "job-3"]
    orch = MockOrchestrator(initial_job_ids=job_ids)
    # Jobs finish after 0.15 s — well within the 5 s grace window
    orch.clear_jobs_after(0.15)

    requeued: List[str] = []
    drainer = GracefulDrainer(
        orchestrator=orch,
        requeue_callback=requeued.append,
        grace_window=5.0,
        poll_interval=0.05,
    )

    summary = drainer.drain()

    assert summary.timed_out is False
    assert summary.drained_jobs == len(job_ids)
    assert summary.requeued_jobs == []
    assert requeued == []
    assert summary.elapsed_seconds < 5.0


# ---------------------------------------------------------------------------
# Test 3: grace window times out → timed_out=True, requeue_callback called
# ---------------------------------------------------------------------------

def test_grace_window_timeout_triggers_requeue() -> None:
    job_ids = ["job-a", "job-b"]
    orch = MockOrchestrator(initial_job_ids=job_ids)
    # Jobs never finish within the tiny grace window

    requeued: List[str] = []
    drainer = GracefulDrainer(
        orchestrator=orch,
        requeue_callback=requeued.append,
        grace_window=0.1,   # tiny window → must time out
        poll_interval=0.05,
    )

    summary = drainer.drain()

    assert summary.timed_out is True
    assert set(summary.requeued_jobs) == set(job_ids)
    assert set(requeued) == set(job_ids)


# ---------------------------------------------------------------------------
# Test 4: no requeue_callback → no error, unfinished jobs not in summary
# ---------------------------------------------------------------------------

def test_drain_no_requeue_callback_no_error() -> None:
    job_ids = ["job-x"]
    orch = MockOrchestrator(initial_job_ids=job_ids)
    # Jobs never finish

    drainer = GracefulDrainer(
        orchestrator=orch,
        requeue_callback=None,   # no callback
        grace_window=0.1,
        poll_interval=0.05,
    )

    # Should not raise
    summary = drainer.drain()

    assert summary.timed_out is True
    # No callback → requeued_jobs stays empty even though there are inflight jobs
    assert summary.requeued_jobs == []


# ---------------------------------------------------------------------------
# Test 5: install_shutdown_handler is callable without error
# ---------------------------------------------------------------------------

def test_install_shutdown_handler_registers_signals() -> None:
    orch = MockOrchestrator()
    drainer = GracefulDrainer(orchestrator=orch, grace_window=1.0)

    # Should not raise; verifies signal.signal() calls succeed
    install_shutdown_handler(drainer)

    # Restore default handlers so we don't affect other tests
    signal.signal(signal.SIGTERM, signal.SIG_DFL)
    signal.signal(signal.SIGINT, signal.SIG_DFL)

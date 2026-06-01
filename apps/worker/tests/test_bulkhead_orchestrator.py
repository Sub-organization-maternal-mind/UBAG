"""Tests for bulkhead admission + crash recovery wired into LiveOrchestrator — Task 2.2."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import pytest

from ubag_worker.live.orchestrator import LiveOrchestrator
from ubag_worker.orchestration.bulkhead import (
    BulkheadConfig,
    BulkheadRegistry,
    CrashLevel,
    RecoveryAction,
)
from ubag_worker.orchestration.topology import ChannelTab, TabState


class _FakeClock:
    def __init__(self, value: float = 0.0) -> None:
        self.value = value

    def __call__(self) -> float:
        return self.value

    def advance(self, seconds: float) -> None:
        self.value += seconds


# ---------------------------------------------------------------------------
# Test 1: Lease past target ceiling is refused (returns backpressure)
# ---------------------------------------------------------------------------


def test_lease_refused_when_target_ceiling_full():
    """A second lease for the same target must be rejected when max_tabs_per_target=1."""
    config = BulkheadConfig(max_tabs_per_tenant=10, max_tabs_per_target=1)
    bulkhead = BulkheadRegistry(config)

    # Pre-fill the target ceiling so there is no room for the orchestrator's lease.
    assert bulkhead.try_acquire("tenant-a", "chatgpt_web") is True

    orch = LiveOrchestrator(
        clock=_FakeClock(),
        bulkhead=bulkhead,
    )

    lease = orch.lease(
        tenant_id="tenant-a",
        provider_id="chatgpt_web",
        identity_ref="acct-1",
        job_id="job-backpressure",
    )

    # The lease must carry no tab — the job stays queued (backpressure).
    assert lease.tab is None
    assert not lease.assigned


def test_lease_succeeds_when_target_has_room():
    """When the bulkhead has space, lease() should still assign a tab normally."""
    config = BulkheadConfig(max_tabs_per_tenant=10, max_tabs_per_target=5)
    bulkhead = BulkheadRegistry(config)

    orch = LiveOrchestrator(
        clock=_FakeClock(),
        bulkhead=bulkhead,
    )

    lease = orch.lease(
        tenant_id="tenant-a",
        provider_id="chatgpt_web",
        identity_ref="acct-1",
        job_id="job-ok",
    )

    assert lease.tab is not None
    assert lease.assigned


def test_bulkhead_slot_released_after_record_outcome():
    """record_outcome() must release the bulkhead slot so the next lease can proceed."""
    config = BulkheadConfig(max_tabs_per_tenant=10, max_tabs_per_target=1)
    bulkhead = BulkheadRegistry(config)

    orch = LiveOrchestrator(
        clock=_FakeClock(),
        bulkhead=bulkhead,
    )

    # First lease acquires the only slot.
    lease = orch.lease(
        tenant_id="tenant-a",
        provider_id="chatgpt_web",
        identity_ref="acct-1",
        job_id="job-1",
    )
    assert lease.tab is not None

    # Release via record_outcome.
    orch.record_outcome(lease, success=True)

    # Now the slot is free — second lease should succeed.
    lease2 = orch.lease(
        tenant_id="tenant-a",
        provider_id="chatgpt_web",
        identity_ref="acct-1",
        job_id="job-2",
    )
    assert lease2.tab is not None
    assert lease2.assigned


# ---------------------------------------------------------------------------
# Test 2: Tab crash with in-flight job_id triggers requeue_callback
# ---------------------------------------------------------------------------


def test_tab_crash_requeues_in_flight_job():
    """record_outcome() with crash_level=TAB calls requeue_callback for the job on the tab."""
    requeued: list[str] = []

    orch = LiveOrchestrator(
        clock=_FakeClock(),
        requeue_callback=requeued.append,
    )

    # Acquire a lease so a real tab is created and assigned a job.
    lease = orch.lease(
        tenant_id="tenant-b",
        provider_id="perplexity_web",
        identity_ref="acct-2",
        job_id="job-xyz",
    )
    assert lease.tab is not None
    assert lease.tab.current_job_id == "job-xyz"

    # Simulate a tab crash.
    orch.record_outcome(lease, success=False, crash_level=CrashLevel.TAB)

    # The requeue_callback must have been called with the in-flight job id.
    assert "job-xyz" in requeued


def test_no_requeue_without_callback():
    """record_outcome() with crash_level set but no callback must not raise."""
    orch = LiveOrchestrator(
        clock=_FakeClock(),
        requeue_callback=None,  # explicitly no callback
    )

    lease = orch.lease(
        tenant_id="tenant-c",
        provider_id="chatgpt_web",
        identity_ref="acct-3",
        job_id="job-silent",
    )
    assert lease.tab is not None

    # Should complete without error even though there is no requeue_callback.
    orch.record_outcome(lease, success=False, crash_level=CrashLevel.TAB)


def test_no_requeue_without_crash_level():
    """requeue_callback must NOT be invoked when crash_level is omitted."""
    requeued: list[str] = []

    orch = LiveOrchestrator(
        clock=_FakeClock(),
        requeue_callback=requeued.append,
    )

    lease = orch.lease(
        tenant_id="tenant-d",
        provider_id="chatgpt_web",
        identity_ref="acct-4",
        job_id="job-normal",
    )

    orch.record_outcome(lease, success=True)

    assert requeued == []


def test_context_crash_requeues_all_tabs_on_context():
    """CrashLevel.CONTEXT requeues all tabs sharing the same context_id."""
    requeued: list[str] = []

    orch = LiveOrchestrator(
        clock=_FakeClock(),
        requeue_callback=requeued.append,
    )

    lease = orch.lease(
        tenant_id="tenant-e",
        provider_id="chatgpt_web",
        identity_ref="acct-5",
        job_id="job-ctx",
    )
    assert lease.tab is not None

    orch.record_outcome(lease, success=False, crash_level=CrashLevel.CONTEXT)

    # The tab's job_id should appear in requeued (context recovery covers its tabs).
    assert "job-ctx" in requeued


# ---------------------------------------------------------------------------
# Test 3: No-bulkhead path is unchanged (regression guard)
# ---------------------------------------------------------------------------


def test_no_bulkhead_plain_lease_still_works():
    """When bulkhead=None the orchestrator behaves exactly as before."""
    orch = LiveOrchestrator(clock=_FakeClock())

    lease = orch.lease(
        tenant_id="tenant-f",
        provider_id="chatgpt_web",
        identity_ref="acct-6",
        job_id="job-plain",
    )
    assert lease.tab is not None
    assert lease.assigned

    change = orch.record_outcome(lease, success=True)
    assert change is None  # default success window not reached yet

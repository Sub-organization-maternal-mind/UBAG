"""Tests for BulkheadRegistry admission control — Task 2.1."""

import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import pytest
from ubag_worker.orchestration.bulkhead import (
    BulkheadConfig,
    BulkheadRegistry,
    CrashLevel,
    RecoveryAction,
    compute_recovery,
)
from ubag_worker.orchestration.topology import ChannelTab


# ---------------------------------------------------------------------------
# Test 1: try_acquire succeeds under ceiling
# ---------------------------------------------------------------------------

def test_try_acquire_succeeds_under_ceiling():
    reg = BulkheadRegistry(BulkheadConfig(max_tabs_per_tenant=3, max_tabs_per_target=3))
    assert reg.try_acquire("tenant1", "target1") is True
    assert reg.try_acquire("tenant1", "target1") is True
    snap = reg.snapshot()
    assert snap["tenant"]["tenant1"] == 2
    assert snap["target"]["target1"] == 2


# ---------------------------------------------------------------------------
# Test 2: try_acquire rejects AT ceiling (per-tenant and per-target)
# ---------------------------------------------------------------------------

def test_try_acquire_rejects_at_tenant_ceiling():
    reg = BulkheadRegistry(BulkheadConfig(max_tabs_per_tenant=2, max_tabs_per_target=10))
    assert reg.try_acquire("tenant1", "target1") is True
    assert reg.try_acquire("tenant1", "target2") is True
    # ceiling reached for tenant1
    assert reg.try_acquire("tenant1", "target3") is False


def test_try_acquire_rejects_at_target_ceiling():
    reg = BulkheadRegistry(BulkheadConfig(max_tabs_per_tenant=10, max_tabs_per_target=2))
    assert reg.try_acquire("tenant1", "target1") is True
    assert reg.try_acquire("tenant2", "target1") is True
    # ceiling reached for target1
    assert reg.try_acquire("tenant3", "target1") is False


# ---------------------------------------------------------------------------
# Test 3: release frees a slot (acquire again after releasing)
# ---------------------------------------------------------------------------

def test_release_frees_slot():
    reg = BulkheadRegistry(BulkheadConfig(max_tabs_per_tenant=1, max_tabs_per_target=1))
    assert reg.try_acquire("tenant1", "target1") is True
    assert reg.try_acquire("tenant1", "target1") is False  # full

    reg.release("tenant1", "target1")
    assert reg.try_acquire("tenant1", "target1") is True   # slot freed


def test_release_is_no_op_when_counter_is_zero():
    reg = BulkheadRegistry()
    # Should not raise
    reg.release("ghost_tenant", "ghost_target")
    snap = reg.snapshot()
    assert snap["tenant"].get("ghost_tenant", 0) == 0
    assert snap["target"].get("ghost_target", 0) == 0


# ---------------------------------------------------------------------------
# Test 4: per-tenant and per-target ceilings are independent
# ---------------------------------------------------------------------------

def test_tenant_ceiling_does_not_affect_other_tenant():
    """tenant1 hitting its ceiling must not block tenant2."""
    reg = BulkheadRegistry(BulkheadConfig(max_tabs_per_tenant=1, max_tabs_per_target=10))
    assert reg.try_acquire("tenant1", "target1") is True
    assert reg.try_acquire("tenant1", "target2") is False  # tenant1 full

    # tenant2 is a separate counter — should succeed
    assert reg.try_acquire("tenant2", "target1") is True


def test_target_ceiling_does_not_affect_other_target():
    """target1 hitting its ceiling must not block target2."""
    reg = BulkheadRegistry(BulkheadConfig(max_tabs_per_tenant=10, max_tabs_per_target=1))
    assert reg.try_acquire("tenant1", "target1") is True
    assert reg.try_acquire("tenant2", "target1") is False  # target1 full

    # target2 is a separate counter — should succeed
    assert reg.try_acquire("tenant1", "target2") is True


# ---------------------------------------------------------------------------
# Test 5: Zero ceiling = unlimited (disabled dimension)
# ---------------------------------------------------------------------------

def test_zero_tenant_ceiling_is_unlimited():
    reg = BulkheadRegistry(BulkheadConfig(max_tabs_per_tenant=0, max_tabs_per_target=2))
    # Tenant ceiling disabled — only target ceiling applies
    for _ in range(50):
        reg.try_acquire("tenant1", "ignored_target")  # target ceiling kicks in
    # With target ceiling=2, after 2 acquires on "ignored_target" it's full;
    # but tenant1 counter itself is not the blocker.
    reg2 = BulkheadRegistry(BulkheadConfig(max_tabs_per_tenant=0, max_tabs_per_target=0))
    for _ in range(100):
        assert reg2.try_acquire("t", "tgt") is True


def test_zero_target_ceiling_is_unlimited():
    reg = BulkheadRegistry(BulkheadConfig(max_tabs_per_tenant=2, max_tabs_per_target=0))
    # Only tenant ceiling applies
    assert reg.try_acquire("tenant1", "target1") is True
    assert reg.try_acquire("tenant1", "target2") is True
    assert reg.try_acquire("tenant1", "target3") is False  # tenant ceiling hit

    # target counter has no ceiling, different tenant should still succeed
    assert reg.try_acquire("tenant2", "target1") is True


def test_both_zero_ceilings_always_succeeds():
    reg = BulkheadRegistry(BulkheadConfig(max_tabs_per_tenant=0, max_tabs_per_target=0))
    for i in range(200):
        assert reg.try_acquire(f"tenant{i % 5}", f"target{i % 3}") is True


# ---------------------------------------------------------------------------
# Test 6: try_acquire is atomic — tenant OK but target full → no counters modified
# ---------------------------------------------------------------------------

def test_atomicity_no_partial_increment():
    """If tenant has room but target is full, tenant counter must NOT be incremented."""
    reg = BulkheadRegistry(BulkheadConfig(max_tabs_per_tenant=5, max_tabs_per_target=1))
    # Fill target1
    assert reg.try_acquire("tenant1", "target1") is True

    snap_before = reg.snapshot()
    tenant1_before = snap_before["tenant"].get("tenant1", 0)

    # tenant1 has room (1 < 5) but target1 is full (1 == 1)
    result = reg.try_acquire("tenant1", "target1")
    assert result is False

    snap_after = reg.snapshot()
    # tenant counter must be unchanged
    assert snap_after["tenant"].get("tenant1", 0) == tenant1_before


def test_atomicity_target_ok_tenant_full_no_target_increment():
    """If target has room but tenant is full, target counter must NOT be incremented."""
    reg = BulkheadRegistry(BulkheadConfig(max_tabs_per_tenant=1, max_tabs_per_target=5))
    # Fill tenant1
    assert reg.try_acquire("tenant1", "target1") is True

    snap_before = reg.snapshot()
    target1_before = snap_before["target"].get("target1", 0)

    # target1 has room (1 < 5) but tenant1 is full (1 == 1)
    result = reg.try_acquire("tenant1", "target1")
    assert result is False

    snap_after = reg.snapshot()
    # target counter must be unchanged
    assert snap_after["target"].get("target1", 0) == target1_before


# ---------------------------------------------------------------------------
# Test 7: BulkheadRegistry + compute_recovery smoke test
# ---------------------------------------------------------------------------

def test_smoke_tab_crash_requeue_with_bulkhead():
    """Smoke test: a TAB-level crash produces a requeue job, and the bulkhead
    correctly tracks and releases slots."""
    crashed_tab = ChannelTab(
        tab_id="tab-001",
        context_id="ctx-001",
        current_job_id="job-abc",
    )

    plan = compute_recovery(CrashLevel.TAB, tab=crashed_tab)
    assert RecoveryAction.REQUEUE_JOB in plan.actions
    assert "job-abc" in plan.requeue_job_ids

    # Registry: acquire before job, release on tab crash
    reg = BulkheadRegistry(BulkheadConfig(max_tabs_per_tenant=3, max_tabs_per_target=3))
    assert reg.try_acquire("tenant-x", "target-y") is True

    snap = reg.snapshot()
    assert snap["tenant"]["tenant-x"] == 1
    assert snap["target"]["target-y"] == 1

    reg.release("tenant-x", "target-y")
    snap2 = reg.snapshot()
    assert snap2["tenant"]["tenant-x"] == 0
    assert snap2["target"]["target-y"] == 0


# ---------------------------------------------------------------------------
# Test 8: Thread-safety — N threads racing to fill the last slot
# ---------------------------------------------------------------------------

def test_thread_safety_boundary():
    """N threads racing to fill the last slot — exactly ceiling total successes."""
    import concurrent.futures
    config = BulkheadConfig(max_tabs_per_tenant=0, max_tabs_per_target=5)
    registry = BulkheadRegistry(config)
    results = []

    def try_acquire_once():
        return registry.try_acquire("tenant", "target")

    with concurrent.futures.ThreadPoolExecutor(max_workers=20) as executor:
        futures = [executor.submit(try_acquire_once) for _ in range(20)]
        results = [f.result() for f in futures]

    # Exactly 5 acquires should succeed (the target ceiling)
    assert sum(results) == 5
    # Snapshot should show exactly 5 in target counter
    snap = registry.snapshot()
    assert snap["target"]["target"] == 5

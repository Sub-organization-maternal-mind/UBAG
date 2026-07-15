"""Tests for cross-pool scheduling/fairness (§12.13) and failure bulkheads
(§12.10, §12.12).

Bulkhead coverage lives here because the file-ownership scope for this task does
not include a dedicated ``test_orchestration_bulkhead.py``; scheduling and
failure-isolation are the two cross-pool coordination concerns.

Scheduler coverage: lane weighting, per-provider concurrency tokens (no
starvation across providers), anti-starvation aging, and sticky multi-turn.
Bulkhead coverage: blast radius per crash level, and single-flight re-login
(N tabs => 1 login).
"""

import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.orchestration import (  # noqa: E402
    BrowserInstance,
    ChannelTab,
    CrashLevel,
    Fleet,
    Lane,
    ProviderContext,
    RecoveryAction,
    ScheduledJob,
    SingleFlightRelogin,
    WeightedScheduler,
    compute_recovery,
)


class _FakeClock:
    def __init__(self, value=0.0):
        self.value = value

    def __call__(self):
        return self.value

    def advance(self, seconds):
        self.value += seconds


class SchedulerTests(unittest.TestCase):
    def test_higher_lane_wins(self):
        clock = _FakeClock()
        sched = WeightedScheduler(default_limit=5, clock=clock)
        sched.submit(ScheduledJob(job_id="low", provider="p1", lane=Lane.LOW))
        sched.submit(ScheduledJob(job_id="crit", provider="p2", lane=Lane.CRITICAL))
        self.assertEqual(sched.pick_next().job_id, "crit")
        self.assertEqual(sched.pick_next().job_id, "low")

    def test_bulk_provider_cannot_starve_critical_on_another(self):
        clock = _FakeClock()
        # p_bulk limited to 1 concurrent; p_crit can run.
        sched = WeightedScheduler(
            provider_limits={"p_bulk": 1, "p_crit": 1}, clock=clock
        )
        sched.submit(ScheduledJob(job_id="b1", provider="p_bulk", lane=Lane.BULK))
        sched.submit(ScheduledJob(job_id="b2", provider="p_bulk", lane=Lane.BULK))
        sched.submit(ScheduledJob(job_id="c1", provider="p_crit", lane=Lane.CRITICAL))
        # First pick: bulk token taken by b1 (critical not yet submitted order
        # independent — verify critical still dispatchable next while p_bulk busy).
        first = sched.pick_next()
        self.assertEqual(first.job_id, "c1")  # critical wins by lane
        second = sched.pick_next()
        self.assertEqual(second.job_id, "b1")  # one bulk token
        # p_bulk now saturated; b2 cannot dispatch, but it does not block others.
        self.assertIsNone(sched.pick_next())

    def test_per_provider_token_limit(self):
        clock = _FakeClock()
        sched = WeightedScheduler(provider_limits={"p1": 1}, clock=clock)
        sched.submit(ScheduledJob(job_id="a", provider="p1"))
        sched.submit(ScheduledJob(job_id="b", provider="p1"))
        first = sched.pick_next()
        self.assertEqual(first.job_id, "a")
        self.assertIsNone(sched.pick_next())  # token exhausted
        sched.complete(first)
        self.assertEqual(sched.pick_next().job_id, "b")

    def test_anti_starvation_age_boost(self):
        clock = _FakeClock()
        sched = WeightedScheduler(default_limit=5, aging_interval=1.0, clock=clock)
        sched.submit(ScheduledJob(job_id="old_low", provider="p1", lane=Lane.LOW), now=0.0)
        # A much later high-priority job.
        clock.advance(10.0)
        sched.submit(ScheduledJob(job_id="new_high", provider="p2", lane=Lane.HIGH), now=10.0)
        # old_low has aged 10s/1.0 = 10 boost -> eff = 3-10 = -7; new_high eff = 1.
        self.assertEqual(sched.pick_next(now=10.0).job_id, "old_low")

    def test_sticky_busy_conversation_held_back(self):
        clock = _FakeClock()
        busy = {"c1"}
        sched = WeightedScheduler(
            default_limit=5,
            clock=clock,
            is_conversation_busy=lambda cid: cid in busy,
        )
        sched.submit(
            ScheduledJob(job_id="follow", provider="p1", lane=Lane.CRITICAL, conversation_id="c1")
        )
        sched.submit(ScheduledJob(job_id="other", provider="p1", lane=Lane.NORMAL))
        # Sticky follow-up parked because its conversation is busy.
        self.assertEqual(sched.pick_next().job_id, "other")
        busy.clear()
        self.assertEqual(sched.pick_next().job_id, "follow")

    def test_same_conversation_key_runs_fifo(self):
        # Two jobs on one conversation must never interleave (typing into the
        # same chat concurrently would corrupt both) and must run in submission
        # order — strict FIFO beats lane priority. j1 is submitted first at the
        # lowest lane; j2 second at the highest. j1 must still run first.
        clock = _FakeClock()
        sched = WeightedScheduler(default_limit=5, clock=clock)
        sched.submit(
            ScheduledJob(job_id="j1", provider="p1", lane=Lane.BULK, conversation_id="conv")
        )
        sched.submit(
            ScheduledJob(job_id="j2", provider="p1", lane=Lane.CRITICAL, conversation_id="conv")
        )
        first = sched.pick_next()
        self.assertEqual(first.job_id, "j1")  # FIFO head wins over j2's higher lane
        # j2 cannot dispatch while its conversation is in flight, even though a
        # provider token is free (default_limit=5).
        self.assertIsNone(sched.pick_next())
        sched.complete(first)
        self.assertEqual(sched.pick_next().job_id, "j2")

    def test_distinct_conversation_keys_run_in_parallel(self):
        # Serialization is per-conversation, not global: two different
        # conversations on the same provider dispatch concurrently under the
        # provider token cap, so throughput does not collapse to one-at-a-time.
        clock = _FakeClock()
        sched = WeightedScheduler(default_limit=5, clock=clock)
        sched.submit(ScheduledJob(job_id="a", provider="p1", conversation_id="convA"))
        sched.submit(ScheduledJob(job_id="b", provider="p1", conversation_id="convB"))
        first = sched.pick_next()
        second = sched.pick_next()
        self.assertIsNotNone(first)
        self.assertIsNotNone(second)
        self.assertEqual({first.job_id, second.job_id}, {"a", "b"})

    def test_jobs_without_conversation_key_are_unaffected(self):
        # No conversation_id anywhere ⇒ today's scheduling exactly: pure lane
        # order, both jobs dispatchable in parallel, no serialization applied.
        clock = _FakeClock()
        sched = WeightedScheduler(default_limit=5, clock=clock)
        sched.submit(ScheduledJob(job_id="low", provider="p1", lane=Lane.LOW))
        sched.submit(ScheduledJob(job_id="crit", provider="p1", lane=Lane.CRITICAL))
        self.assertEqual(sched.pick_next().job_id, "crit")
        self.assertEqual(sched.pick_next().job_id, "low")


class BulkheadTests(unittest.TestCase):
    def _topology(self):
        ctx = ProviderContext(
            context_id="ctx_1",
            instance_id="br_1",
            tenant_id="t1",
            target_id="chatgpt_web",
            identity_ref="acct_a",
        )
        tabs = [
            ChannelTab(tab_id="tab_1", context_id="ctx_1", current_job_id="j1"),
            ChannelTab(tab_id="tab_2", context_id="ctx_1", current_job_id="j2"),
        ]
        return ctx, tabs

    def test_tab_blast_radius(self):
        _, tabs = self._topology()
        plan = compute_recovery(CrashLevel.TAB, tab=tabs[0], tabs=tabs)
        self.assertEqual(plan.affected_tab_ids, ["tab_1"])
        self.assertEqual(plan.requeue_job_ids, ["j1"])
        self.assertIn(RecoveryAction.OPEN_REPLACEMENT_TAB, plan.actions)

    def test_context_blast_radius(self):
        ctx, tabs = self._topology()
        plan = compute_recovery(CrashLevel.CONTEXT, context=ctx, tabs=tabs)
        self.assertEqual(plan.affected_context_ids, ["ctx_1"])
        self.assertEqual(sorted(plan.affected_tab_ids), ["tab_1", "tab_2"])
        self.assertEqual(sorted(plan.requeue_job_ids), ["j1", "j2"])
        self.assertIn(RecoveryAction.RESTORE_SNAPSHOT, plan.actions)
        self.assertIn(RecoveryAction.REWARM_MIN_TABS, plan.actions)

    def test_browser_blast_radius(self):
        ctx, tabs = self._topology()
        browser = BrowserInstance(instance_id="br_1", worker_id="w1", tenant_id="t1")
        plan = compute_recovery(
            CrashLevel.BROWSER, browser=browser, contexts=[ctx], tabs=tabs
        )
        self.assertEqual(plan.affected_browser_ids, ["br_1"])
        self.assertEqual(sorted(plan.affected_tab_ids), ["tab_1", "tab_2"])
        self.assertIn(RecoveryAction.RESPAWN_BROWSER, plan.actions)

    def test_worker_blast_radius(self):
        fleet = Fleet()
        browser = fleet.create_browser(worker_id="w1", tenant_id="t1")
        ctx = ProviderContext(
            context_id="ctx_1",
            instance_id=browser.instance_id,
            tenant_id="t1",
            target_id="chatgpt_web",
            identity_ref="acct_a",
        )
        tabs = [ChannelTab(tab_id="tab_1", context_id="ctx_1", current_job_id="j1")]
        plan = compute_recovery(
            CrashLevel.WORKER,
            fleet=fleet,
            worker_id="w1",
            contexts=[ctx],
            tabs=tabs,
        )
        self.assertEqual(plan.affected_browser_ids, [browser.instance_id])
        self.assertEqual(plan.requeue_job_ids, ["j1"])
        self.assertIn(RecoveryAction.OUTBOX_REQUEUE, plan.actions)
        self.assertIn(RecoveryAction.REASSIGN_WORKER, plan.actions)

    def test_single_flight_relogin_n_tabs_one_login(self):
        mutex = SingleFlightRelogin()
        leader = mutex.request("tab_1")
        self.assertTrue(leader.is_leader)
        for sibling in ("tab_2", "tab_3", "tab_4"):
            self.assertFalse(mutex.request(sibling).is_leader)
        self.assertEqual(mutex.parked, ["tab_2", "tab_3", "tab_4"])
        resumed = mutex.complete("tab_1")
        self.assertEqual(mutex.login_count, 1)
        self.assertEqual(resumed, ["tab_2", "tab_3", "tab_4"])
        self.assertFalse(mutex.in_progress)

    def test_only_leader_can_complete(self):
        mutex = SingleFlightRelogin()
        mutex.request("tab_1")
        mutex.request("tab_2")
        with self.assertRaises(RuntimeError):
            mutex.complete("tab_2")


if __name__ == "__main__":
    unittest.main()

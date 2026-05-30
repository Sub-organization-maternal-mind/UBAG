"""Tests for the ChannelPool assignment algorithm (§12.8).

Covers all five assignment branches (sticky-ready, sticky-enqueue, reuse,
auto-open new tab, bounded-queue backpressure), spa-singleton context fan-out,
work-stealing, scale-down, and recycle.
"""

import itertools
import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.orchestration import (  # noqa: E402
    AIMDController,
    AssignOutcome,
    ChannelPool,
    ConversationModel,
    Job,
    PoolConfig,
    ProviderContext,
    SubmitPacer,
    TabState,
)
from ubag_worker.orchestration.topology import Lane  # noqa: E402


class _FakeClock:
    def __init__(self, value=0.0):
        self.value = value

    def __call__(self):
        return self.value

    def advance(self, seconds):
        self.value += seconds


def _context(model=ConversationModel.URL, cid="ctx_0001"):
    return ProviderContext(
        context_id=cid,
        instance_id="br_0001",
        tenant_id="t1",
        target_id="chatgpt_web",
        identity_ref="acct_a",
        conversation_model=model,
    )


def _pool(clock, *, config=None, model=ConversationModel.URL, aimd=None, pacer=None,
          context_factory=None):
    return ChannelPool(
        tenant_id="t1",
        provider_id="chatgpt_web",
        identity_ref="acct_a",
        context=_context(model),
        config=config if config is not None else PoolConfig(min_tabs=0),
        aimd=aimd if aimd is not None else AIMDController(start=2, clock=clock),
        pacer=pacer if pacer is not None else SubmitPacer(base_gap=0.0, jitter=0.0, clock=clock),
        clock=clock,
        conversation_model=model,
        context_factory=context_factory,
    )


class ChannelPoolTests(unittest.TestCase):
    def test_first_submit_opens_new_tab(self):
        clock = _FakeClock()
        pool = _pool(clock)
        res = pool.submit(Job(job_id="j1"))
        self.assertEqual(res.outcome, AssignOutcome.ASSIGNED_NEW_TAB)
        self.assertEqual(len(pool.tabs), 1)
        self.assertEqual(res.tab.state, TabState.BUSY)

    def test_reuse_ready_idle_tab(self):
        clock = _FakeClock()
        pool = _pool(clock)
        first = pool.submit(Job(job_id="j1"))
        pool.complete(first.tab)
        res = pool.submit(Job(job_id="j2"))
        self.assertEqual(res.outcome, AssignOutcome.ASSIGNED_REUSE)
        self.assertEqual(len(pool.tabs), 1)
        self.assertIs(res.tab, first.tab)

    def test_sticky_ready_assigns_same_tab(self):
        clock = _FakeClock()
        pool = _pool(clock)
        first = pool.submit(Job(job_id="j1", conversation_id="c1"))
        pool.complete(first.tab)
        res = pool.submit(Job(job_id="j2", conversation_id="c1"))
        self.assertEqual(res.outcome, AssignOutcome.ASSIGNED_STICKY)
        self.assertIs(res.tab, first.tab)

    def test_sticky_busy_enqueues_behind_owner(self):
        clock = _FakeClock()
        pool = _pool(clock)
        first = pool.submit(Job(job_id="j1", conversation_id="c1"))
        res = pool.submit(Job(job_id="j2", conversation_id="c1"))
        self.assertEqual(res.outcome, AssignOutcome.ENQUEUED_STICKY)
        # Completing the owner resumes the sticky follow-up on the SAME tab.
        dispatched = pool.complete(first.tab)
        self.assertIsNotNone(dispatched)
        self.assertEqual(dispatched.outcome, AssignOutcome.ASSIGNED_STICKY)
        self.assertEqual(dispatched.job_id, "j2")
        self.assertIs(dispatched.tab, first.tab)

    def test_backpressure_enqueues_when_at_effective_max(self):
        clock = _FakeClock()
        pool = _pool(clock, aimd=AIMDController(start=1, clock=clock))
        pool.submit(Job(job_id="j1"))  # opens the only tab (cap == 1)
        res = pool.submit(Job(job_id="j2"))
        self.assertEqual(res.outcome, AssignOutcome.ENQUEUED)
        self.assertEqual(pool.queue_depth, 1)

    def test_queue_full_returns_signal(self):
        clock = _FakeClock()
        pool = _pool(
            clock,
            config=PoolConfig(min_tabs=0, queue_maxsize=1),
            aimd=AIMDController(start=1, clock=clock),
        )
        pool.submit(Job(job_id="j1"))  # tab busy
        self.assertEqual(pool.submit(Job(job_id="j2")).outcome, AssignOutcome.ENQUEUED)
        res = pool.submit(Job(job_id="j3"))
        self.assertEqual(res.outcome, AssignOutcome.QUEUE_FULL)

    def test_pacer_gate_forces_enqueue(self):
        clock = _FakeClock()
        # Large gap means the second submit cannot open a new tab yet.
        pacer = SubmitPacer(base_gap=5.0, jitter=0.0, clock=clock)
        pool = _pool(clock, pacer=pacer)
        pool.submit(Job(job_id="j1"))  # opens tab, consumes a token
        res = pool.submit(Job(job_id="j2"))
        self.assertEqual(res.outcome, AssignOutcome.ENQUEUED)

    def test_work_stealing_dispatches_from_queue(self):
        clock = _FakeClock()
        pool = _pool(clock, aimd=AIMDController(start=1, clock=clock))
        first = pool.submit(Job(job_id="j1"))
        pool.submit(Job(job_id="j2"))  # enqueued
        dispatched = pool.complete(first.tab)
        self.assertIsNotNone(dispatched)
        self.assertEqual(dispatched.job_id, "j2")

    def test_priority_lane_orders_queue(self):
        clock = _FakeClock()
        pool = _pool(clock, aimd=AIMDController(start=1, clock=clock))
        first = pool.submit(Job(job_id="j1"))
        pool.submit(Job(job_id="low", lane=Lane.LOW))
        pool.submit(Job(job_id="crit", lane=Lane.CRITICAL))
        dispatched = pool.complete(first.tab)
        self.assertEqual(dispatched.job_id, "crit")

    def test_spa_singleton_fans_out_into_contexts(self):
        clock = _FakeClock()
        counter = itertools.count(1)

        def factory():
            return _context(ConversationModel.SPA_SINGLETON, cid="ctx_%04d" % next(counter))

        pool = _pool(
            clock,
            model=ConversationModel.SPA_SINGLETON,
            context_factory=factory,
        )
        r1 = pool.submit(Job(job_id="j1"))
        r2 = pool.submit(Job(job_id="j2"))
        self.assertEqual(r1.outcome, AssignOutcome.ASSIGNED_NEW_TAB)
        self.assertEqual(r2.outcome, AssignOutcome.ASSIGNED_NEW_TAB)
        # Each channel got its own context (no shared-context multi-tab).
        self.assertNotEqual(r1.tab.context_id, r2.tab.context_id)
        self.assertGreaterEqual(len(pool.contexts), 3)  # base + 2 fan-out

    def test_scale_down_closes_idle_to_min_tabs(self):
        clock = _FakeClock()
        pool = _pool(clock, config=PoolConfig(min_tabs=1, idle_ttl=10.0))
        a = pool.submit(Job(job_id="j1"))
        b = pool.submit(Job(job_id="j2"))
        pool.complete(a.tab)
        pool.complete(b.tab)
        self.assertEqual(len(pool.tabs), 2)
        clock.advance(11.0)
        closed = pool.reap_idle()
        self.assertEqual(len(closed), 1)
        self.assertEqual(len(pool.tabs), 1)

    def test_recycle_replaces_tab_after_job_budget(self):
        clock = _FakeClock()
        pool = _pool(clock, config=PoolConfig(min_tabs=0, recycle_after_jobs=1))
        first = pool.submit(Job(job_id="j1"))
        old_id = first.tab.tab_id
        pool.complete(first.tab)
        self.assertEqual(first.tab.state, TabState.CLOSED)
        self.assertEqual(len(pool.tabs), 1)
        self.assertNotIn(old_id, [t.tab_id for t in pool.tabs])

    def test_effective_max_is_min_of_constraints(self):
        clock = _FakeClock()
        pool = _pool(
            clock,
            config=PoolConfig(min_tabs=0, max_tabs=6, memory_budget_tabs=3),
            aimd=AIMDController(start=5, clock=clock),
        )
        self.assertEqual(pool.effective_max(), 3)


if __name__ == "__main__":
    unittest.main()

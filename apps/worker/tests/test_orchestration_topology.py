"""Tests for the orchestration topology (browser → context → tab).

Covers tenant isolation (INV-5), lazy context create + reuse (INV-2/INV-3), and
per-browser context-ceiling spillover (§12.7).
"""

import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.orchestration import (  # noqa: E402
    ConversationModel,
    Fleet,
    TenantIsolationError,
)
from ubag_worker.orchestration.topology import CeilingExceededError  # noqa: E402


class TopologyTests(unittest.TestCase):
    def test_same_provider_identity_reuses_context_inv2(self):
        fleet = Fleet()
        a = fleet.get_or_create_context(
            tenant_id="t1", target_id="chatgpt_web", identity_ref="acct_a"
        )
        b = fleet.get_or_create_context(
            tenant_id="t1", target_id="chatgpt_web", identity_ref="acct_a"
        )
        self.assertIs(a, b)
        self.assertEqual(len(fleet.contexts), 1)
        self.assertEqual(len(fleet.browsers), 1)

    def test_different_provider_new_context_same_browser_inv3(self):
        fleet = Fleet()
        a = fleet.get_or_create_context(
            tenant_id="t1", target_id="chatgpt_web", identity_ref="acct_a"
        )
        b = fleet.get_or_create_context(
            tenant_id="t1", target_id="claude_web", identity_ref="acct_a"
        )
        self.assertNotEqual(a.context_id, b.context_id)
        self.assertEqual(a.instance_id, b.instance_id)
        self.assertEqual(len(fleet.browsers), 1)

    def test_different_identity_is_a_new_context(self):
        fleet = Fleet()
        a = fleet.get_or_create_context(
            tenant_id="t1", target_id="deepseek_web", identity_ref="acct_a"
        )
        b = fleet.get_or_create_context(
            tenant_id="t1", target_id="deepseek_web", identity_ref="acct_b"
        )
        self.assertNotEqual(a.context_id, b.context_id)

    def test_tenant_isolation_never_shares_browser_inv5(self):
        fleet = Fleet()
        a = fleet.get_or_create_context(
            tenant_id="t1", target_id="chatgpt_web", identity_ref="acct_a"
        )
        b = fleet.get_or_create_context(
            tenant_id="t2", target_id="chatgpt_web", identity_ref="acct_a"
        )
        self.assertNotEqual(a.instance_id, b.instance_id)
        self.assertEqual(len(fleet.browsers), 2)

    def test_place_context_on_wrong_tenant_browser_rejected(self):
        fleet = Fleet()
        ctx = fleet.get_or_create_context(
            tenant_id="t1", target_id="chatgpt_web", identity_ref="acct_a"
        )
        other = fleet.create_browser(worker_id="w1", tenant_id="t2")
        with self.assertRaises(TenantIsolationError):
            fleet.place_context_on_browser(ctx, other)

    def test_context_ceiling_spillover_to_second_browser(self):
        fleet = Fleet(context_ceiling=3)
        for i in range(4):
            fleet.get_or_create_context(
                tenant_id="t1", target_id="provider_%d" % i, identity_ref="acct_a"
            )
        self.assertEqual(len(fleet.contexts), 4)
        self.assertEqual(len(fleet.browsers), 2)
        counts = sorted(b.context_count for b in fleet.browsers)
        self.assertEqual(counts, [1, 3])

    def test_place_context_at_ceiling_rejected(self):
        fleet = Fleet(context_ceiling=1)
        first = fleet.create_browser(worker_id="w1", tenant_id="t1")
        ctx = fleet.get_or_create_context(
            tenant_id="t1", target_id="chatgpt_web", identity_ref="acct_a"
        )
        # 'first' already holds the lazily created context (ceiling 1 reached).
        self.assertEqual(first.context_count, 1)
        extra = ctx  # reuse same tenant context object
        with self.assertRaises(CeilingExceededError):
            fleet.place_context_on_browser(extra, first)

    def test_tab_ceiling_enforced(self):
        from ubag_worker.orchestration import ChannelTab

        fleet = Fleet(tab_ceiling=2)
        ctx = fleet.get_or_create_context(
            tenant_id="t1", target_id="chatgpt_web", identity_ref="acct_a"
        )
        for i in range(2):
            fleet.register_tab(ChannelTab(tab_id="tab_%d" % i, context_id=ctx.context_id))
        with self.assertRaises(CeilingExceededError):
            fleet.register_tab(ChannelTab(tab_id="tab_x", context_id=ctx.context_id))

    def test_conversation_model_carried_on_context(self):
        fleet = Fleet()
        ctx = fleet.get_or_create_context(
            tenant_id="t1",
            target_id="gemini_web",
            identity_ref="acct_a",
            conversation_model=ConversationModel.SPA_SINGLETON,
        )
        self.assertEqual(ctx.conversation_model, ConversationModel.SPA_SINGLETON)


if __name__ == "__main__":
    unittest.main()

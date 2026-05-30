"""Tests for the live runtime ↔ orchestration integration (§12.6–§12.9, §13.10–§13.11).

These tests are fully deterministic and dependency-free: no browser, no network,
no Playwright. They cover three integration seams added to the live worker:

* :meth:`PlaywrightPageDriver._resolve_launch_plan` — pure, offline engine
  selection (local/remote, browser kind, headed/headless).
* :class:`LiveOrchestrator` — Fleet + ChannelPool + AIMD coordination
  (lease / record_outcome / concurrency_state / topology_snapshot).
* :class:`LiveSessionEngine` wired with an orchestrator — the canonical event
  stream is preserved and augmented with ``browser.topology_reported`` and,
  on adverse signals, ``concurrency.cap_changed``. The ``orchestrator=None``
  default path stays byte-identical to the legacy stream.
"""

import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.live import (  # noqa: E402
    LiveOrchestrator,
    LiveSessionEngine,
    MockPageDriver,
)
from ubag_worker.live.engines import EngineKind, EngineSpec  # noqa: E402
from ubag_worker.live.page_driver import PlaywrightPageDriver  # noqa: E402
from ubag_worker.live.selectors import get_provider_selectors  # noqa: E402
from ubag_worker.orchestration import NegativeSignal, PoolConfig  # noqa: E402
from ubag_worker.orchestration.topology import Fleet, TabState  # noqa: E402


class _FakeClock:
    def __init__(self, value=0.0):
        self.value = value

    def __call__(self):
        return self.value

    def advance(self, seconds):
        self.value += seconds


_MANUAL_CONTEXT = {
    "account_binding_id": "acct_live_123",
    "consent_ref": "consent_live_123",
    "automation_scope": ["manual_login", "submit_prompt", "read_response"],
}


def _payload(target, prompt="Reply with the word ready.", **context):
    job_context = dict(_MANUAL_CONTEXT)
    job_context.update(context)
    return {
        "api_version": "2026-05-22",
        "job_id": "job_live_%s" % target,
        "trace_id": "trace_live_%s" % target,
        "job": {
            "target": target,
            "command_type": "chat.prompt",
            "input": {"prompt": prompt},
            "context": job_context,
        },
    }


def _types(events):
    return [event["type"] for event in events]


# ---------------------------------------------------------------------------
# _resolve_launch_plan — pure engine selection
# ---------------------------------------------------------------------------


class LaunchPlanResolutionTests(unittest.TestCase):
    def test_default_spec_is_local_chromium_and_honors_headless(self):
        plan = PlaywrightPageDriver._resolve_launch_plan(None, headless=True)
        self.assertEqual(plan.browser_type_name, "chromium")
        self.assertIsNone(plan.remote_endpoint)
        self.assertTrue(plan.headless)

    def test_default_spec_passes_through_headed_request(self):
        plan = PlaywrightPageDriver._resolve_launch_plan(None, headless=False)
        self.assertFalse(plan.headless)

    def test_firefox_kind_selects_firefox_browser_type(self):
        spec = EngineSpec(kind=EngineKind.FIREFOX)
        plan = PlaywrightPageDriver._resolve_launch_plan(spec, headless=True)
        self.assertEqual(plan.browser_type_name, "firefox")

    def test_webkit_kind_selects_webkit_browser_type(self):
        spec = EngineSpec(kind=EngineKind.WEBKIT)
        plan = PlaywrightPageDriver._resolve_launch_plan(spec, headless=True)
        self.assertEqual(plan.browser_type_name, "webkit")

    def test_bidi_kind_launches_locally_via_chromium(self):
        # "bidi" is vendor-neutral; locally it maps onto the chromium browser
        # type (remote BiDi is reached through the remote endpoint instead).
        spec = EngineSpec(kind=EngineKind.BIDI)
        plan = PlaywrightPageDriver._resolve_launch_plan(spec, headless=True)
        self.assertEqual(plan.browser_type_name, "chromium")

    def test_remote_endpoint_is_surfaced(self):
        spec = EngineSpec(
            kind=EngineKind.CHROMIUM,
            remote_endpoint="ws://grid.internal:4444/cdp",
        )
        plan = PlaywrightPageDriver._resolve_launch_plan(spec, headless=True)
        self.assertEqual(plan.remote_endpoint, "ws://grid.internal:4444/cdp")

    def test_headed_spec_forces_headless_false(self):
        spec = EngineSpec(kind=EngineKind.CHROMIUM, headed=True)
        plan = PlaywrightPageDriver._resolve_launch_plan(spec, headless=True)
        self.assertFalse(plan.headless)


# ---------------------------------------------------------------------------
# LiveOrchestrator — Fleet + ChannelPool + AIMD coordination
# ---------------------------------------------------------------------------


class LiveOrchestratorTests(unittest.TestCase):
    def _orchestrator(self, clock=None, pool_config=None):
        clock = clock if clock is not None else _FakeClock()
        return LiveOrchestrator(clock=clock, pool_config=pool_config)

    def test_lease_assigns_a_tab_and_registers_topology(self):
        orch = self._orchestrator()
        lease = orch.lease(
            tenant_id="t1",
            provider_id="chatgpt_web",
            identity_ref="acct_a",
            job_id="j1",
        )
        self.assertTrue(lease.assigned)
        self.assertIsNotNone(lease.tab)
        self.assertEqual(lease.tab.state, TabState.BUSY)
        snapshot = orch.topology_snapshot("t1")
        self.assertEqual(len(snapshot["contexts"]), 1)
        self.assertEqual(len(snapshot["tabs"]), 1)
        self.assertEqual(snapshot["tabs"][0]["state"], TabState.BUSY.value)

    def test_record_outcome_success_releases_tab(self):
        orch = self._orchestrator()
        lease = orch.lease(
            tenant_id="t1", provider_id="chatgpt_web", identity_ref="acct_a", job_id="j1"
        )
        state = orch.concurrency_state(lease)
        self.assertEqual(state.in_flight, 1)
        change = orch.record_outcome(lease, success=True)
        # Default success window is 5, so a single success does not move the cap.
        self.assertIsNone(change)
        after = orch.concurrency_state(lease)
        self.assertEqual(after.in_flight, 0)

    def test_negative_signal_cuts_cap_and_emits_change(self):
        orch = self._orchestrator()
        lease = orch.lease(
            tenant_id="t1", provider_id="chatgpt_web", identity_ref="acct_a", job_id="j1"
        )
        change = orch.record_outcome(
            lease, success=False, signal=NegativeSignal.ERROR_RATE_SPIKE
        )
        self.assertIsNotNone(change)
        self.assertEqual(change.reason, NegativeSignal.ERROR_RATE_SPIKE.value)
        self.assertLess(change.new, change.old)

    def test_persistent_aimd_across_leases_for_same_key(self):
        orch = self._orchestrator()
        first = orch.lease(
            tenant_id="t1", provider_id="chatgpt_web", identity_ref="acct_a", job_id="j1"
        )
        orch.record_outcome(
            first, success=False, signal=NegativeSignal.ERROR_RATE_SPIKE
        )
        # A new lease for the same (tenant, provider, identity) reuses the pool's
        # AIMD controller, so the lowered ceiling persists.
        second = orch.lease(
            tenant_id="t1", provider_id="chatgpt_web", identity_ref="acct_a", job_id="j2"
        )
        self.assertEqual(second.pool, first.pool)
        self.assertLess(orch.concurrency_state(second).current_cap, 2)

    def test_tenant_isolation_in_topology_snapshot(self):
        orch = self._orchestrator()
        orch.lease(
            tenant_id="t1", provider_id="chatgpt_web", identity_ref="acct_a", job_id="j1"
        )
        orch.lease(
            tenant_id="t2", provider_id="chatgpt_web", identity_ref="acct_b", job_id="j2"
        )
        t1_snap = orch.topology_snapshot("t1")
        self.assertEqual(len(t1_snap["contexts"]), 1)
        self.assertEqual(t1_snap["contexts"][0]["identity_ref"], "acct_a")
        all_snap = orch.topology_snapshot()
        self.assertEqual(len(all_snap["contexts"]), 2)

    def test_snapshot_never_includes_storage_state(self):
        orch = self._orchestrator()
        orch.lease(
            tenant_id="t1", provider_id="chatgpt_web", identity_ref="acct_a", job_id="j1"
        )
        snapshot = orch.topology_snapshot("t1")
        for context in snapshot["contexts"]:
            self.assertIn("has_storage_state", context)
            self.assertFalse(context["has_storage_state"])

    def test_concurrency_state_reports_pool_maximum(self):
        orch = self._orchestrator(pool_config=PoolConfig(min_tabs=0, max_tabs=4))
        lease = orch.lease(
            tenant_id="t1", provider_id="chatgpt_web", identity_ref="acct_a", job_id="j1"
        )
        self.assertEqual(orch.concurrency_state(lease).maximum, 4)

    def test_injected_fleet_is_used(self):
        fleet = Fleet()
        orch = LiveOrchestrator(clock=_FakeClock(), fleet=fleet)
        self.assertIs(orch.fleet, fleet)
        orch.lease(
            tenant_id="t1", provider_id="chatgpt_web", identity_ref="acct_a", job_id="j1"
        )
        self.assertEqual(len(fleet.contexts), 1)


# ---------------------------------------------------------------------------
# LiveSessionEngine wired with an orchestrator
# ---------------------------------------------------------------------------


class EngineWithOrchestratorTests(unittest.TestCase):
    def _engine(self, provider="chatgpt_web", **orch_kwargs):
        orch = LiveOrchestrator(clock=_FakeClock(), **orch_kwargs)
        engine = LiveSessionEngine(get_provider_selectors(provider), orchestrator=orch)
        return engine, orch

    def test_happy_path_emits_topology_report_in_canonical_position(self):
        engine, _ = self._engine()
        events = engine.run(
            _payload("chatgpt_web", tenant_id="t1"),
            driver=MockPageDriver(authenticated=True, response_text="ready now"),
        )
        types = _types(events)
        self.assertEqual(
            types,
            [
                "queued",
                "session.opening",
                "session.authenticated",
                "running",
                "browser.topology_reported",
                "token",
                "token",
                "completed",
            ],
        )
        report = next(e for e in events if e["type"] == "browser.topology_reported")
        self.assertEqual(report["data"]["tenant_id"], "t1")
        self.assertEqual(len(report["data"]["contexts"]), 1)
        self.assertEqual(len(report["data"]["tabs"]), 1)
        # No credential/storage-state material in the telemetry payload.
        self.assertFalse(report["data"]["contexts"][0]["has_storage_state"])

    def test_sequence_numbers_stay_monotonic_with_extra_events(self):
        engine, _ = self._engine()
        events = engine.run(
            _payload("chatgpt_web", tenant_id="t1"),
            driver=MockPageDriver(authenticated=True),
        )
        for index, event in enumerate(events, start=1):
            self.assertEqual(event["sequence"], index)

    def test_drift_emits_concurrency_cap_changed_trailer(self):
        engine, _ = self._engine()
        events = engine.run(
            _payload("chatgpt_web", tenant_id="t1"),
            driver=MockPageDriver(authenticated=True, drift_group="prompt_input"),
        )
        types = _types(events)
        self.assertIn("blocked", types)
        self.assertEqual(types[-1], "concurrency.cap_changed")
        cap = events[-1]["data"]
        self.assertEqual(cap["target"], "chatgpt_web")
        self.assertEqual(cap["identity_ref"], "acct_live_123")
        self.assertEqual(cap["reason"], NegativeSignal.ERROR_RATE_SPIKE.value)
        self.assertLess(cap["current_cap"], 2)

    def test_manual_login_block_does_not_lease_or_emit_topology(self):
        engine, orch = self._engine(provider="perplexity_web")
        events = engine.run(
            _payload("perplexity_web", tenant_id="t1"),
            driver=MockPageDriver(authenticated=False, login_after_wait=False),
        )
        types = _types(events)
        self.assertNotIn("browser.topology_reported", types)
        self.assertNotIn("concurrency.cap_changed", types)
        self.assertEqual(types[-1], "blocked")
        # Never acquired a lease, so no Fleet context was created.
        self.assertEqual(len(orch.fleet.contexts), 0)

    def test_orchestrator_none_path_is_unchanged(self):
        plain = LiveSessionEngine(get_provider_selectors("chatgpt_web"))
        events = plain.run(
            _payload("chatgpt_web"),
            driver=MockPageDriver(authenticated=True, response_text="ready now"),
        )
        self.assertEqual(
            _types(events),
            [
                "queued",
                "session.opening",
                "session.authenticated",
                "running",
                "token",
                "token",
                "completed",
            ],
        )

    def test_lease_released_after_run_completes(self):
        engine, orch = self._engine()
        engine.run(
            _payload("chatgpt_web", tenant_id="t1"),
            driver=MockPageDriver(authenticated=True),
        )
        # After the run the tab is completed/released — no busy tabs remain.
        snapshot = orch.topology_snapshot("t1")
        busy = [t for t in snapshot["tabs"] if t["state"] == TabState.BUSY.value]
        self.assertEqual(busy, [])


if __name__ == "__main__":  # pragma: no cover
    unittest.main()

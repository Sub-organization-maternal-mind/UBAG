"""Tests for the concurrency telemetry contract (worker -> gateway)."""

import unittest

from ubag_worker.orchestration import (
    CONCURRENCY_CHANGE_EVENT_TYPE,
    AIMDController,
    NegativeSignal,
    concurrency_change_data,
)


class ConcurrencyTelemetryTest(unittest.TestCase):
    def test_event_type_matches_gateway_contract(self):
        self.assertEqual(CONCURRENCY_CHANGE_EVENT_TYPE, "concurrency.cap_changed")

    def test_additive_increase_payload(self):
        clock = {"t": 0.0}
        controller = AIMDController(start=2, floor=1, ceiling_max=8, success_window=1, clock=lambda: clock["t"])
        change = controller.record_success()
        self.assertIsNotNone(change)
        data = concurrency_change_data(
            target="mock",
            identity_ref="acct-1",
            change=change,
            minimum=controller.floor,
            maximum=8,
            in_flight=2,
        )
        self.assertEqual(
            data,
            {
                "target": "mock",
                "identity_ref": "acct-1",
                "current_cap": 3,
                "min": 1,
                "max": 8,
                "in_flight": 2,
                "reason": "additive_increase",
            },
        )

    def test_unbounded_max_reported_as_zero(self):
        controller = AIMDController(start=2, floor=1, success_window=1)
        change = controller.record_success()
        data = concurrency_change_data(
            target="mock",
            identity_ref="acct-1",
            change=change,
            minimum=1,
            maximum=None,
            in_flight=0,
        )
        self.assertEqual(data["max"], 0)

    def test_negative_signal_reason_propagates(self):
        controller = AIMDController(start=4, floor=1, decrease_factor=0.5)
        change = controller.record_signal(NegativeSignal.HTTP_429)
        data = concurrency_change_data(
            target="mock",
            identity_ref="acct-1",
            change=change,
            minimum=1,
            maximum=8,
            in_flight=1,
        )
        self.assertEqual(data["reason"], "http_429")
        self.assertEqual(data["current_cap"], 2)


if __name__ == "__main__":
    unittest.main()

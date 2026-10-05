"""Stage timings (P0.9b): opt-in data.timings_ms on the completed event."""

import json
import os
import sys
import time
import unittest
from pathlib import Path
from unittest import mock

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))
sys.path.insert(0, str(REPO_ROOT / "adapters" / "mock"))

from ubag_mock_adapter import build_mock_events  # noqa: E402
from ubag_worker.live import LiveSessionEngine, MockPageDriver  # noqa: E402
from ubag_worker.live.events import STAGE_KEYS, StageTimer  # noqa: E402
from ubag_worker.live.selectors import get_provider_selectors  # noqa: E402

_SCHEMA = REPO_ROOT / "packages" / "shared-schemas" / "schemas" / "job-event.schema.json"
_ENV = "UBAG_WORKER_STAGE_TIMINGS"


class _SlowDriver(MockPageDriver):
    """MockPageDriver with a measurable delay per stage."""

    def open(self, *a, **k):
        time.sleep(0.01)
        return super().open(*a, **k)

    def submit_prompt(self, *a, **k):
        time.sleep(0.01)
        return super().submit_prompt(*a, **k)

    def stream_response(self, *a, **k):
        time.sleep(0.01)  # submit -> first token
        for i, tok in enumerate(super().stream_response(*a, **k)):
            if i:
                time.sleep(0.01)
            yield tok

    def read_final_response(self, *a, **k):
        time.sleep(0.01)
        return super().read_final_response(*a, **k)


def _payload():
    return {
        "api_version": "v0",
        "job_id": "job_timings",
        "trace_id": "trace_timings",
        "job": {
            "target": "deepseek_web",
            "command_type": "chat.prompt",
            "input": {"prompt": "hi"},
            "context": {
                "account_binding_id": "acct_1",
                "consent_ref": "consent_1",
                "automation_scope": ["manual_login", "submit_prompt", "read_response"],
            },
        },
    }


def _env(value):
    env = {k: v for k, v in os.environ.items() if k != _ENV}
    if value is not None:
        env[_ENV] = value
    return mock.patch.dict(os.environ, env, clear=True)


def _completed(events):
    return [e for e in events if e["type"] == "completed"][0]


class StageTimingsTests(unittest.TestCase):
    def test_schema_key_set_matches_stage_keys(self):
        schema = json.loads(_SCHEMA.read_text(encoding="utf-8"))
        keys = schema["properties"]["data"]["properties"]["timings_ms"]["properties"]
        self.assertEqual(set(keys), set(STAGE_KEYS))

    def test_timer_accumulates_and_rejects_unknown_stage(self):
        timer = StageTimer()
        timer.add("browser_prep", 0.002)
        timer.add("browser_prep", 0.003)
        self.assertAlmostEqual(timer.as_dict()["browser_prep"], 5.0, places=3)
        with self.assertRaises(ValueError):
            timer.add("bogus", 1)

    def test_live_engine_off_by_default(self):
        with _env(None):
            events = LiveSessionEngine(get_provider_selectors("deepseek_web")).run(
                _payload(), driver=MockPageDriver(tokens=["a", "b"])
            )
        self.assertNotIn("timings_ms", _completed(events)["data"])

    def test_live_engine_emits_closed_key_nonnegative_timings(self):
        with _env("1"):
            events = LiveSessionEngine(get_provider_selectors("deepseek_web")).run(
                _payload(), driver=_SlowDriver(tokens=["a", "b", "c"])
            )
        timings = _completed(events)["data"]["timings_ms"]
        self.assertTrue(set(timings) <= set(STAGE_KEYS))
        for stage in (
            "worker_start", "browser_prep", "auth_check", "provider_submit",
            "first_token", "provider_stream", "extraction",
        ):
            self.assertIn(stage, timings)
            self.assertGreaterEqual(timings[stage], 0)
        # Real sleeps are visible, so these are wall-measured, not created_at-derived.
        self.assertGreaterEqual(timings["browser_prep"], 9)
        self.assertGreaterEqual(timings["provider_submit"], 9)
        self.assertGreaterEqual(timings["first_token"], 9)
        self.assertGreaterEqual(timings["provider_stream"], 15)
        self.assertGreaterEqual(timings["extraction"], 9)
        # Only the terminal event carries timings.
        self.assertEqual(
            [e["type"] for e in events if "timings_ms" in e["data"]], ["completed"]
        )

    def test_mock_adapter_off_by_default_and_deterministic(self):
        payload = {"job_id": "j", "job": {"input": {"prompt": "x y z"}}}
        with _env(None):
            first, second = build_mock_events(payload), build_mock_events(payload)
        self.assertEqual(first, second)
        self.assertNotIn("timings_ms", _completed(first)["data"])

    def test_mock_adapter_emits_timings_when_enabled(self):
        payload = {"job_id": "j", "job": {"input": {"prompt": "x y z"}}}
        with _env("true"):
            timings = _completed(build_mock_events(payload))["data"]["timings_ms"]
        self.assertTrue(set(timings) <= set(STAGE_KEYS))
        self.assertEqual(
            set(timings), {"worker_start", "first_token", "provider_stream", "extraction"}
        )
        self.assertTrue(all(v >= 0 for v in timings.values()))


if __name__ == "__main__":
    unittest.main()

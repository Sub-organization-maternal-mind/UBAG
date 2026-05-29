import io
import json
import sys
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))
sys.path.insert(0, str(REPO_ROOT / "adapters" / "mock"))

from ubag_worker.runner import emit_jsonl, load_payload_from_text  # noqa: E402


class WorkerRunnerTests(unittest.TestCase):
    def test_emit_jsonl_writes_one_json_object_per_line(self):
        payload = {
            "job_id": "job-jsonl",
            "job": {
                "target": "mock",
                "input": {"prompt": "stream me"},
                "options": {"mock_tokens": ["one", " ", "two"]},
            },
        }
        stream = io.StringIO()

        count = emit_jsonl(payload, stream)
        lines = stream.getvalue().splitlines()
        events = [json.loads(line) for line in lines]

        self.assertEqual(count, len(lines))
        self.assertEqual(events[0]["type"], "queued")
        self.assertEqual(events[1]["type"], "running")
        self.assertEqual(events[-1]["type"], "completed")
        self.assertEqual(events[-1]["data"]["result"]["text"], "one two")

    def test_emit_jsonl_accepts_gateway_dispatch_envelope(self):
        payload = {
            "api_version": "2026-05-22",
            "job_id": "job-gateway-envelope",
            "tenant_id": "tenant_edge",
            "app_id": "app_default",
            "idempotency_key": "idem-worker-envelope",
            "trace_id": "trace_worker_envelope",
            "client": {"app_id": "fixture", "app_version": "0.0.0"},
            "created_at": "2026-05-23T00:00:00Z",
            "job": {
                "target": "mock",
                "command_type": "submit",
                "input": {"prompt": "gateway envelope"},
                "options": {"mock_tokens": ["gateway", " ", "ok"]},
                "context": {"account_binding_id": "acct_1"},
            },
        }
        stream = io.StringIO()

        count = emit_jsonl(payload, stream)
        events = [json.loads(line) for line in stream.getvalue().splitlines()]

        self.assertEqual(count, len(events))
        self.assertEqual(events[0]["job_id"], "job-gateway-envelope")
        self.assertEqual(events[0]["trace_id"], "trace_worker_envelope")
        self.assertEqual(events[-1]["data"]["result"]["text"], "gateway ok")

    def test_load_payload_requires_json_object(self):
        with self.assertRaises(ValueError):
            load_payload_from_text('["not", "an", "object"]')


if __name__ == "__main__":
    unittest.main()

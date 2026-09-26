import os
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from ubag_antigravity_sdk_adapter.adapter import AntigravitySDKAdapter
from ubag_antigravity_sdk_adapter.capabilities import get_cli_capabilities, get_sdk_capabilities
from ubag_antigravity_sdk_adapter.events import AgentEventContext, ToolCall, UsageMetrics
from ubag_antigravity_sdk_adapter.sdk_client import SDKResponse


class TestCapabilities(unittest.TestCase):
    def test_sdk_default_model_is_38(self):
        caps = get_sdk_capabilities()
        self.assertEqual(caps.default_model, "gemini-3.8-flash")

    def test_sdk_default_thinking_is_high(self):
        caps = get_sdk_capabilities()
        self.assertEqual(caps.default_thinking, "high")

    def test_sdk_has_compaction(self):
        caps = get_sdk_capabilities()
        self.assertIn("compaction", caps.capabilities)

    def test_sdk_has_budgets(self):
        caps = get_sdk_capabilities()
        self.assertIn("budgets", caps.capabilities)

    def test_cli_default_model_is_38(self):
        caps = get_cli_capabilities()
        self.assertEqual(caps.default_model, "gemini-3.8-flash")

    def test_cli_default_thinking_is_high(self):
        self.assertEqual(get_cli_capabilities().default_thinking, "high")


class TestEvents(unittest.TestCase):
    def test_agent_event_context(self):
        ctx = AgentEventContext(
            request_id="req_123",
            provider="antigravity_sdk",
            model="gemini-3.8-flash",
            conversation_id="conv_456",
        )
        d = ctx.to_dict()
        self.assertEqual(d["request_id"], "req_123")
        self.assertEqual(d["provider"], "antigravity_sdk")
        self.assertEqual(d["model"], "gemini-3.8-flash")

    def test_usage_metrics(self):
        u = UsageMetrics(input_tokens=100, output_tokens=50, thinking_tokens=25, cached_tokens=10, total_tokens=185)
        d = u.to_dict()
        self.assertEqual(d["input_tokens"], 100)
        self.assertEqual(d["total_tokens"], 185)

    def test_tool_call(self):
        tc = ToolCall(tool_call_id="tc_1", tool_name="read_file", arguments='{"path": "/tmp/test"}')
        d = tc.to_dict()
        self.assertEqual(d["tool_call_id"], "tc_1")
        self.assertEqual(d["tool_name"], "read_file")


class TestAdapter(unittest.TestCase):
    def test_adapter_name(self):
        adapter = AntigravitySDKAdapter()
        self.assertEqual(adapter.name, "antigravity_sdk")

    def test_adapter_version(self):
        adapter = AntigravitySDKAdapter()
        self.assertEqual(adapter.version, "0.2.0")

    def test_normalize_payload_missing(self):
        adapter = AntigravitySDKAdapter()
        with self.assertRaises(Exception):
            adapter.run("not a dict")

    def test_disallowed_secret_rejected(self):
        payload = {"job": {"target": "antigravity_sdk", "input": {"prompt": "test", "api_key": "secret123"}}}
        adapter = AntigravitySDKAdapter()
        with self.assertRaises(Exception):
            adapter.run(payload)

    def test_worker_events_preserve_gateway_envelope(self):
        payload = {
            "api_version": "2026-05-22",
            "job_id": "job_gateway_sdk",
            "trace_id": "trace_gateway_sdk",
            "job": {"target": "antigravity_sdk", "input": {"prompt": "hello"}},
        }
        response = SDKResponse(text="hello", model="gemini-3.8-flash", effort="high")
        with patch.dict(os.environ, {"GEMINI_API_KEY": "test-only"}):
            with patch.object(AntigravitySDKAdapter, "_execute", return_value=response):
                events = AntigravitySDKAdapter().run(payload)
        self.assertEqual(events[-1]["type"], "completed")
        self.assertTrue(all(event["job_id"] == payload["job_id"] for event in events))
        self.assertTrue(all(event["trace_id"] == payload["trace_id"] for event in events))
        self.assertTrue(all(event["api_version"] == payload["api_version"] for event in events))


class TestCLICapabilities(unittest.TestCase):
    def test_cli_provider_id(self):
        caps = get_cli_capabilities()
        self.assertEqual(caps.provider_id, "antigravity_cli")

    def test_cli_auth_modes(self):
        caps = get_cli_capabilities()
        self.assertIn("google_oauth", caps.auth_modes)


if __name__ == "__main__":
    unittest.main()

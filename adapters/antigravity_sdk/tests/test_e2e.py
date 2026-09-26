import os
import sys
import unittest
from types import ModuleType, SimpleNamespace
from unittest.mock import AsyncMock, MagicMock, patch

sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

from ubag_antigravity_sdk_adapter.sdk_client import (
    AdaptiveRateLimiter,
    AntigravitySDKClient,
    CircuitBreaker,
    SDKConfig,
    SDKResponse,
    SessionPool,
)


class TestCircuitBreaker(unittest.TestCase):
    def test_starts_closed(self):
        cb = CircuitBreaker(failure_threshold=3)
        self.assertEqual(cb.state, "closed")
        self.assertTrue(cb.can_execute())

    def test_opens_after_threshold(self):
        cb = CircuitBreaker(failure_threshold=3)
        cb.record_failure()
        cb.record_failure()
        self.assertEqual(cb.state, "closed")
        cb.record_failure()
        self.assertEqual(cb.state, "open")
        self.assertFalse(cb.can_execute())

    def test_recovery_after_timeout(self):
        cb = CircuitBreaker(failure_threshold=2, recovery_timeout=0.1)
        cb.record_failure()
        cb.record_failure()
        self.assertEqual(cb.state, "open")
        import time
        time.sleep(0.15)
        self.assertEqual(cb.state, "half_open")
        self.assertTrue(cb.can_execute())

    def test_success_resets(self):
        cb = CircuitBreaker(failure_threshold=3)
        cb.record_failure()
        cb.record_failure()
        cb.record_success()
        self.assertEqual(cb.state, "closed")
        self.assertEqual(cb._failures, 0)


class TestAdaptiveRateLimiter(unittest.TestCase):
    def test_initial_concurrency(self):
        rl = AdaptiveRateLimiter(initial_concurrency=3)
        self.assertEqual(rl.concurrency, 3)

    def test_rate_limit_reduces_concurrency(self):
        rl = AdaptiveRateLimiter(initial_concurrency=3, min_concurrency=1)
        rl.record_rate_limit()
        self.assertEqual(rl.concurrency, 2)

    def test_high_latency_reduces_concurrency(self):
        rl = AdaptiveRateLimiter(initial_concurrency=3, min_concurrency=1, latency_threshold_ms=1000)
        rl.record_latency(5000)
        self.assertEqual(rl.concurrency, 2)


class TestSDKConfig(unittest.TestCase):
    def test_default_model(self):
        config = SDKConfig(api_key="test_key")
        self.assertEqual(config.model, "gemini-3.8-flash")

    def test_default_effort(self):
        config = SDKConfig(api_key="test_key")
        self.assertEqual(config.effort, "high")

    def test_compaction_enabled_by_default(self):
        config = SDKConfig(api_key="test_key")
        self.assertTrue(config.compaction_enabled)


class TestSDKResponse(unittest.TestCase):
    def test_total_tokens(self):
        resp = SDKResponse(text="hello", token_usage={"total_tokens": 15})
        self.assertEqual(resp.total_tokens, 15)

    def test_empty_response(self):
        resp = SDKResponse()
        self.assertEqual(resp.text, "")
        self.assertEqual(resp.total_tokens, 0)


class TestSDKChatContract(unittest.IsolatedAsyncioTestCase):
    async def test_real_sdk_config_shape_when_installed(self):
        try:
            from google.antigravity import Agent
        except ImportError:
            self.skipTest("google-antigravity optional dependency is not installed")

        with patch.object(Agent, "__aenter__", new_callable=AsyncMock):
            agent = await SessionPool(SDKConfig(api_key="test-only", effort="high"))._create_agent()

        self.assertEqual(agent._config.model.name, "gemini-3.8-flash")
        self.assertEqual(agent._config.model.endpoint.options.thinking_level.value, "high")
        self.assertEqual(agent._config.capabilities.enabled_tools, [])

    async def test_agent_config_binds_model_effort_and_safe_tools(self):
        def config_object(**fields):
            return SimpleNamespace(**fields)

        class FakeAgent:
            def __init__(self, config):
                self.config = config

            async def __aenter__(self):
                return self

        fake_sdk = ModuleType("google.antigravity")
        fake_sdk.Agent = FakeAgent
        for name in (
            "LocalAgentConfig", "ModelTarget", "GeminiAPIEndpoint", "GeminiModelOptions",
            "CapabilitiesConfig", "CompactionConfig", "RetryConfig",
            "ModelAPIRetryConfig", "ToolOutputTruncationConfig", "VertexEndpoint",
        ):
            setattr(fake_sdk, name, config_object)
        fake_types = ModuleType("google.antigravity.types")
        fake_types.BudgetConfig = config_object
        fake_sdk.ModelType = SimpleNamespace(TEXT="text")
        fake_sdk.ThinkingLevel = str
        fake_sdk.BuiltinTools = SimpleNamespace(none=lambda: [])
        fake_sdk.AgentBehavior = SimpleNamespace(MINIMAL="minimal")

        with patch.dict(sys.modules, {"google.antigravity": fake_sdk, "google.antigravity.types": fake_types}):
            agent = await SessionPool(SDKConfig(api_key="test-only", effort="high"))._create_agent()

        self.assertEqual(agent.config.model.name, "gemini-3.8-flash")
        self.assertEqual(agent.config.model.endpoint.options.thinking_level, "high")
        self.assertEqual(agent.config.capabilities.enabled_tools, [])
        self.assertEqual(agent.config.budget_config.max_model_calls, 20)

    async def test_sdk_chat_accepts_only_prompt_and_reads_async_response(self):
        class FakeResponse:
            usage_metadata = SimpleNamespace(prompt_token_count=2, candidates_token_count=1, total_token_count=3)
            stop_reason = "STOP"

            async def text(self):
                return "hello"

            async def structured_output(self):
                return {"answer": "hello"}

            @property
            def tool_calls(self):
                async def calls():
                    yield SimpleNamespace(id="call-1", name="view_file", args={"path": "test.txt"})
                return calls()

        class FakeAgent:
            conversation_id = "sdk_conversation_id"

            async def chat(self, prompt):
                if prompt != "hello":
                    raise AssertionError("wrong prompt")
                return FakeResponse()

        client = AntigravitySDKClient(SDKConfig(api_key="test-only"))
        client._pool = MagicMock()
        client._pool.acquire_turn = AsyncMock(return_value=SimpleNamespace(agent=FakeAgent()))
        client._pool.release_turn = AsyncMock()
        response = await client.chat("hello")

        self.assertEqual(response.text, "hello")
        self.assertEqual(response.structured_output, {"answer": "hello"})
        self.assertEqual(response.conversation_id, "sdk_conversation_id")
        self.assertEqual(response.tool_calls[0]["tool_call_id"], "call-1")
        self.assertEqual(response.token_usage["total_tokens"], 3)


class TestAdapterEventContract(unittest.TestCase):
    def test_event_types_present(self):
        from ubag_antigravity_sdk_adapter.events import (
            AgentEventContext,
            ToolCall,
            UsageMetrics,
            make_cancellation_event,
            make_event,
            make_stop_reason_event,
            make_structured_output_event,
            make_text_delta_event,
            make_tool_call_event,
            make_usage_event,
        )
        ctx = AgentEventContext(request_id="r1", provider="antigravity_sdk", model="gemini-3.8-flash")
        events = [
            make_event(ctx, 1, "queued", {}),
            make_text_delta_event(ctx, 2, "hello"),
            make_tool_call_event(ctx, 3, ToolCall("tc1", "read_file")),
            make_usage_event(ctx, 4, UsageMetrics(total_tokens=100)),
            make_structured_output_event(ctx, 5, {"key": "value"}),
            make_cancellation_event(ctx, 6),
            make_stop_reason_event(ctx, 7, "STOP"),
        ]
        types = [e["type"] for e in events]
        self.assertIn("queued", types)
        self.assertIn("text_delta", types)
        self.assertIn("tool_call", types)
        self.assertIn("usage_update", types)
        self.assertIn("structured_output", types)
        self.assertIn("cancelled", types)
        self.assertIn("stop_reason", types)


if __name__ == "__main__":
    unittest.main()

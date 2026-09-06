"""Tests for the formal TargetAdapter Protocol and MockAdapter."""

from ubag_worker.adapters._common.base import (
    AdapterCapabilities,
    AdapterResult,
    TargetAdapter,
)
from ubag_worker.adapters.mock.adapter import MockAdapter


class TestAdapterCapabilities:
    def test_defaults(self):
        cap = AdapterCapabilities()
        assert cap.supports_streaming is False
        assert cap.supports_conversation is True
        assert cap.max_context_tokens is None
        assert cap.adapter_version == "0.1.0"

    def test_custom_values(self):
        cap = AdapterCapabilities(
            supports_streaming=True,
            max_context_tokens=8192,
            adapter_version="2.0.0",
            command_types=["submit"],
        )
        assert cap.supports_streaming is True
        assert cap.max_context_tokens == 8192
        assert "submit" in cap.command_types


class TestAdapterResult:
    def test_defaults(self):
        r = AdapterResult()
        assert r.text == ""
        assert r.tokens == []
        assert r.error_code is None
        assert r.cached is False

    def test_with_values(self):
        r = AdapterResult(text="hello", tokens=["hello"], cached=True)
        assert r.text == "hello"
        assert r.cached is True


class TestTargetAdapterProtocol:
    def test_mock_adapter_satisfies_protocol(self):
        adapter = MockAdapter()
        assert isinstance(adapter, TargetAdapter)

    def test_plain_object_does_not_satisfy_protocol(self):
        assert not isinstance(object(), TargetAdapter)


class TestMockAdapter:
    def test_capabilities(self):
        adapter = MockAdapter()
        cap = adapter.capabilities()
        assert isinstance(cap, AdapterCapabilities)
        assert cap.supports_streaming is True
        assert "submit" in cap.command_types

    def test_happy_path(self):
        adapter = MockAdapter(response_text="Test response")
        page = None  # mock adapter ignores page
        assert adapter.health_check(page) is True
        assert adapter.ensure_logged_in(page) is True
        assert adapter.open_conversation(page, "conv-1") is True
        assert adapter.submit_prompt(page, "Hello?") is True
        assert adapter.wait_for_completion(page) is True
        result = adapter.extract_output(page)
        assert result.text == "Test response"

    def test_streaming(self):
        adapter = MockAdapter(response_tokens=["Hello", " world"])
        tokens = list(adapter.stream_tokens(None))
        assert tokens == ["Hello", " world"]

    def test_normalized_output_is_identity(self):
        adapter = MockAdapter()
        r = AdapterResult(text="x")
        assert adapter.normalize_output(r) is r

    def test_call_tracking(self):
        adapter = MockAdapter()
        adapter.health_check(None)
        adapter.health_check(None)
        assert adapter.call_count("health_check") == 2
        assert adapter.call_count("ensure_logged_in") == 0

    def test_failure_modes(self):
        adapter = MockAdapter(health_ok=False, authenticated=False)
        assert adapter.health_check(None) is False
        assert adapter.ensure_logged_in(None) is False

    def test_on_error_and_teardown_do_not_raise(self):
        adapter = MockAdapter()
        adapter.on_error(None, RuntimeError("boom"))
        adapter.teardown(None)
        assert adapter.call_count("on_error") == 1
        assert adapter.call_count("teardown") == 1

    def test_extract_includes_metadata(self):
        adapter = MockAdapter(adapter_version="test-9.9.9")
        result = adapter.extract_output(None)
        assert result.metadata.get("adapter") == "mock"
        assert "9.9.9" in result.metadata.get("version", "")

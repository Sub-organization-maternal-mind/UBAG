"""MockAdapter — deterministic test double implementing TargetAdapter Protocol.

Used by unit tests and by the offline execution path.  Never touches a real
browser or network connection.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict, Iterator, List, Optional

from .._common.base import AdapterCapabilities, AdapterResult


@dataclass
class MockAdapter:
    """Fully scriptable adapter for tests.

    All boolean responses default to ``True`` (happy-path) and can be
    overridden per-field for negative-path tests.
    """

    # Configurable happy-path responses
    response_text: str = "This is a mock response."
    response_tokens: List[str] = field(default_factory=lambda: ["This", " is", " a", " mock", " response."])
    authenticated: bool = True
    health_ok: bool = True
    open_ok: bool = True
    resume_ok: bool = True
    submit_ok: bool = True
    wait_ok: bool = True

    # Per-instance metadata
    adapter_version: str = "mock-1.0.0"
    _calls: Dict[str, int] = field(default_factory=dict, repr=False)

    # ------------------------------------------------------------------
    # Protocol implementation
    # ------------------------------------------------------------------

    def capabilities(self) -> AdapterCapabilities:
        return AdapterCapabilities(
            supports_streaming=True,
            supports_conversation=True,
            max_context_tokens=None,
            adapter_version=self.adapter_version,
            command_types=["submit", "summarize", "translate", "extract"],
        )

    def health_check(self, page: Any) -> bool:
        self._track("health_check")
        return self.health_ok

    def ensure_logged_in(self, page: Any, timeout_seconds: float = 300.0) -> bool:
        self._track("ensure_logged_in")
        return self.authenticated

    def open_conversation(self, page: Any, conversation_id: str) -> bool:
        self._track("open_conversation")
        return self.open_ok

    def resume_conversation(self, page: Any, conversation_id: str) -> bool:
        self._track("resume_conversation")
        return self.resume_ok

    def submit_prompt(self, page: Any, prompt: str) -> bool:
        self._track("submit_prompt")
        return self.submit_ok

    def stream_tokens(self, page: Any) -> Iterator[str]:
        self._track("stream_tokens")
        yield from self.response_tokens

    def wait_for_completion(self, page: Any, timeout_seconds: float = 120.0) -> bool:
        self._track("wait_for_completion")
        return self.wait_ok

    def extract_output(self, page: Any) -> AdapterResult:
        self._track("extract_output")
        return AdapterResult(
            text=self.response_text,
            tokens=list(self.response_tokens),
            metadata={"adapter": "mock", "version": self.adapter_version},
        )

    def normalize_output(self, result: AdapterResult) -> AdapterResult:
        self._track("normalize_output")
        return result

    def on_error(self, page: Any, exc: Exception) -> None:
        self._track("on_error")

    def teardown(self, page: Any) -> None:
        self._track("teardown")

    # ------------------------------------------------------------------
    # Test helpers
    # ------------------------------------------------------------------

    def call_count(self, method: str) -> int:
        """Return how many times ``method`` was called."""
        return self._calls.get(method, 0)

    def _track(self, method: str) -> None:
        self._calls[method] = self._calls.get(method, 0) + 1

"""Formal TargetAdapter Protocol for all UBAG provider adapters (§13.3).

Every adapter — built-in or community-contributed — must implement the
``TargetAdapter`` Protocol so the live engine can drive any provider
interchangeably without knowing its specific implementation.

Hard constraints (mirrored from ``page_driver`` and ``adapter_registry``):

* No credential ingestion, no CAPTCHA solving, no cookie scraping.
* Authentication relies solely on the user's persistent browser profile.
* Adapters may NOT read, write, or transmit passwords, tokens, or cookies.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict, Iterator, List, Optional, Protocol, runtime_checkable

# ---------------------------------------------------------------------------
# Data contracts
# ---------------------------------------------------------------------------


@dataclass
class AdapterCapabilities:
    """Declared capabilities of a provider adapter."""

    supports_streaming: bool = False
    """True if the adapter can yield tokens incrementally."""

    supports_conversation: bool = True
    """True if the adapter supports multi-turn conversations."""

    max_context_tokens: Optional[int] = None
    """Provider-reported context limit, or None if unknown."""

    adapter_version: str = "0.1.0"
    """Semantic version of the adapter implementation."""

    command_types: List[str] = field(default_factory=list)
    """Supported command_type values (e.g. ['submit', 'summarize'])."""

    extra: Dict[str, Any] = field(default_factory=dict)
    """Adapter-specific metadata (model families, rate limits, etc.)."""


@dataclass
class AdapterResult:
    """Normalised output from a completed adapter run."""

    text: str = ""
    """Full response text (markdown-preserving)."""

    tokens: List[str] = field(default_factory=list)
    """Streamed token fragments, if streaming was used."""

    metadata: Dict[str, Any] = field(default_factory=dict)
    """Adapter-specific metadata (latency, model, finish_reason, etc.)."""

    raw: Optional[str] = None
    """Original raw provider response before normalisation, for debugging."""

    error_code: Optional[str] = None
    """UBAG error code if the adapter detected a partial or failed run."""

    cached: bool = False
    """True if the result was served from cache rather than a live run."""


# ---------------------------------------------------------------------------
# TargetAdapter Protocol
# ---------------------------------------------------------------------------


@runtime_checkable
class TargetAdapter(Protocol):
    """Protocol every UBAG provider adapter must implement.

    The protocol is intentionally *narrow*: it only specifies the contract
    that the live engine calls.  Adapters are free to add additional methods
    and state.  Use ``isinstance(obj, TargetAdapter)`` for duck-type checking
    at runtime.

    All methods that drive a browser receive ``page`` as their first positional
    argument.  ``page`` is opaque — adapters receive a ``PageDriver``-compatible
    object and must not import Playwright directly in their module body.
    """

    # ------------------------------------------------------------------
    # Lifecycle
    # ------------------------------------------------------------------

    def capabilities(self) -> AdapterCapabilities:
        """Return static capabilities declaration."""
        ...

    def health_check(self, page: Any) -> bool:
        """Return True if the provider page is reachable and responsive.

        Must not raise — return False on any failure.
        """
        ...

    # ------------------------------------------------------------------
    # Session management
    # ------------------------------------------------------------------

    def ensure_logged_in(self, page: Any, timeout_seconds: float = 300.0) -> bool:
        """Verify the current session is authenticated.

        The adapter may emit a ``session.manual_action_required`` signal if
        the user needs to log in manually via the noVNC viewer.  It must
        *never* attempt automated login or fill credential fields.

        Returns True when authenticated, False when login is still needed
        after the timeout.
        """
        ...

    def open_conversation(self, page: Any, conversation_id: str) -> bool:
        """Navigate to a new conversation context on the provider.

        Returns True on success, False if the provider page could not be
        reached or is in an unexpected state.
        """
        ...

    def resume_conversation(self, page: Any, conversation_id: str) -> bool:
        """Resume an existing conversation by its provider-local identifier.

        Returns True on success, False if the conversation no longer exists
        or cannot be resumed.
        """
        ...

    # ------------------------------------------------------------------
    # Job execution
    # ------------------------------------------------------------------

    def submit_prompt(self, page: Any, prompt: str) -> bool:
        """Type and submit ``prompt`` into the provider's input field.

        Returns True when the prompt was submitted, False on any failure
        (selector drift, network error, unexpected UI state).
        """
        ...

    def stream_tokens(self, page: Any) -> Iterator[str]:
        """Yield response tokens as they appear in the provider UI.

        The generator must be safe to exhaust without raising.  Adapters
        that don't support streaming may yield the full text in one chunk.
        """
        ...

    def wait_for_completion(self, page: Any, timeout_seconds: float = 120.0) -> bool:
        """Block until the provider finishes generating a response.

        Returns True when complete, False on timeout or error.
        """
        ...

    def extract_output(self, page: Any) -> AdapterResult:
        """Read and return the finished response from the provider UI."""
        ...

    def normalize_output(self, result: AdapterResult) -> AdapterResult:
        """Post-process the extracted result (clean formatting, strip boilerplate).

        The default identity implementation is acceptable for adapters that
        do not require normalisation.
        """
        ...

    # ------------------------------------------------------------------
    # Error handling & teardown
    # ------------------------------------------------------------------

    def on_error(self, page: Any, exc: Exception) -> None:
        """Called when the engine catches an unexpected error during the run.

        The adapter may attempt recovery (reload, dismiss dialogs) but must
        not raise — any exception from this method is silently logged.
        """
        ...

    def teardown(self, page: Any) -> None:
        """Release adapter-level resources.

        Called after every run (success *and* failure).  Must not raise.
        """
        ...

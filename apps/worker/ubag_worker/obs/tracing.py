"""OTLP trace context propagation for the UBAG worker (§18 / Task 2.4).

Parses a W3C traceparent from the gateway dispatch envelope and provides
helpers to continue the trace in worker spans.

No external OTel SDK is required in offline/offline-test mode; the module
degrades gracefully when UBAG_OTLP_ENDPOINT is unset.
"""
from __future__ import annotations

import os
import re
from dataclasses import dataclass
from typing import Optional

# W3C Trace Context — traceparent header format:
#   00-<trace-id>-<parent-id>-<flags>
_TRACEPARENT_RE = re.compile(
    r"^00-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$",
    re.IGNORECASE,
)


@dataclass(frozen=True)
class SpanContext:
    """Immutable span context parsed from a W3C traceparent."""

    trace_id: str   # 32 hex chars
    parent_id: str  # 16 hex chars
    flags: str      # 2 hex chars (e.g. "01" = sampled)

    @property
    def is_sampled(self) -> bool:
        """Return True if the trace flags indicate sampling."""
        try:
            return bool(int(self.flags, 16) & 0x01)
        except ValueError:
            return False

    def to_traceparent(self) -> str:
        """Serialise back to a W3C traceparent header value."""
        return f"00-{self.trace_id}-{self.parent_id}-{self.flags}"


def parse_traceparent(value: Optional[str]) -> Optional[SpanContext]:
    """Parse a W3C traceparent string.  Returns None on any parse failure."""
    if not value:
        return None
    m = _TRACEPARENT_RE.match(value.strip())
    if not m:
        return None
    return SpanContext(
        trace_id=m.group(1).lower(),
        parent_id=m.group(2).lower(),
        flags=m.group(3).lower(),
    )


def extract_from_envelope(envelope: dict) -> Optional[SpanContext]:
    """Extract the OTel span context from a UBAG dispatch envelope.

    The gateway embeds ``trace_context.traceparent`` (or top-level
    ``trace_id`` as a fallback) in the job envelope.
    """
    # Prefer the structured trace_context block.
    tc = envelope.get("trace_context") or {}
    tp = tc.get("traceparent") or envelope.get("traceparent") or ""

    # Fallback: gateway sets trace_id (32 chars) without a parent-id.
    if not tp:
        tid = envelope.get("trace_id") or ""
        if len(tid) == 32 and re.fullmatch(r"[0-9a-f]+", tid, re.IGNORECASE):
            tp = f"00-{tid.lower()}-{'0' * 16}-01"

    return parse_traceparent(tp)


def make_child_traceparent(parent: SpanContext, child_span_id: str) -> str:
    """Build a traceparent for a child span within the same trace."""
    return f"00-{parent.trace_id}-{child_span_id.lower()}-{parent.flags}"


# ── Optional OTel SDK integration ─────────────────────────────────────────────

def init_otlp_tracer(service_name: str = "ubag-worker") -> bool:
    """Initialise an OTel OTLP tracer if the SDK and endpoint are available.

    Returns True if initialisation succeeded, False if OTel is unavailable
    (e.g. UBAG_ADAPTER_OFFLINE=1 or missing packages).
    """
    endpoint = os.environ.get("UBAG_OTLP_ENDPOINT", "").strip()
    if not endpoint:
        return False

    try:
        from opentelemetry import trace  # type: ignore
        from opentelemetry.sdk.trace import TracerProvider  # type: ignore
        from opentelemetry.sdk.trace.export import BatchSpanProcessor  # type: ignore
        from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import (  # type: ignore
            OTLPSpanExporter,
        )

        provider = TracerProvider()
        exporter = OTLPSpanExporter(endpoint=endpoint, insecure=True)
        provider.add_span_processor(BatchSpanProcessor(exporter))
        trace.set_tracer_provider(provider)
        return True
    except ImportError:
        # OTel SDK not installed — run without tracing.
        return False

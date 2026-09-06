"""UBAG worker observability — logging, tracing, metrics (§18 / Task 2.4)."""

from .logging import UbagJsonHandler, get_logger, inject_trace_id, is_forbidden_key
from .metrics import WorkerMetrics, default_metrics
from .tracing import (
    SpanContext,
    extract_from_envelope,
    init_otlp_tracer,
    make_child_traceparent,
    parse_traceparent,
)

__all__ = [
    "UbagJsonHandler",
    "get_logger",
    "inject_trace_id",
    "is_forbidden_key",
    "WorkerMetrics",
    "default_metrics",
    "SpanContext",
    "extract_from_envelope",
    "init_otlp_tracer",
    "make_child_traceparent",
    "parse_traceparent",
]

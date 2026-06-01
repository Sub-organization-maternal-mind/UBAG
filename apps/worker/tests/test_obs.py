"""Offline tests for ubag_worker.obs (§18 / Task 2.4).

All tests run without network access (UBAG_ADAPTER_OFFLINE=1).
"""
from __future__ import annotations

import io
import json
import logging
import os

import pytest

# Ensure offline mode for all tests in this module.
os.environ.setdefault("UBAG_ADAPTER_OFFLINE", "1")

from ubag_worker.obs.logging import (
    UbagJsonHandler,
    get_logger,
    is_forbidden_key,
)
from ubag_worker.obs.metrics import WorkerMetrics
from ubag_worker.obs.tracing import (
    SpanContext,
    extract_from_envelope,
    make_child_traceparent,
    parse_traceparent,
)


# ── Logging tests ─────────────────────────────────────────────────────────────

class TestUbagJsonHandler:
    """JSON handler emits contract-required fields and redacts forbidden keys."""

    def _make_logger(self) -> tuple[logging.Logger, io.StringIO]:
        buf = io.StringIO()
        handler = UbagJsonHandler(stream=buf)
        handler.setFormatter(logging.Formatter("%(message)s"))
        logger = logging.getLogger(f"test.obs.{id(self)}")
        logger.handlers.clear()
        logger.addHandler(handler)
        logger.setLevel(logging.DEBUG)
        logger.propagate = False
        return logger, buf

    def _last_record(self, buf: io.StringIO) -> dict:
        buf.seek(0)
        lines = [l for l in buf.getvalue().splitlines() if l.strip()]
        assert lines, "no log output produced"
        return json.loads(lines[-1])

    def test_required_fields_present(self):
        logger, buf = self._make_logger()
        logger.info("hello test")
        rec = self._last_record(buf)
        for field in ("timestamp", "level", "environment", "service", "message", "trace_id"):
            assert field in rec, f"required field '{field}' missing from log record"

    def test_level_lowercased(self):
        logger, buf = self._make_logger()
        logger.warning("w")
        assert self._last_record(buf)["level"] == "warn"

    def test_message_matches(self):
        logger, buf = self._make_logger()
        logger.info("unique message 42")
        assert "unique message 42" in self._last_record(buf)["message"]

    def test_password_field_redacted(self):
        logger, buf = self._make_logger()
        logger.info("login", extra={"password": "hunter2", "username": "alice"})
        rec = self._last_record(buf)
        assert "password" not in rec, "password must be redacted"
        assert rec.get("username") == "alice", "non-sensitive key must be kept"

    def test_raw_prompt_redacted(self):
        logger, buf = self._make_logger()
        logger.info("prompt trace", extra={"raw_prompt": "secret instructions"})
        rec = self._last_record(buf)
        assert "raw_prompt" not in rec

    def test_api_key_redacted(self):
        logger, buf = self._make_logger()
        logger.info("api call", extra={"api_key": "sk-12345", "model": "gpt-4"})
        rec = self._last_record(buf)
        assert "api_key" not in rec
        assert rec.get("model") == "gpt-4"

    def test_user_token_redacted(self):
        logger, buf = self._make_logger()
        logger.info("auth", extra={"user_token": "tok_abc"})
        rec = self._last_record(buf)
        assert "user_token" not in rec


class TestIsForbiddenKey:
    def test_password(self):
        assert is_forbidden_key("password")

    def test_api_key(self):
        assert is_forbidden_key("api_key")

    def test_raw_prompt(self):
        assert is_forbidden_key("raw_prompt")

    def test_user_token(self):
        assert is_forbidden_key("user_token")

    def test_safe_key(self):
        assert not is_forbidden_key("username")
        assert not is_forbidden_key("target")
        assert not is_forbidden_key("job_id")


# ── Metrics tests ─────────────────────────────────────────────────────────────

class TestWorkerMetrics:
    def test_record_job_increments_counter(self):
        m = WorkerMetrics()
        m.record_job("success", duration_seconds=1.5)
        m.record_job("success", duration_seconds=0.5)
        m.record_job("failure", duration_seconds=0.1)
        snap = m.snapshot()
        assert snap["jobs_processed"]["success"] == 2
        assert snap["jobs_processed"]["failure"] == 1
        assert abs(snap["job_duration_sum"]["success"] - 2.0) < 1e-9

    def test_record_ingestion_increments(self):
        m = WorkerMetrics()
        m.record_ingestion("success")
        m.record_ingestion("failure")
        snap = m.snapshot()
        assert snap["result_ingestions"]["success"] == 1
        assert snap["result_ingestions"]["failure"] == 1

    def test_record_artifact(self):
        m = WorkerMetrics()
        m.record_artifact("success")
        snap = m.snapshot()
        assert snap["artifact_captures"]["success"] == 1

    def test_to_prometheus_text_contains_metric_names(self):
        m = WorkerMetrics()
        m.record_job("success", 1.0)
        m.record_ingestion("success")
        m.record_artifact()
        text = m.to_prometheus_text()
        assert "ubag_worker_jobs_processed_total" in text
        assert "ubag_worker_result_ingestions_total" in text
        assert "ubag_artifact_captures_total" in text

    def test_to_prometheus_text_no_high_cardinality_labels(self):
        """Label values must not contain raw IDs or UUIDs."""
        m = WorkerMetrics()
        text = m.to_prometheus_text()
        import re
        uuid_re = re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")
        assert not uuid_re.search(text), "UUID found in Prometheus label value"


# ── Tracing tests ─────────────────────────────────────────────────────────────

class TestParseTraceparent:
    def test_valid(self):
        tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
        ctx = parse_traceparent(tp)
        assert ctx is not None
        assert ctx.trace_id == "4bf92f3577b34da6a3ce929d0e0e4736"
        assert ctx.parent_id == "00f067aa0ba902b7"
        assert ctx.flags == "01"
        assert ctx.is_sampled is True

    def test_invalid_returns_none(self):
        assert parse_traceparent("garbage") is None
        assert parse_traceparent("") is None
        assert parse_traceparent(None) is None

    def test_unsampled_flag(self):
        tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-00"
        ctx = parse_traceparent(tp)
        assert ctx is not None
        assert ctx.is_sampled is False


class TestExtractFromEnvelope:
    def test_from_trace_context_block(self):
        envelope = {
            "trace_context": {
                "traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
            }
        }
        ctx = extract_from_envelope(envelope)
        assert ctx is not None
        assert ctx.trace_id == "4bf92f3577b34da6a3ce929d0e0e4736"

    def test_fallback_to_trace_id(self):
        envelope = {"trace_id": "4bf92f3577b34da6a3ce929d0e0e4736"}
        ctx = extract_from_envelope(envelope)
        assert ctx is not None
        assert ctx.trace_id == "4bf92f3577b34da6a3ce929d0e0e4736"

    def test_empty_envelope_returns_none(self):
        assert extract_from_envelope({}) is None

    def test_make_child_traceparent(self):
        parent = SpanContext(
            trace_id="4bf92f3577b34da6a3ce929d0e0e4736",
            parent_id="00f067aa0ba902b7",
            flags="01",
        )
        child_tp = make_child_traceparent(parent, "aabbccdd11223344")
        assert child_tp.startswith("00-4bf92f3577b34da6a3ce929d0e0e4736-aabbccdd11223344-01")


class TestInitOtlpTracer:
    def test_no_endpoint_returns_false(self):
        os.environ.pop("UBAG_OTLP_ENDPOINT", None)
        from ubag_worker.obs.tracing import init_otlp_tracer
        result = init_otlp_tracer()
        assert result is False  # no endpoint → no tracer

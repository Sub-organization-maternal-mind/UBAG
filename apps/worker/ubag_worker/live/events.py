"""Deterministic worker-event construction for the live engine.

These helpers mirror the JSONL event envelope produced by the mock adapter and
the safe-mode manual-session path in ``adapter_registry`` so downstream
consumers (gateway, dashboard, conformance tests) see one consistent schema.
"""

from __future__ import annotations

import hashlib
import json
import os
import time
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
from typing import Any, Dict, Iterator, Mapping

JsonObject = Dict[str, Any]

# Synthetic clock for offline/mock runs (byte-for-byte deterministic, matching
# the mock adapter and adapter_registry manual-session events). Production
# workers emit real wall-clock timestamps — a synthetic 2026-01-01 stored
# verbatim into /v1/concurrency and /v1/concurrency telemetry made those
# records useless for correlation. Set UBAG_WORKER_EVENT_CLOCK=fixed to opt
# back into the deterministic clock (tests, offline fixture generation).
BASE_CLOCK = datetime(2026, 1, 1, 0, 0, 0)
_REAL_CLOCK = os.environ.get("UBAG_WORKER_EVENT_CLOCK") != "fixed"

# Conversation-affinity telemetry event names. These are projected into the
# gateway conversations store by WorkerConsumer (intercepted, not appended to
# the job's lifecycle event log), mirroring the browser.topology_reported
# precedent. thread_bound = first binding for a new chat; thread_broken = the
# bound chat could no longer be resumed; thread_rebound = a broken/missing chat
# was replaced by a fresh one under the same conversation key.
CONVERSATION_THREAD_BOUND_EVENT_TYPE = "conversation.thread_bound"
CONVERSATION_THREAD_BROKEN_EVENT_TYPE = "conversation.thread_broken"
CONVERSATION_THREAD_REBOUND_EVENT_TYPE = "conversation.thread_rebound"


# Stage timings (P0.9). ``data.timings_ms`` on the terminal ``completed`` event;
# the closed key set mirrors job-event.schema.json (and observability JOB_STAGES).
# Measured with a monotonic clock around the real work - NOT derived from event
# ``created_at``, because the engine buffers the interaction and replays events
# after it ran. Inert unless UBAG_WORKER_STAGE_TIMINGS is truthy, so the default
# event stream stays byte-identical.
STAGE_KEYS = (
    "worker_start",
    "browser_prep",
    "auth_check",
    "attachment_materialize",
    "provider_submit",
    "first_token",
    "provider_stream",
    "extraction",
)
STAGE_TIMINGS_ENV = "UBAG_WORKER_STAGE_TIMINGS"


def stage_timings_enabled() -> bool:
    return os.environ.get(STAGE_TIMINGS_ENV, "").strip().lower() in ("1", "true", "yes", "on")


class StageTimer:
    """Accumulates per-stage durations (ms). Repeated spans (retries) sum."""

    def __init__(self) -> None:
        self._ms: Dict[str, float] = {}

    def add(self, stage: str, seconds: float) -> None:
        if stage not in STAGE_KEYS:
            raise ValueError("unknown stage %r" % stage)
        self._ms[stage] = self._ms.get(stage, 0.0) + max(seconds, 0.0) * 1000.0

    @contextmanager
    def span(self, stage: str) -> Iterator[None]:
        start = time.perf_counter()
        try:
            yield
        finally:  # a failed attempt still cost this much
            self.add(stage, time.perf_counter() - start)

    def as_dict(self) -> Dict[str, float]:
        return {k: round(self._ms[k], 3) for k in STAGE_KEYS if k in self._ms}


def digest(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def canonical_json(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True)


def timestamp(sequence: int) -> str:
    if _REAL_CLOCK:
        return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace(
            "+00:00", "Z"
        )
    return (BASE_CLOCK + timedelta(milliseconds=250 * (sequence - 1))).isoformat(
        timespec="milliseconds"
    ) + "Z"


def worker_event(
    *,
    api_version: str,
    job_id: str,
    trace_id: str,
    sequence: int,
    event_type: str,
    data: Mapping[str, Any],
) -> JsonObject:
    return {
        "api_version": api_version,
        "event_id": "evt_" + digest("%s:%s" % (job_id, sequence))[:16],
        "job_id": job_id,
        "trace_id": trace_id,
        "type": event_type,
        "sequence": sequence,
        "created_at": timestamp(sequence),
        "data": dict(data),
    }


__all__ = [
    "BASE_CLOCK",
    "CONVERSATION_THREAD_BOUND_EVENT_TYPE",
    "CONVERSATION_THREAD_BROKEN_EVENT_TYPE",
    "CONVERSATION_THREAD_REBOUND_EVENT_TYPE",
    "JsonObject",
    "STAGE_KEYS",
    "STAGE_TIMINGS_ENV",
    "StageTimer",
    "canonical_json",
    "digest",
    "stage_timings_enabled",
    "timestamp",
    "worker_event",
]

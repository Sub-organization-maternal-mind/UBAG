"""Deterministic worker-event construction for the live engine.

These helpers mirror the JSONL event envelope produced by the mock adapter and
the safe-mode manual-session path in ``adapter_registry`` so downstream
consumers (gateway, dashboard, conformance tests) see one consistent schema.
"""

from __future__ import annotations

import hashlib
import json
from datetime import datetime, timedelta
from typing import Any, Dict, Mapping

JsonObject = Dict[str, Any]

# Fixed clock so offline/mock runs are byte-for-byte deterministic, matching the
# mock adapter and adapter_registry manual-session events.
BASE_CLOCK = datetime(2026, 1, 1, 0, 0, 0)

# Conversation-affinity telemetry event names. These are projected into the
# gateway conversations store by WorkerConsumer (intercepted, not appended to
# the job's lifecycle event log), mirroring the browser.topology_reported
# precedent. thread_bound = first binding for a new chat; thread_broken = the
# bound chat could no longer be resumed; thread_rebound = a broken/missing chat
# was replaced by a fresh one under the same conversation key.
CONVERSATION_THREAD_BOUND_EVENT_TYPE = "conversation.thread_bound"
CONVERSATION_THREAD_BROKEN_EVENT_TYPE = "conversation.thread_broken"
CONVERSATION_THREAD_REBOUND_EVENT_TYPE = "conversation.thread_rebound"


def digest(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def canonical_json(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True)


def timestamp(sequence: int) -> str:
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
    "canonical_json",
    "digest",
    "timestamp",
    "worker_event",
]

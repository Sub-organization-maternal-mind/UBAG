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
    "JsonObject",
    "canonical_json",
    "digest",
    "timestamp",
    "worker_event",
]

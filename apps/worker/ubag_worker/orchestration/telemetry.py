"""Concurrency telemetry — the worker → gateway AIMD cap-change contract.

The worker owns the live :class:`~ubag_worker.orchestration.aimd.AIMDController`.
When a cap change occurs, the worker reports it to the gateway as a
``concurrency.cap_changed`` worker event so the gateway's read-only
ConcurrencyRegistry (``GET /v1/concurrency``) reflects live, worker-reported
lane ceilings. The gateway NEVER computes AIMD state; it only records what the
worker reports here.

POLICY: this is observability only. No scraping, no credential/storage-state
data ever flows through these events — only structural concurrency counters.
"""

from __future__ import annotations

from typing import Any, Dict, Optional

from .aimd import CapChange

#: Worker event type recorded by the gateway worker-event ingestion path. Kept in
#: lock-step with ``concurrencyChangeEventType`` in the Go gateway
#: (apps/gateway/internal/executor/workerconsumer.go).
CONCURRENCY_CHANGE_EVENT_TYPE = "concurrency.cap_changed"


def concurrency_change_data(
    *,
    target: str,
    identity_ref: str,
    change: CapChange,
    minimum: int,
    maximum: Optional[int],
    in_flight: int,
) -> Dict[str, Any]:
    """Build the ``data`` payload for a ``concurrency.cap_changed`` event.

    Parameters mirror the gateway ConcurrencyView projection. ``maximum`` may be
    ``None`` (unbounded additive increase) and is reported as ``0`` so the JSON
    integer contract stays stable; the dashboard treats ``0`` as "unbounded".
    """

    return {
        "target": target,
        "identity_ref": identity_ref,
        "current_cap": int(change.new),
        "min": int(minimum),
        "max": int(maximum) if maximum is not None else 0,
        "in_flight": int(in_flight),
        "reason": change.reason,
    }


__all__ = ["CONCURRENCY_CHANGE_EVENT_TYPE", "concurrency_change_data"]

"""Worker region configuration — reads UBAG_REGION and UBAG_WORKER_ROAMING."""

import os

NATS_SUBJECT_BASE = "ubag.jobs"


def current_region() -> str:
    """Return the region this worker is pinned to. Empty string = no pin."""
    return os.environ.get("UBAG_REGION", "").strip()


def is_roaming() -> bool:
    """Return True when UBAG_WORKER_ROAMING=1 (consume all regions)."""
    return os.environ.get("UBAG_WORKER_ROAMING", "").strip() == "1"


def subject_filter() -> str:
    """Return the NATS subject filter for this worker.

    - Roaming:           ubag.jobs.>
    - Pinned (region R): ubag.jobs.R.>
    - No pin (default):  ubag.jobs.default.>
    """
    if is_roaming():
        return f"{NATS_SUBJECT_BASE}.>"
    region = current_region() or "default"
    return f"{NATS_SUBJECT_BASE}.{region}.>"

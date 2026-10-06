#!/usr/bin/env python3
"""Long-lived warm-browser worker daemon (``UBAG_WORKER_DAEMON``).

Counterpart to :mod:`run_live_worker`, which the gateway spawns once per job.
This process stays up and keeps browser pages warm between jobs, so a job stops
paying to re-attach over CDP and cold-load the provider SPA.

Reads one job request per line on stdin and writes the engine's JSONL events plus
a terminal marker per job to stdout. See :mod:`ubag_worker.live.daemon_protocol`
for the framing and :mod:`ubag_worker.live.daemon` for the reuse safety gate.

Runs ONE job at a time by construction: never drive two pages against a single
provider account concurrently.
"""
from __future__ import annotations

import os
import signal
import sys
from pathlib import Path

# ---------------------------------------------------------------------------
# sys.path bootstrap — must happen before any ubag_worker imports
# ---------------------------------------------------------------------------

_WORKER_DIR = Path(__file__).resolve().parent
if str(_WORKER_DIR) not in sys.path:
    sys.path.insert(0, str(_WORKER_DIR))

from ubag_worker.live.daemon import WarmWorkerDaemon  # noqa: E402
from ubag_worker.live.daemon_protocol import serve  # noqa: E402


def _orchestrator_enabled() -> bool:
    raw = os.environ.get("UBAG_ORCHESTRATOR_ENABLED", "").strip().lower()
    return raw in ("1", "true", "yes", "on")


def _orchestrator_if_enabled(worker_id: str = "worker-daemon"):
    """LiveOrchestrator only when UBAG_ORCHESTRATOR_ENABLED is truthy (inert
    by default — matches the repo convention for risky runtime features)."""
    if not _orchestrator_enabled():
        return None
    from ubag_worker.live.orchestrator import LiveOrchestrator

    return LiveOrchestrator(worker_id=worker_id)


def _reap_orphan_slot_registries() -> None:
    """Slot mode only: close tabs left by slots beyond the current pool size."""
    from ubag_worker.live.identity_lock import slot_id
    from ubag_worker.live.page_driver import reap_orphan_registries

    raw = os.environ.get("UBAG_WORKER_POOL_SIZE", "").strip()
    if slot_id() is None or not raw.isdigit():
        return
    try:
        reap_orphan_registries(int(raw), os.environ.get("UBAG_REMOTE_BROWSER_ENDPOINT", "").strip())
    except Exception:  # noqa: BLE001 - best-effort housekeeping
        pass


def main() -> int:
    from ubag_worker.live.identity_lock import slot_id

    # Per-process AIMD would emit conflicting caps across pool slots.
    if slot_id() is not None and _orchestrator_enabled():
        sys.stderr.write(
            "[ubag-daemon] UBAG_ORCHESTRATOR_ENABLED is not supported in slot mode "
            "(UBAG_WORKER_SLOT_ID is set); refusing to start\n"
        )
        return 2
    _reap_orphan_slot_registries()
    daemon = WarmWorkerDaemon(orchestrator=_orchestrator_if_enabled())

    # SIGTERM/SIGINT previously leaked the warm browser page: nothing closed
    # the daemon. Close it (pages + driver) and exit cleanly.
    def _terminate(signum, _frame):
        try:
            daemon.close()
        except Exception:
            pass
        raise SystemExit(0)

    for _sig in (signal.SIGTERM, signal.SIGINT):
        try:
            signal.signal(_sig, _terminate)
        except (ValueError, OSError):
            pass

    return serve(sys.stdin, sys.stdout, daemon)


if __name__ == "__main__":
    raise SystemExit(main())

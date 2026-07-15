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


def main() -> int:
    return serve(sys.stdin, sys.stdout, WarmWorkerDaemon())


if __name__ == "__main__":
    raise SystemExit(main())

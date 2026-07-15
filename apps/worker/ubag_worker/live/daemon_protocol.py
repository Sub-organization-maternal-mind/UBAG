"""stdin/stdout framing for the warm worker daemon (Layer B <-> Layer C).

Request  (one JSON object per line on stdin):
    {"job_id": "...", "payload": {...}, "deadline_s": 420}

Response (JSONL on stdout):
    ... the engine's events, verbatim ...
    {"__ubag_job_end__": true, "job_id": "...", "status": "completed"|"failed",
     "error": "..."}

Why a terminal marker: the per-job worker signalled "job over" by exiting, and Go
read to EOF. A daemon never exits, so every job -- including one that raises --
MUST emit exactly one marker, or the Go side blocks forever on a job that is
already finished.

Events are forwarded untouched. This layer decides where the page comes from,
never what was captured from it.
"""
from __future__ import annotations

import json
import os
import sys
import threading
from typing import Any, Mapping, Optional, TextIO

JOB_END = "__ubag_job_end__"

# Exit code used when a job blows its deadline; mirrors today's semantics, where
# the Go side kills a worker that overruns its max runtime.
EXIT_DEADLINE = 75


def _dump(event: object) -> str:
    return json.dumps(event, sort_keys=True, separators=(",", ":"), ensure_ascii=True)


def _emit(stream: TextIO, event: object) -> None:
    stream.write(_dump(event))
    stream.write("\n")
    stream.flush()


class _Deadline:
    """Hard per-job deadline.

    The Go runner used to bound a job by killing its subprocess; a daemon
    outlives the job, so the bound has to live here. Enforcement is a hard
    process exit rather than a cooperative check because the engine spends its
    time inside blocking Playwright calls that cannot be interrupted -- and a
    wedged browser call is exactly what a deadline exists to escape. Go observes
    EOF, fails the job, and restarts the daemon: the same outcome as today's
    killed worker.
    """

    def __init__(self, seconds: Optional[float], *, on_expire=None) -> None:
        self._timer: Optional[threading.Timer] = None
        if seconds and seconds > 0:
            self._timer = threading.Timer(seconds, on_expire or self._die)
            self._timer.daemon = True

    def __enter__(self) -> "_Deadline":
        if self._timer is not None:
            self._timer.start()
        return self

    def __exit__(self, *_exc) -> None:
        if self._timer is not None:
            self._timer.cancel()

    @staticmethod
    def _die() -> None:  # pragma: no cover - terminates the interpreter
        sys.stderr.write("[ubag-daemon] job exceeded deadline; exiting\n")
        sys.stderr.flush()
        os._exit(EXIT_DEADLINE)


def _deadline_seconds(request: Mapping[str, Any]) -> Optional[float]:
    try:
        value = float(request.get("deadline_s") or 0)
    except (TypeError, ValueError):
        return None
    return value if value > 0 else None


def serve(stdin: TextIO, stdout: TextIO, daemon: Any) -> int:
    """Run jobs off ``stdin`` until EOF. Returns a process exit code."""
    try:
        for line in stdin:
            line = line.strip()
            if not line:
                continue

            job_id = ""
            try:
                request = json.loads(line)
                job_id = str(request.get("job_id", ""))
                payload = request.get("payload")
                if not isinstance(payload, Mapping):
                    raise ValueError("request.payload must be a JSON object")
            except Exception as exc:  # noqa: BLE001
                # A malformed request is that request's failure, not the
                # daemon's: staying up keeps the warm pages for the next job.
                _emit(stdout, {
                    JOB_END: True,
                    "job_id": job_id,
                    "status": "failed",
                    "error": "bad request: %s" % exc,
                })
                continue

            status, error = "completed", None
            try:
                with _Deadline(_deadline_seconds(request)):
                    for event in daemon.run_job(payload):
                        _emit(stdout, event)
            except Exception as exc:  # noqa: BLE001
                status, error = "failed", str(exc)

            end = {JOB_END: True, "job_id": job_id, "status": status}
            if error is not None:
                end["error"] = error
            _emit(stdout, end)
    finally:
        daemon.close()
    return 0

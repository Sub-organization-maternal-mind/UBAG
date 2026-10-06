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

Protocol v2 is strictly opt-in (see worker-daemon-request.schema.json): a job line
carrying ``"proto": 2`` gets a JOB_END that also echoes proto/attempt_id/slot_id
and adds pid, warm_key (one-way hash), outcome_signal and submitted; a
``{"__ubag_control__": "hello"|"probe", "proto": 2}`` line gets one control reply.
A v1 line gets the exact v1 bytes: the daemon never emits an unsolicited line.

Events are forwarded untouched. This layer decides where the page comes from,
never what was captured from it.
"""
from __future__ import annotations

import json
import os
import sys
import threading
from typing import Any, Callable, Mapping, Optional, TextIO

JOB_END = "__ubag_job_end__"
CONTROL = "__ubag_control__"
PROTO_V2 = 2
# Capabilities this daemon implements, answered to an explicit v2 hello.
FEATURES = ["attempt", "outcome_signal", "strict_stream_end", "strict_submit"]

# Exit code used when a job blows its deadline; mirrors today's semantics, where
# the Go side kills a worker that overruns its max runtime.
EXIT_DEADLINE = 75


def _dump(event: object) -> str:
    return json.dumps(event, sort_keys=True, separators=(",", ":"), ensure_ascii=True)


def _emit(stream: TextIO, event: object) -> None:
    stream.write(_dump(event) + "\n")
    stream.flush()


class _JobOutput:
    """Serialize one job's events and let exactly one terminal marker win."""

    def __init__(
        self,
        stream: TextIO,
        job_id: str,
        *,
        v2: Optional[Mapping[str, Any]] = None,
    ) -> None:
        self._stream = stream
        self._job_id = job_id
        self._lock = threading.Lock()
        self._terminal = False
        # v2 only: echo fields for the marker; None keeps v1 output byte-identical.
        self._v2 = dict(v2) if v2 is not None else None
        self._submitted = False
        self._outcome = "none"
        self._timed_out = False

    @property
    def v2(self) -> bool:
        return self._v2 is not None

    def _observe(self, event: object) -> None:
        if not isinstance(event, Mapping):
            return
        etype = str(event.get("type", event.get("event_type", "")))
        data = event.get("data")
        data = data if isinstance(data, Mapping) else {}
        reason = str(data.get("reason", ""))
        if etype == "prompt_submitted" or data.get("submitted") is True:
            self._submitted = True
        if etype == "timed_out" or data.get("stream_end_reason") == "deadline":
            self._timed_out = True
            self._outcome = "timeout"
        elif reason == "selector_drift_detected":
            self._outcome = "drift"
        elif reason == "human_verification_required":
            self._outcome = "captcha"
        elif etype in ("blocked", "session.manual_action_required"):
            self._outcome = "manual_action"

    def emit_event(self, event: object) -> bool:
        with self._lock:
            if self._terminal:
                return False
            if self._v2 is not None:
                self._observe(event)
            _emit(self._stream, event)
            return True

    def finish(
        self,
        status: str,
        *,
        error: Optional[str] = None,
        reason: Optional[str] = None,
    ) -> bool:
        with self._lock:
            if self._terminal:
                return False
            self._terminal = True
            if reason == "worker_deadline_exceeded":
                self._outcome = "timeout"
            end = {JOB_END: True, "job_id": self._job_id, "status": status}
            if self._v2 is not None:
                if status == "completed" and self._timed_out:
                    end["status"] = "timed_out"  # D4: a deadline-cut stream is never completed
                end.update(self._v2)
                end["submitted"] = self._submitted
                end["outcome_signal"] = self._outcome
            if error is not None:
                end["error"] = error
            if reason is not None:
                end["reason"] = reason
            _emit(self._stream, end)
            return True


def _deadline_expired(
    output: _JobOutput,
    *,
    seconds: float,
    exit_process: Callable[[int], Any] = os._exit,
) -> None:
    """Emit a complete failure marker before the hard process exit."""

    try:
        output.finish(
            "timed_out" if output.v2 else "failed",
            reason="worker_deadline_exceeded",
            error="job deadline exceeded after %g seconds" % seconds,
        )
    finally:
        sys.stderr.write("[ubag-daemon] job exceeded deadline; exiting\n")
        sys.stderr.flush()
        exit_process(EXIT_DEADLINE)


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
            if self._timer is not threading.current_thread():
                self._timer.join()

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


def _slot_id(request: Mapping[str, Any]) -> Optional[int]:
    value = request.get("slot_id")
    if isinstance(value, int) and not isinstance(value, bool) and value >= 0:
        return value
    return None


def _v2_fields(request: Mapping[str, Any], payload: Mapping[str, Any], daemon: Any) -> dict:
    """Echo fields for a proto:2 JOB_END. Only built when the request opted in."""
    fields: dict = {"proto": PROTO_V2, "pid": os.getpid()}
    attempt = request.get("attempt")
    attempt_id = attempt.get("id") if isinstance(attempt, Mapping) else None
    if isinstance(attempt_id, str) and attempt_id:
        fields["attempt_id"] = attempt_id
    slot = _slot_id(request)
    if slot is not None:
        fields["slot_id"] = slot
    warm_key = getattr(daemon, "warm_key", None)
    if callable(warm_key):
        try:
            fields["warm_key"] = warm_key(payload)  # one-way hash, never a raw path
        except Exception:  # noqa: BLE001 - optional field, never fail the job on it
            pass
    return fields


def _control_reply(kind: str) -> dict:
    # One reply per explicit control line. Probe is read-only: the loop is serial,
    # so the daemon is idle whenever it reads a line.
    reply: dict = {CONTROL: kind, "proto": PROTO_V2, "pid": os.getpid()}
    slot = os.environ.get("UBAG_WORKER_SLOT_ID", "").strip()
    if slot.isdigit():
        reply["slot_id"] = int(slot)
    if kind == "hello":
        reply["features"] = list(FEATURES)
    else:
        reply["state"] = "idle"
    return reply


def serve(stdin: TextIO, stdout: TextIO, daemon: Any) -> int:
    """Run jobs off ``stdin`` until EOF. Returns a process exit code."""
    try:
        for line in stdin:
            line = line.strip()
            if not line:
                continue

            job_id = ""
            output: Optional[_JobOutput] = None
            try:
                request = json.loads(line)
                if (
                    request.get("proto") == PROTO_V2
                    and request.get(CONTROL) in ("hello", "probe")
                ):
                    _emit(stdout, _control_reply(request[CONTROL]))
                    continue
                job_id = str(request.get("job_id", ""))
                payload = request.get("payload")
                if not isinstance(payload, Mapping):
                    raise ValueError("request.payload must be a JSON object")
                output = _JobOutput(
                    stdout,
                    job_id,
                    v2=_v2_fields(request, payload, daemon)
                    if request.get("proto") == PROTO_V2
                    else None,
                )
            except Exception as exc:  # noqa: BLE001
                # A malformed request is that request's failure, not the
                # daemon's: staying up keeps the warm pages for the next job.
                (output or _JobOutput(stdout, job_id)).finish(
                    "failed", error="bad request: %s" % exc
                )
                continue

            assert output is not None
            status, error = "completed", None
            try:
                deadline_s = _deadline_seconds(request)
                with _Deadline(
                    deadline_s,
                    on_expire=lambda output=output, deadline_s=deadline_s: (
                        _deadline_expired(
                            output,
                            seconds=deadline_s or 0,
                        )
                    ),
                ):
                    for event in daemon.run_job(payload):
                        if not output.emit_event(event):
                            break
            except Exception as exc:  # noqa: BLE001
                status, error = "failed", str(exc)

            output.finish(status, error=error)
    finally:
        daemon.close()
    return 0

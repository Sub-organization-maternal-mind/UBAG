"""Tests for the warm daemon's stdin/stdout framing (Layer B <-> Layer C).

The per-job worker signalled "job over" by exiting; a daemon never exits, so the
protocol carries an explicit terminal marker instead. Go relies on that marker to
finish a job, so a job that dies MUST still produce one -- otherwise the Go side
would block forever on a job that is already over.
"""
import io
import json

from ubag_worker.live.daemon_protocol import JOB_END, serve


class _StubDaemon:
    def __init__(self, *, events=None, error=None):
        self._events = events or [{"event_type": "completed", "data": {}}]
        self._error = error
        self.jobs = []
        self.closed = False

    def run_job(self, payload):
        self.jobs.append(payload)
        for event in self._events:
            yield event
        if self._error is not None:
            raise self._error

    def close(self):
        self.closed = True


def _request(job_id="j1"):
    return json.dumps({"job_id": job_id, "payload": {"job": {"target": "gemini_web"}}})


def _lines(out):
    return [json.loads(line) for line in out.getvalue().splitlines() if line.strip()]


class TestFraming:
    def test_emits_engine_events_then_a_terminal_marker(self):
        daemon = _StubDaemon(events=[{"event_type": "queued"}, {"event_type": "completed"}])
        out = io.StringIO()

        serve(io.StringIO(_request() + "\n"), out, daemon)

        lines = _lines(out)
        assert [l.get("event_type") for l in lines[:2]] == ["queued", "completed"]
        assert lines[-1][JOB_END] is True
        assert lines[-1]["status"] == "completed"
        assert lines[-1]["job_id"] == "j1"

    def test_runs_multiple_jobs_over_one_stream(self):
        daemon = _StubDaemon()
        out = io.StringIO()

        serve(io.StringIO(_request("a") + "\n" + _request("b") + "\n"), out, daemon)

        ends = [l for l in _lines(out) if l.get(JOB_END)]
        assert [e["job_id"] for e in ends] == ["a", "b"]
        assert len(daemon.jobs) == 2

    def test_a_failed_job_still_terminates_and_the_daemon_keeps_serving(self):
        """A crash must be reported as this job's failure, not as a dead daemon:
        Go would otherwise wait forever on a job that already ended."""
        daemon = _StubDaemon(events=[{"event_type": "queued"}], error=RuntimeError("boom"))
        out = io.StringIO()

        serve(io.StringIO(_request("a") + "\n"), out, daemon)

        end = [l for l in _lines(out) if l.get(JOB_END)][-1]
        assert end["status"] == "failed"
        assert "boom" in end["error"]

    def test_malformed_request_does_not_kill_the_daemon(self):
        daemon = _StubDaemon()
        out = io.StringIO()

        serve(io.StringIO("{not json\n" + _request("b") + "\n"), out, daemon)

        ends = [l for l in _lines(out) if l.get(JOB_END)]
        assert ends[-1]["job_id"] == "b"
        assert ends[-1]["status"] == "completed"

    def test_closes_warm_drivers_on_shutdown(self):
        daemon = _StubDaemon()

        serve(io.StringIO(""), io.StringIO(), daemon)

        assert daemon.closed is True

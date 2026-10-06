"""Tests for the warm daemon's stdin/stdout framing (Layer B <-> Layer C).

The per-job worker signalled "job over" by exiting; a daemon never exits, so the
protocol carries an explicit terminal marker instead. Go relies on that marker to
finish a job, so a job that dies MUST still produce one -- otherwise the Go side
would block forever on a job that is already over.
"""
import io
import json

from ubag_worker.live.daemon_protocol import (
    EXIT_DEADLINE,
    JOB_END,
    _deadline_expired,
    _JobOutput,
    serve,
)


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
        assert [ln.get("event_type") for ln in lines[:2]] == ["queued", "completed"]
        assert lines[-1][JOB_END] is True
        assert lines[-1]["status"] == "completed"
        assert lines[-1]["job_id"] == "j1"

    def test_runs_multiple_jobs_over_one_stream(self):
        daemon = _StubDaemon()
        out = io.StringIO()

        serve(io.StringIO(_request("a") + "\n" + _request("b") + "\n"), out, daemon)

        ends = [ln for ln in _lines(out) if ln.get(JOB_END)]
        assert [e["job_id"] for e in ends] == ["a", "b"]
        assert len(daemon.jobs) == 2

    def test_a_failed_job_still_terminates_and_the_daemon_keeps_serving(self):
        """A crash must be reported as this job's failure, not as a dead daemon:
        Go would otherwise wait forever on a job that already ended."""
        daemon = _StubDaemon(events=[{"event_type": "queued"}], error=RuntimeError("boom"))
        out = io.StringIO()

        serve(io.StringIO(_request("a") + "\n"), out, daemon)

        end = [ln for ln in _lines(out) if ln.get(JOB_END)][-1]
        assert end["status"] == "failed"
        assert "boom" in end["error"]

    def test_malformed_request_does_not_kill_the_daemon(self):
        daemon = _StubDaemon()
        out = io.StringIO()

        serve(io.StringIO("{not json\n" + _request("b") + "\n"), out, daemon)

        ends = [ln for ln in _lines(out) if ln.get(JOB_END)]
        assert ends[-1]["job_id"] == "b"
        assert ends[-1]["status"] == "completed"

    def test_closes_warm_drivers_on_shutdown(self):
        daemon = _StubDaemon()

        serve(io.StringIO(""), io.StringIO(), daemon)

        assert daemon.closed is True

    def test_deadline_emits_a_terminal_failure_before_exiting(self):
        out = io.StringIO()
        exit_codes = []
        output = _JobOutput(out, "job_deadline")

        _deadline_expired(
            output,
            seconds=12.5,
            exit_process=exit_codes.append,
        )

        end = _lines(out)[-1]
        assert end[JOB_END] is True
        assert end["job_id"] == "job_deadline"
        assert end["status"] == "failed"
        assert end["reason"] == "worker_deadline_exceeded"
        assert "12.5" in end["error"]
        assert exit_codes == [EXIT_DEADLINE]

    def test_only_one_terminal_marker_can_be_emitted_for_a_job(self):
        out = io.StringIO()
        output = _JobOutput(out, "job_once")

        assert output.finish("completed") is True
        assert output.finish("failed", error="too late") is False
        assert output.emit_event({"type": "completed"}) is False

        ends = [line for line in _lines(out) if line.get(JOB_END)]
        assert len(ends) == 1
        assert ends[0]["status"] == "completed"


# --- protocol v2 (opt-in) ----------------------------------------------------


class _KeyedDaemon(_StubDaemon):
    def warm_key(self, payload):
        return "9f2b6c1d4e7a3b58"


def _v2_request(job_id="j1", **extra):
    req = {"job_id": job_id, "payload": {"job": {}}, "proto": 2}
    req.update(extra)
    return json.dumps(req)


class TestProtocolV2:
    def test_v1_request_output_is_byte_identical(self):
        daemon = _StubDaemon(events=[{"type": "queued"}])
        out = io.StringIO()

        serve(io.StringIO(_request("a") + "\n"), out, daemon)

        assert out.getvalue() == (
            '{"type":"queued"}\n'
            '{"__ubag_job_end__":true,"job_id":"a","status":"completed"}\n'
        )

    def test_v2_extras_without_proto_get_a_v1_marker(self):
        daemon = _KeyedDaemon(events=[{"type": "queued"}])
        out = io.StringIO()
        line = json.dumps(
            {"job_id": "a", "payload": {}, "slot_id": 1, "attempt": {"id": "att_x", "lease_generation": 1}}
        )

        serve(io.StringIO(line + "\n"), out, daemon)

        assert set(_lines(out)[-1]) == {JOB_END, "job_id", "status"}

    def test_v2_request_echoes_attempt_slot_and_adds_fields(self):
        daemon = _KeyedDaemon(
            events=[
                {"type": "queued", "data": {}},
                {"type": "prompt_submitted", "data": {}},
                {"type": "completed", "data": {}},
            ]
        )
        out = io.StringIO()
        req = _v2_request(slot_id=2, attempt={"id": "att_abc", "lease_generation": 3})

        serve(io.StringIO(req + "\n"), out, daemon)

        end = _lines(out)[-1]
        assert end[JOB_END] is True and end["status"] == "completed"
        assert end["proto"] == 2 and end["attempt_id"] == "att_abc" and end["slot_id"] == 2
        assert end["warm_key"] == "9f2b6c1d4e7a3b58"
        assert end["submitted"] is True and end["outcome_signal"] == "none"
        assert isinstance(end["pid"], int) and end["pid"] > 0
        assert len([ln for ln in _lines(out) if ln.get(JOB_END)]) == 1

    def test_v2_without_warm_key_support_omits_it(self):
        out = io.StringIO()

        serve(io.StringIO(_v2_request() + "\n"), out, _StubDaemon())

        end = _lines(out)[-1]
        assert "warm_key" not in end and end["submitted"] is False

    def test_v2_outcome_signals(self):
        cases = [
            ({"type": "blocked", "data": {"reason": "selector_drift_detected"}}, "drift"),
            ({"type": "blocked", "data": {"reason": "human_verification_required"}}, "captcha"),
            ({"type": "blocked", "data": {"reason": "manual_login_required"}}, "manual_action"),
            ({"type": "session.manual_action_required", "data": {}}, "manual_action"),
        ]
        for event, expected in cases:
            out = io.StringIO()
            serve(io.StringIO(_v2_request() + "\n"), out, _StubDaemon(events=[event]))
            assert _lines(out)[-1]["outcome_signal"] == expected

    def test_v2_deadline_cut_stream_ends_timed_out_never_completed(self):
        daemon = _StubDaemon(
            events=[
                {"type": "prompt_submitted", "data": {}},
                {"type": "timed_out", "data": {"stream_end_reason": "deadline", "submitted": True}},
            ]
        )
        out = io.StringIO()

        serve(io.StringIO(_v2_request() + "\n"), out, daemon)

        end = _lines(out)[-1]
        assert end["status"] == "timed_out" and end["outcome_signal"] == "timeout"
        # Same events on a v1 request: unchanged behaviour.
        out = io.StringIO()
        serve(io.StringIO(_request() + "\n"), out, daemon)
        assert _lines(out)[-1]["status"] == "completed"

    def test_v2_deadline_expiry_is_timed_out_v1_stays_failed(self):
        for v2, status in ((True, "timed_out"), (False, "failed")):
            out = io.StringIO()
            output = _JobOutput(out, "j", v2={"proto": 2} if v2 else None)
            _deadline_expired(output, seconds=1, exit_process=lambda _c: None)
            end = _lines(out)[-1]
            assert end["status"] == status and end["reason"] == "worker_deadline_exceeded"
            assert ("outcome_signal" in end) is v2

    def test_hello_and_probe_only_on_explicit_v2_control(self):
        out = io.StringIO()
        lines = [
            json.dumps({"__ubag_control__": "hello", "proto": 2}),
            json.dumps({"__ubag_control__": "probe", "proto": 2}),
        ]

        serve(io.StringIO("\n".join(lines) + "\n"), out, _StubDaemon())

        hello, probe = _lines(out)
        assert hello["__ubag_control__"] == "hello" and hello["proto"] == 2
        assert set(hello["features"]) <= {
            "attempt", "stream_events", "outcome_signal", "strict_stream_end", "strict_submit"
        }
        assert probe["state"] == "idle"
        assert JOB_END not in hello and JOB_END not in probe

    def test_control_line_without_proto_is_a_v1_bad_request(self):
        out = io.StringIO()

        serve(io.StringIO(json.dumps({"__ubag_control__": "hello"}) + "\n"), out, _StubDaemon())

        end = _lines(out)[-1]
        assert end[JOB_END] is True and end["status"] == "failed" and end["job_id"] == ""

    def test_slot_mode_refuses_orchestrator(self, monkeypatch):
        import run_worker_daemon

        monkeypatch.setenv("UBAG_WORKER_SLOT_ID", "1")
        monkeypatch.setenv("UBAG_ORCHESTRATOR_ENABLED", "true")

        assert run_worker_daemon.main() == 2

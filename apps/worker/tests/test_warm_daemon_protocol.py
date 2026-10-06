"""Tests for the warm daemon's stdin/stdout framing (Layer B <-> Layer C).

The per-job worker signalled "job over" by exiting; a daemon never exits, so the
protocol carries an explicit terminal marker instead. Go relies on that marker to
finish a job, so a job that dies MUST still produce one -- otherwise the Go side
would block forever on a job that is already over.
"""
import io
import json

import pytest
from ubag_worker.live.daemon import WarmWorkerDaemon, _driver_key
from ubag_worker.live.daemon_protocol import (
    EXIT_DEADLINE,
    JOB_END,
    _deadline_expired,
    _JobOutput,
    serve,
)
from ubag_worker.live.page_driver import MockPageDriver
from ubag_worker.live.selectors import PROVIDER_SELECTORS


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


# --- read-only provider readiness probe (proto:2 probe + payload) ------------


class _ReadOnlyDriver(MockPageDriver):
    """Any write-ish call is a test failure: a probe may only open + detect."""

    def submit_prompt(self, selectors, prompt):
        raise AssertionError("readiness probe must never submit")

    def await_manual_login(self, selectors, *, timeout_s):
        raise AssertionError("readiness probe must never drive a login")


def _probe_line(target="gemini_web", tenant="t1"):
    return json.dumps(
        {
            "__ubag_control__": "probe",
            "proto": 2,
            "payload": {"job": {"target": target}, "tenant_id": tenant, "input": {"prompt": "NO"}},
        }
    )


def _probe(daemon, line=None):
    out = io.StringIO()
    serve(io.StringIO((line or _probe_line()) + "\n"), out, daemon)
    (reply,) = _lines(out)
    return reply


@pytest.fixture
def probe_env(monkeypatch, tmp_path):
    monkeypatch.setenv("UBAG_CHAT_LEDGER_PATH", str(tmp_path / "ledger.jsonl"))
    monkeypatch.delenv("UBAG_WORKER_SLOT_ID", raising=False)
    monkeypatch.delenv("UBAG_WORKER_IDENTITY_LOCK", raising=False)
    monkeypatch.setenv("UBAG_WORKER_PROBE_MIN_INTERVAL_S", "0")
    return tmp_path


def _daemon(**driver_kwargs):
    drivers = []

    def factory(_options):
        d = _ReadOnlyDriver(**driver_kwargs)
        drivers.append(d)
        return d

    return WarmWorkerDaemon(driver_factory=factory), drivers


class TestReadinessProbe:
    def test_probe_reports_login_state_and_selector_version_without_writing(self, probe_env):
        daemon, drivers = _daemon(authenticated=True)

        reply = _probe(daemon)

        assert reply["__ubag_control__"] == "probe" and reply["proto"] == 2
        assert reply["state"] == "idle" and reply["login_state"] == "authenticated"
        assert reply["target"] == "gemini_web" and reply["selector_version"]
        assert len(drivers) == 1 and drivers[0].opened and drivers[0].closed
        assert drivers[0].submitted_prompt is None
        assert JOB_END not in reply

    def test_logged_out_session_maps_to_login_required_never_logs_in(self, probe_env):
        daemon, drivers = _daemon(authenticated=False, login_after_wait=True)

        assert _probe(daemon)["login_state"] == "login_required"
        assert drivers[0].authenticated is False  # await_manual_login was not called

    def test_selector_drift_and_driver_errors_are_unknown_without_leaking(self, probe_env):
        daemon, _ = _daemon(drift_group=PROVIDER_SELECTORS["gemini_web"].authenticated_signal.name)
        assert _probe(daemon)["login_state"] == "unknown"

        def boom(_options):
            raise RuntimeError("secret /home/u/profile")

        reply = _probe(WarmWorkerDaemon(driver_factory=boom))
        assert reply["login_state"] == "unknown" and "secret" not in json.dumps(reply)

    def test_unknown_target_is_a_generic_error_not_an_exception_text(self, probe_env):
        daemon, drivers = _daemon()

        reply = _probe(daemon, _probe_line(target="nope"))

        assert reply["error"] == "readiness_probe_failed" and "login_state" not in reply
        assert "nope" not in json.dumps(reply) and not drivers

    def test_probe_without_payload_stays_a_pure_liveness_query(self, probe_env):
        daemon, drivers = _daemon()
        line = json.dumps({"__ubag_control__": "probe", "proto": 2})

        reply = _probe(daemon, line)

        assert reply["state"] == "idle" and "login_state" not in reply and not drivers

    def test_probe_on_a_held_identity_returns_busy_and_does_not_touch_the_browser(
        self, probe_env, monkeypatch
    ):
        from ubag_worker.live import daemon as daemon_mod
        from ubag_worker.live.identity_lock import IdentityBusy

        class _Held:
            def __init__(self, key, *, blocking=True):
                assert blocking is False  # a probe must never wait for a job

            def __enter__(self):
                raise IdentityBusy("held")

            def __exit__(self, *_):
                pass

        monkeypatch.setenv("UBAG_WORKER_SLOT_ID", "0")
        monkeypatch.setattr(daemon_mod, "IdentityLock", _Held)
        daemon, drivers = _daemon()

        reply = _probe(daemon)

        assert reply["state"] == "busy" and "login_state" not in reply
        assert reply["selector_version"] and not drivers

    def test_probe_releases_the_lock_it_took(self, probe_env, monkeypatch):
        from ubag_worker.live import daemon as daemon_mod

        log = []

        class _Rec:
            def __init__(self, key, *, blocking=True):
                log.append(("new", blocking))

            def __enter__(self):
                log.append("enter")
                return self

            def __exit__(self, *_):
                log.append("exit")

        monkeypatch.setenv("UBAG_WORKER_SLOT_ID", "0")
        monkeypatch.setattr(daemon_mod, "IdentityLock", _Rec)
        daemon, _ = _daemon()

        assert _probe(daemon)["login_state"] == "authenticated"
        assert log == [("new", False), "enter", "exit"]

    def test_repeat_probes_inside_the_interval_are_served_from_cache(self, probe_env, monkeypatch):
        monkeypatch.setenv("UBAG_WORKER_PROBE_MIN_INTERVAL_S", "30")
        daemon, drivers = _daemon()
        now = [100.0]
        daemon._clock = lambda: now[0]

        first = _probe(daemon)
        now[0] += 5
        second = _probe(daemon)
        now[0] += 30
        third = _probe(daemon)

        assert "cached" not in first and second["cached"] is True
        assert second["login_state"] == "authenticated"
        assert "cached" not in third and len(drivers) == 2

    def test_probe_reuses_a_warm_page_and_leaves_it_open(self, probe_env):
        daemon, drivers = _daemon()
        warm = _ReadOnlyDriver()
        payload = json.loads(_probe_line())["payload"]
        daemon._warm[_driver_key(payload)] = warm

        reply = daemon.probe_readiness(json.loads(_probe_line())["payload"])

        assert reply["login_state"] == "authenticated"
        assert not drivers and warm.closed is False
        assert daemon._warm  # still warm for the next job

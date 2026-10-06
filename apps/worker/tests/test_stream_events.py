"""UBAG_WORKER_STREAM_EVENTS: pre-submit buffered, post-submit live, never resubmit (P3.8)."""
import io
import json
import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "apps" / "worker"))

from attachment_paths import temp_attachment  # noqa: E402
from ubag_worker.live import LiveSessionEngine, MockPageDriver  # noqa: E402
from ubag_worker.live.daemon import WarmWorkerDaemon  # noqa: E402
from ubag_worker.live.daemon_protocol import JOB_END, serve  # noqa: E402
from ubag_worker.live.engine import _stream_token_budget  # noqa: E402
from ubag_worker.live.selectors import get_provider_selectors  # noqa: E402

FLAG = "UBAG_WORKER_STREAM_EVENTS"
# Captured from the engine at the tip BEFORE this slice (created_at stripped).
GOLDEN = json.loads(
    (Path(__file__).parent / "golden" / "stream_events_flag_off.json").read_text(encoding="utf-8")
)
TERMINAL = {"completed", "timed_out", "failed_terminal", "blocked"}


class _Driver(MockPageDriver):
    """Records call order; can fail submits, or raise mid-stream / after the answer."""

    def __init__(self, *, fail_submits=0, raise_at=None, thread_url_raises=False, **kw):
        super().__init__(**kw)
        self.calls = []
        self.submit_calls = 0
        self._fail_submits = fail_submits
        self._raise_at = raise_at
        self._thread_url_raises = thread_url_raises

    def start_new_chat(self, selectors):
        self.calls.append("start_new_chat")
        return super().start_new_chat(selectors)

    def submit_prompt(self, selectors, prompt):
        self.calls.append("submit_prompt")
        self.submit_calls += 1
        if self.submit_calls <= self._fail_submits:
            raise RuntimeError("cdp hiccup before submit landed")
        super().submit_prompt(selectors, prompt)

    def stream_response(self, selectors, *, timeout_s):
        self.calls.append("stream_response")
        for index, token in enumerate(super().stream_response(selectors, timeout_s=timeout_s)):
            if index == self._raise_at:
                raise RuntimeError("secret page detail")
            yield token

    def read_final_response(self, selectors, *, return_mode="final"):
        self.calls.append("read_final_response")
        return super().read_final_response(selectors, return_mode=return_mode)

    def current_thread_url(self, selectors):
        if self._thread_url_raises:
            raise RuntimeError("page gone after the answer")
        return super().current_thread_url(selectors)


def _payload(**extra):
    payload = {
        "api_version": "v1", "job_id": "job_g", "trace_id": "trace_g",
        "target": "chatgpt_web", "prompt": "hi",
        "context": {"tenant_id": "t1"}, "options": {},
    }
    payload.update(extra)
    return payload


def _engine(target="chatgpt_web"):
    return LiveSessionEngine(get_provider_selectors(target))


def _run(driver, monkeypatch, *, flag=True, payload=None):
    if flag:
        monkeypatch.setenv(FLAG, "1")
    else:
        monkeypatch.delenv(FLAG, raising=False)
    return list(_engine().iter_events(payload or _payload(), driver=driver))


def _types(events):
    return [e["type"] for e in events]


def _strip(events):
    return json.loads(json.dumps([{k: v for k, v in e.items() if k != "created_at"} for e in events]))


@pytest.fixture(autouse=True)
def _clean_env(monkeypatch):
    for name in (
        "UBAG_WORKER_STRICT_SUBMIT",
        "UBAG_WORKER_STRICT_STREAM_END",
        "UBAG_WORKER_STREAM_MAX_TOKEN_EVENTS",
        "UBAG_WORKER_STAGE_TIMINGS",
    ):
        monkeypatch.delenv(name, raising=False)


def test_tokens_are_yielded_before_the_final_read(monkeypatch):
    monkeypatch.setenv(FLAG, "1")
    d = _Driver(tokens=["a", "b", "c"], response_text="abc")
    at = {}
    for event in _engine().iter_events(_payload(), driver=d):
        at.setdefault(event["type"], list(d.calls))
    assert "submit_prompt" in at["token"] and "read_final_response" not in at["token"]
    assert "read_final_response" in at["completed"]
    assert d.calls.count("submit_prompt") == 1


def test_flag_off_still_buffers_the_whole_interaction(monkeypatch):
    monkeypatch.delenv(FLAG, raising=False)
    d = _Driver(tokens=["a", "b", "c"])
    first_token_calls = None
    for event in _engine().iter_events(_payload(), driver=d):
        if event["type"] == "token" and first_token_calls is None:
            first_token_calls = list(d.calls)
    assert "read_final_response" in first_token_calls


def test_abandoning_the_stream_stops_the_interaction(monkeypatch):
    monkeypatch.setenv(FLAG, "1")
    d = _Driver(tokens=["a", "b", "c"])
    stream = _engine().iter_events(_payload(), driver=d)
    for event in stream:
        if event["type"] == "token":
            break
    stream.close()
    assert "read_final_response" not in d.calls and d.calls.count("submit_prompt") == 1


def test_pre_submit_events_are_held_until_submit_returns(monkeypatch):
    monkeypatch.setenv(FLAG, "1")
    d = _Driver()
    at = {}
    for event in _engine().iter_events(_payload(), driver=d):
        at.setdefault(event["type"], list(d.calls))
    # new_chat is the first interaction event, yet it is only released after submit.
    assert "submit_prompt" in at["session.new_chat"]
    assert "submit_prompt" in at["session.configured"]


def test_post_submit_exception_is_terminal_and_never_resubmits(monkeypatch):
    d = _Driver(raise_at=1, tokens=["a", "b", "c"], thread_url="https://chatgpt.com/c/abc")
    events = _run(d, monkeypatch)
    types = _types(events)
    assert d.submit_calls == 1 and "read_final_response" not in d.calls
    assert types.count("token") == 1  # streamed live before the failure
    assert types.index("prompt_submitted") < types.index("token") < types.index("failed_terminal")
    assert types[-1] == "failed_terminal" and sum(t in TERMINAL for t in types) == 1
    data = events[-1]["data"]
    assert data["submitted"] is True and data["retryable"] is False
    assert data["reconcile_required"] is True and data["stream_end_reason"] == "error"
    assert data["current_thread_url"] == "https://chatgpt.com/c/abc"
    assert "secret page detail" not in str(data)


def test_pre_submit_failures_still_retry_without_duplicate_events(monkeypatch):
    d = _Driver(fail_submits=2)
    events = _run(d, monkeypatch)
    types = _types(events)
    assert d.submit_calls == 3 and d.calls.count("start_new_chat") == 3
    assert types.count("session.new_chat") == 1 and types.count("prompt_submitted") == 1
    assert types[-1] == "completed"


def test_exhausted_pre_submit_retries_raise_and_emit_nothing_held(monkeypatch):
    monkeypatch.setenv(FLAG, "1")
    d = _Driver(fail_submits=99)
    seen = []
    with pytest.raises(RuntimeError):
        for event in _engine().iter_events(_payload(), driver=d):
            seen.append(event["type"])
    assert d.submit_calls == 3
    assert "session.new_chat" not in seen and "prompt_submitted" not in seen


def test_post_submit_drift_is_blocked_not_retried(monkeypatch):
    drift = get_provider_selectors("chatgpt_web").response_container.name
    d = _Driver(drift_group=drift)
    events = _run(d, monkeypatch)
    assert d.submit_calls == 1
    assert _types(events)[-2:] == ["prompt_submitted", "blocked"]
    assert events[-1]["data"]["reason"] == "selector_drift_detected"


def test_pre_submit_block_drops_held_events_like_the_buffered_path(monkeypatch):
    payload = {
        "api_version": "2026-05-22", "job_id": "job_att", "trace_id": "t_att",
        "job": {
            "target": "generic_live_web", "command_type": "chat.prompt",
            "input": {
                "prompt": "p",
                "attachments": [{"key": "a.pdf", "content_type": "application/pdf", "kind": "document"}],
                "attachment_local_paths": [temp_attachment("a.pdf")],
            },
            "context": {
                "account_binding_id": "acct", "consent_ref": "consent",
                "automation_scope": ["manual_login", "submit_prompt", "read_response"],
            },
        },
    }
    outcomes = []
    for flag in (False, True):
        d = _Driver()
        if flag:
            monkeypatch.setenv(FLAG, "1")
        else:
            monkeypatch.delenv(FLAG, raising=False)
        events = list(_engine("generic_live_web").iter_events(payload, driver=d))
        assert d.submit_calls == 0
        assert events[-1]["data"]["reason"] == "attachment_not_supported_by_target"
        outcomes.append(_types(events))
    assert outcomes[0] == outcomes[1]


def test_deadline_cut_stream_is_timed_out_after_live_tokens(monkeypatch):
    monkeypatch.setenv("UBAG_WORKER_STRICT_STREAM_END", "1")
    d = _Driver(tokens=["a", "b"], scripted_end_reason="deadline")
    events = _run(d, monkeypatch)
    types = _types(events)
    assert types[-1] == "timed_out" and "completed" not in types
    assert "read_final_response" not in d.calls
    assert events[-1]["data"]["partial"] == {"text": "ab", "token_events": 2}


def test_thread_bind_failure_after_the_answer_never_adds_a_second_terminal(monkeypatch):
    payload = _payload(conversation={"key": "k1", "on_missing": "fail"})
    events = _run(_Driver(thread_url_raises=True), monkeypatch, payload=payload)
    types = _types(events)
    assert types[-1] == "completed" and sum(t in TERMINAL for t in types) == 1
    assert "conversation.thread_bound" not in types
    # Buffered path unchanged: the failure propagates (and the whole interaction retried).
    with pytest.raises(RuntimeError):
        _run(_Driver(thread_url_raises=True), monkeypatch, flag=False, payload=payload)


def test_event_ids_are_attempt_scoped_and_stable_within_an_attempt(monkeypatch):
    def ids(attempt):
        events = _run(_Driver(), monkeypatch, payload=_payload(attempt={"id": attempt}))
        assert {e["data"]["attempt_id"] for e in events} == {attempt}
        return [e["event_id"] for e in events]

    first, again, other = ids("att_a1"), ids("att_a1"), ids("att_a2")
    assert first == again and len(set(first)) == len(first)
    assert not set(first) & set(other)


@pytest.mark.parametrize("scenario", sorted(GOLDEN))
def test_flag_off_equals_golden(monkeypatch, scenario):
    monkeypatch.delenv(FLAG, raising=False)
    payload = {
        "plain": _payload(),
        "attempt": _payload(attempt={"id": "att1"}),
        "bind_new": _payload(conversation={"key": "k1", "on_missing": "fail"}),
    }[scenario]
    kw = {"thread_url": "https://chatgpt.com/c/new"} if scenario == "bind_new" else {}
    driver = MockPageDriver(tokens=["a", "b", "c"], response_text="abc", **kw)
    events = list(_engine().iter_events(payload, driver=driver))
    assert _strip(events) == GOLDEN[scenario]


def test_flag_on_matches_flag_off_apart_from_the_submit_marker(monkeypatch):
    def run(flag):
        d = MockPageDriver(tokens=["a", "b", "c"], response_text="abc", thread_url="https://x/c/1")
        payload = _payload(conversation={"key": "k1", "on_missing": "fail"})
        return _run(d, monkeypatch, flag=flag, payload=payload)

    off, on = run(False), run(True)
    assert _types(on) == _types(off)[:6] + ["prompt_submitted"] + _types(off)[6:]
    assert [(e["type"], e["data"]) for e in on if e["type"] != "prompt_submitted"] == [
        (e["type"], e["data"]) for e in off
    ]
    assert [e["sequence"] for e in on] == list(range(1, len(on) + 1))


def test_token_events_are_bounded_and_the_tail_is_coalesced(monkeypatch):
    monkeypatch.setenv("UBAG_WORKER_STREAM_MAX_TOKEN_EVENTS", "3")
    tokens = ["t%d" % i for i in range(10)]
    events = _run(_Driver(tokens=tokens, response_text="".join(tokens)), monkeypatch)
    live = [e for e in events if e["type"] == "token"]
    assert len(live) == 4 and [e["data"]["token_index"] for e in live] == [0, 1, 2, 3]
    assert "".join(e["data"]["delta"]["text"] for e in live) == "".join(tokens)
    assert events[-1]["data"]["metadata"]["token_count"] == 10  # deltas, not events


def test_default_token_budget_stays_under_the_gateway_event_cap():
    assert 0 < _stream_token_budget() < 512


class _Out(io.StringIO):
    """Records how many driver calls had happened when each line was flushed."""

    def __init__(self, driver):
        super().__init__()
        self.driver = driver
        self.lines = []

    def write(self, text):
        if text.strip():
            self.lines.append((json.loads(text), list(self.driver.calls)))
        return super().write(text)


def test_job_end_not_the_terminal_event_closes_the_stream(monkeypatch):
    monkeypatch.setenv(FLAG, "1")
    d = _Driver(tokens=["a", "b"], response_text="ab", thread_url="https://chatgpt.com/c/1")
    daemon = WarmWorkerDaemon(driver_factory=lambda options: d)
    request = {"job_id": "job_g", "payload": _payload(conversation={"key": "k1", "on_missing": "fail"})}
    out = _Out(d)

    serve(io.StringIO(json.dumps(request) + "\n"), out, daemon)

    types = [line.get("type", JOB_END if JOB_END in line else "?") for line, _ in out.lines]
    assert types[-3:] == ["completed", "conversation.thread_bound", JOB_END]
    first_token_calls = next(calls for line, calls in out.lines if line.get("type") == "token")
    assert "read_final_response" not in first_token_calls  # flushed to the pipe live


@pytest.mark.parametrize("flag,expected", [(True, True), (False, False)])
def test_hello_advertises_stream_events_only_when_enabled(monkeypatch, flag, expected):
    if flag:
        monkeypatch.setenv(FLAG, "1")
    else:
        monkeypatch.delenv(FLAG, raising=False)
    out = io.StringIO()
    hello = json.dumps({"__ubag_control__": "hello", "proto": 2})

    serve(io.StringIO(hello + "\n"), out, WarmWorkerDaemon())

    assert ("stream_events" in json.loads(out.getvalue())["features"]) is expected

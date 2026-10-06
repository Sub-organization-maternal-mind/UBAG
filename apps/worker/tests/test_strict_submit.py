"""No blind resubmit: prompt_submitted marker + post-submit terminal failure (D4)."""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "apps" / "worker"))

from ubag_worker.live import LiveSessionEngine, MockPageDriver  # noqa: E402
from ubag_worker.live.selectors import get_provider_selectors  # noqa: E402

FLAG = "UBAG_WORKER_STRICT_SUBMIT"


class _Driver(MockPageDriver):
    """Counts submits; can fail submit N times and/or the stream after submit."""

    def __init__(self, *, fail_submits=0, stream_raises=False, **kw):
        super().__init__(**kw)
        self.submit_calls = 0
        self._fail_submits = fail_submits
        self._stream_raises = stream_raises

    def submit_prompt(self, selectors, prompt):
        self.submit_calls += 1
        if self.submit_calls <= self._fail_submits:
            raise RuntimeError("cdp hiccup before submit landed")
        super().submit_prompt(selectors, prompt)

    def stream_response(self, selectors, *, timeout_s):
        if self._stream_raises:
            yield "par"
            raise RuntimeError("secret page detail")
        yield from super().stream_response(selectors, timeout_s=timeout_s)


def _run(driver, monkeypatch, flag=True):
    if flag:
        monkeypatch.setenv(FLAG, "1")
    else:
        monkeypatch.delenv(FLAG, raising=False)
    payload = {
        "api_version": "v1", "job_id": "job_1", "trace_id": "trace_1",
        "target": "chatgpt_web", "prompt": "hi",
        "context": {"tenant_id": "t1"}, "options": {},
    }
    engine = LiveSessionEngine(get_provider_selectors("chatgpt_web"))
    return engine.run(payload, driver=driver)


def _types(events):
    return [e["type"] for e in events]


def test_post_submit_exception_is_terminal_and_submits_once(monkeypatch):
    d = _Driver(stream_raises=True, thread_url="https://chatgpt.com/c/abc")
    events = _run(d, monkeypatch)
    types = _types(events)
    assert d.submit_calls == 1
    assert types.count("prompt_submitted") == 1
    assert types.index("prompt_submitted") < types.index("failed_terminal")
    assert types[-1] == "failed_terminal"
    data = events[-1]["data"]
    assert data["submitted"] is True and data["retryable"] is False
    assert data["reconcile_required"] is True
    assert data["current_thread_url"] == "https://chatgpt.com/c/abc"
    assert "secret page detail" not in str(data)


def test_pre_submit_failures_still_retry(monkeypatch):
    d = _Driver(fail_submits=2)
    events = _run(d, monkeypatch)
    assert d.submit_calls == 3
    assert events[-1]["type"] == "completed"
    assert _types(events).count("prompt_submitted") == 1


def test_flag_off_unchanged(monkeypatch):
    # Off: no marker; the stream error retries the whole interaction (3 submits).
    d = _Driver(stream_raises=True)
    try:
        _run(d, monkeypatch, flag=False)
        raised = False
    except RuntimeError:
        raised = True
    assert raised and d.submit_calls == 3
    ok = _run(_Driver(), monkeypatch, flag=False)
    assert "prompt_submitted" not in _types(ok) and ok[-1]["type"] == "completed"

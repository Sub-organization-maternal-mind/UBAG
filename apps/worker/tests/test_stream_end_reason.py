"""Truncation guard: a deadline-cut stream is never reported completed (D4)."""
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "apps" / "worker"))

from ubag_worker.live import LiveSessionEngine, MockPageDriver  # noqa: E402
from ubag_worker.live.selectors import get_provider_selectors  # noqa: E402

FLAG = "UBAG_WORKER_STRICT_STREAM_END"


def _run(reason, monkeypatch, flag):
    if flag:
        monkeypatch.setenv(FLAG, "1")
    else:
        monkeypatch.delenv(FLAG, raising=False)
    sel = get_provider_selectors("chatgpt_web")
    payload = {
        "api_version": "v1", "job_id": "job_1", "trace_id": "trace_1",
        "target": "chatgpt_web", "prompt": "hi",
        "context": {"tenant_id": "t1"}, "options": {},
    }
    driver = MockPageDriver(response_text="full answer", tokens=["par", "tial"],
                            scripted_end_reason=reason)
    return LiveSessionEngine(sel).run(payload, driver=driver)


def test_settled_completes(monkeypatch):
    ev = _run("settled", monkeypatch, True)[-1]
    assert ev["type"] == "completed"
    assert ev["data"]["result"]["text"] == "full answer"


def test_deadline_times_out_with_partial(monkeypatch):
    ev = _run("deadline", monkeypatch, True)[-1]
    assert ev["type"] == "timed_out"
    d = ev["data"]
    assert d["stream_end_reason"] == "deadline" and d["submitted"] is True
    assert d["partial"] == {"text": "partial", "token_events": 2}
    assert d["partial_text_chars"] == 7
    assert "result" not in d


def test_flag_off_deadline_unchanged(monkeypatch):
    ev = _run("deadline", monkeypatch, False)[-1]
    assert ev["type"] == "completed"
    assert ev["data"]["result"]["text"] == "full answer"

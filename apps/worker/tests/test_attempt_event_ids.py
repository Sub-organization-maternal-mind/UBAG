from ubag_worker.live.events import worker_event


def _ev(attempt_id=""):
    return worker_event(api_version="v", job_id="job_1", trace_id="t", sequence=1,
                        event_type="queued", data={}, attempt_id=attempt_id)


def test_event_id_stable_within_attempt_and_differs_across():
    assert _ev("a1")["event_id"] == _ev("a1")["event_id"]
    assert _ev("a1")["event_id"] != _ev("a2")["event_id"]
    assert _ev("a1")["data"]["attempt_id"] == "a1"


def test_legacy_id_unchanged_without_attempt():
    ev = _ev()
    assert "attempt_id" not in ev["data"]
    assert ev["event_id"] != _ev("a1")["event_id"]

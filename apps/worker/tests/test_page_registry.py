"""The leaked-tab registry: a SIGKILLed worker must not leave tabs behind forever.

Tabs UBAG opens in the operator's shared browser are recorded by CDP target id;
the next process of the same role closes them over plain HTTP before attaching.
Only registered ids may ever be closed -- the operator's own tabs are untouchable.
"""
import json
import urllib.error

import pytest
from ubag_worker.live import page_driver


@pytest.fixture
def registry(tmp_path, monkeypatch):
    path = tmp_path / "open-pages.test.json"
    monkeypatch.setattr(page_driver, "_page_registry_path", lambda: str(path))
    page_driver._LIVE_TARGET_IDS.clear()
    return path


class _Response:
    def read(self):
        return b"Target is closing"


def test_registry_round_trip_and_graceful_close_unregisters(registry):
    page_driver._registry_update(add="AAAAAAAA1111")
    page_driver._registry_update(add="BBBBBBBB2222")
    assert json.loads(registry.read_text()) == ["AAAAAAAA1111", "BBBBBBBB2222"]

    page_driver._registry_update(remove="AAAAAAAA1111")
    assert page_driver._registry_read() == ["BBBBBBBB2222"]


def test_close_stale_pages_closes_only_registered_ids_then_forgets_them(registry):
    registry.write_text(json.dumps(["DEADBEEF0001", "DEADBEEF0002", "../evil"]))
    closed = []

    def opener(url, timeout):
        closed.append(url)
        return _Response()

    page_driver._close_stale_pages("http://172.28.0.10:9223/", opener=opener)

    assert closed == [
        "http://172.28.0.10:9223/json/close/DEADBEEF0001",
        "http://172.28.0.10:9223/json/close/DEADBEEF0002",
    ]
    assert page_driver._registry_read() == []


def test_close_stale_pages_skips_pages_live_in_this_process(registry):
    page_driver._registry_update(add="LIVE00000001")
    registry.write_text(json.dumps(["LIVE00000001", "STALE0000001"]))
    closed = []

    page_driver._close_stale_pages(
        "http://b:9223", opener=lambda url, timeout: closed.append(url) or _Response()
    )

    assert closed == ["http://b:9223/json/close/STALE0000001"]
    assert page_driver._registry_read() == ["LIVE00000001"]


def test_already_gone_tab_is_forgotten_but_unreachable_browser_keeps_registry(registry):
    registry.write_text(json.dumps(["GONE00000001", "KEEP00000001"]))

    def gone(url, timeout):
        raise urllib.error.HTTPError(url, 404, "No such target id", None, None)

    page_driver._close_stale_pages("http://b:9223", opener=gone)
    assert page_driver._registry_read() == []

    registry.write_text(json.dumps(["KEEP00000001"]))

    def unreachable(url, timeout):
        raise ConnectionRefusedError()

    page_driver._close_stale_pages("http://b:9223", opener=unreachable)
    assert page_driver._registry_read() == ["KEEP00000001"]


def test_no_registry_file_means_no_http_calls(registry):
    page_driver._close_stale_pages(
        "http://b:9223", opener=lambda *a, **k: pytest.fail("must not call the browser")
    )

"""Tests for the session recording module."""
import pytest

from ubag_worker.live.recording import (
    ConsoleLog,
    DomSnapshot,
    HarEntry,
    RecordingSession,
    finish_session,
    new_session,
    record_console_log,
    record_dom_snapshot,
    record_har_entry,
    record_screenshot,
    to_summary,
)


class TestRecordingSession:
    def test_new_session_has_job_id_and_timestamp(self):
        s = new_session("job-abc")
        assert s.job_id == "job-abc"
        assert s.started_at != ""
        assert s.ended_at == ""

    def test_finish_session_sets_ended_at(self):
        s = new_session("job-1")
        finish_session(s)
        assert s.ended_at != ""


class TestHarEntry:
    def test_sensitive_headers_redacted(self):
        entry = HarEntry(
            url="https://example.com",
            request_headers={"Authorization": "Bearer token", "Content-Type": "application/json"},
            response_headers={"Set-Cookie": "session=abc", "Content-Type": "text/html"},
        )
        req = entry.sanitized_request_headers()
        assert req["Authorization"] == "[redacted]"
        assert req["Content-Type"] == "application/json"

        resp = entry.sanitized_response_headers()
        assert resp["Set-Cookie"] == "[redacted]"
        assert resp["Content-Type"] == "text/html"

    def test_cookie_header_redacted(self):
        entry = HarEntry(url="https://x.com", request_headers={"cookie": "sid=xyz"})
        assert entry.sanitized_request_headers()["cookie"] == "[redacted]"


class TestRecordHelpers:
    def test_record_har_entry(self):
        s = new_session("j1")
        entry = HarEntry(url="https://chatgpt.com/api/submit", method="POST", status=200)
        record_har_entry(s, entry)
        assert len(s.har_entries) == 1
        assert s.har_entries[0].url == "https://chatgpt.com/api/submit"

    def test_record_dom_snapshot(self):
        s = new_session("j2")
        snap = record_dom_snapshot(s, url="https://chatgpt.com/", title="ChatGPT",
                                   selector_counts={"textarea": 1, "button": 3})
        assert isinstance(snap, DomSnapshot)
        assert len(s.dom_snapshots) == 1
        assert s.dom_snapshots[0].selector_counts["button"] == 3
        assert snap.timestamp != ""

    def test_record_console_log(self):
        s = new_session("j3")
        log = record_console_log(s, "warn", "Selector drift detected")
        assert isinstance(log, ConsoleLog)
        assert len(s.console_logs) == 1
        assert s.console_logs[0].level == "warn"

    def test_record_screenshot(self):
        s = new_session("j4")
        record_screenshot(s, "s3://ubag-recordings/job4/screen1.png")
        assert len(s.screenshots) == 1


class TestToSummary:
    def test_summary_structure(self):
        s = new_session("j5")
        record_har_entry(s, HarEntry(url="https://example.com", method="GET", status=200))
        record_dom_snapshot(s, url="https://example.com")
        record_console_log(s, "log", "page loaded")
        record_screenshot(s, "screen.png")
        finish_session(s)

        summary = to_summary(s)
        assert summary["job_id"] == "j5"
        assert summary["har_entry_count"] == 1
        assert summary["dom_snapshot_count"] == 1
        assert summary["console_log_count"] == 1
        assert summary["screenshot_count"] == 1
        assert summary["ended_at"] != ""

    def test_summary_redacts_sensitive_headers(self):
        s = new_session("j6")
        entry = HarEntry(
            url="https://example.com",
            request_headers={"Authorization": "Bearer secret", "Content-Type": "text/plain"},
        )
        record_har_entry(s, entry)
        summary = to_summary(s)
        har = summary["har_entries"][0]
        assert har["request_headers"]["Authorization"] == "[redacted]"
        assert har["request_headers"]["Content-Type"] == "text/plain"

    def test_summary_is_json_serializable(self):
        import json
        s = new_session("j7")
        record_har_entry(s, HarEntry(url="https://x.com", status=200, duration_ms=123.5))
        json.dumps(to_summary(s))  # must not raise

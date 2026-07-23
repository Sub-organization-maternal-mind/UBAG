"""Tests for the generalized multi-file attachment phase of the live engine.

A job carrying ``input.attachments`` (documents/images/audio/video/voice) plus
the gateway-materialized ``input.attachment_local_paths`` must attach every local
file to the provider's file input in one operation, before submitting the prompt,
and emit a single ``file.attached`` event listing the declared keys. Targets
without a ``file_input`` selector are blocked with
``attachment_not_supported_by_target``. A bad attachment key (path separators) is
rejected outright.
"""

import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.live import (  # noqa: E402
    LiveSessionEngine,
    LiveSessionError,
    MockPageDriver,
)
from ubag_worker.live.selectors import get_provider_selectors  # noqa: E402

_MANUAL_CONTEXT = {
    "account_binding_id": "acct_live_123",
    "consent_ref": "consent_live_123",
    "automation_scope": ["manual_login", "submit_prompt", "read_response"],
}


def _attachments_payload(target, attachments, local_paths, *, prompt="Summarize."):
    return {
        "api_version": "2026-05-22",
        "job_id": "job_attach_%s" % target,
        "trace_id": "trace_attach_%s" % target,
        "job": {
            "target": target,
            "command_type": "chat.prompt",
            "input": {
                "prompt": prompt,
                "attachments": attachments,
                "attachment_local_paths": local_paths,
            },
            "context": dict(_MANUAL_CONTEXT),
        },
    }


def _types(events):
    return [event["type"] for event in events]


class MultiFileAttachTests(unittest.TestCase):
    def test_attaches_all_files_then_submits(self):
        selectors = get_provider_selectors("gemini_web")
        driver = MockPageDriver(response_text="done")
        payload = _attachments_payload(
            "gemini_web",
            attachments=[
                {"key": "report.pdf", "content_type": "application/pdf", "kind": "document"},
                {"key": "note.webm", "content_type": "audio/webm", "kind": "voice"},
            ],
            local_paths=["/tmp/ubag/report.pdf", "/tmp/ubag/note.webm"],
        )
        events = LiveSessionEngine(selectors).run(payload, driver=driver)
        types = _types(events)

        self.assertEqual(driver.attached_files, ["/tmp/ubag/report.pdf", "/tmp/ubag/note.webm"])
        self.assertIn("file.attached", types)
        self.assertLess(types.index("file.attached"), types.index("completed"))
        attached = next(e for e in events if e["type"] == "file.attached")
        self.assertEqual(attached["data"]["artifact_keys"], ["report.pdf", "note.webm"])
        self.assertEqual(attached["data"]["count"], 2)

    def test_target_without_file_input_is_blocked(self):
        selectors = get_provider_selectors("generic_live_web")
        driver = MockPageDriver()
        payload = _attachments_payload(
            "generic_live_web",
            attachments=[{"key": "a.pdf", "content_type": "application/pdf", "kind": "document"}],
            local_paths=["/tmp/ubag/a.pdf"],
        )
        events = LiveSessionEngine(selectors).run(payload, driver=driver)
        blocked = [e for e in events if e["type"] == "blocked"]

        self.assertTrue(blocked)
        self.assertEqual(blocked[0]["data"]["reason"], "attachment_not_supported_by_target")
        self.assertEqual(driver.attached_files, [])

    def test_attachment_key_with_separator_is_rejected(self):
        selectors = get_provider_selectors("gemini_web")
        payload = _attachments_payload(
            "gemini_web",
            attachments=[{"key": "sub/a.pdf", "content_type": "application/pdf", "kind": "document"}],
            local_paths=["/tmp/ubag/a.pdf"],
        )
        with self.assertRaises(LiveSessionError):
            LiveSessionEngine(selectors).run(payload, driver=MockPageDriver())

    def test_gemini_uses_upload_menu_trigger(self):
        # Gemini injects its <input type=file> only via the "Upload & tools" ->
        # "Upload files" menu path (verified live 2026-07-23), so it declares a
        # file_attach_trigger that the driver walks before setting files.
        selectors = get_provider_selectors("gemini_web")
        self.assertGreaterEqual(len(selectors.file_attach_trigger), 2)
        driver = MockPageDriver(response_text="ok")
        payload = _attachments_payload(
            "gemini_web",
            attachments=[{"key": "a.pdf", "content_type": "application/pdf", "kind": "document"}],
            local_paths=["/tmp/ubag/a.pdf"],
        )
        events = LiveSessionEngine(selectors).run(payload, driver=driver)
        self.assertTrue(driver.used_attach_trigger)
        self.assertEqual(driver.attached_files, ["/tmp/ubag/a.pdf"])
        self.assertIn("file.attached", _types(events))

    def test_gemini_upload_trigger_drift_blocks(self):
        selectors = get_provider_selectors("gemini_web")
        driver = MockPageDriver(drift_group="upload_files_item")
        payload = _attachments_payload(
            "gemini_web",
            attachments=[{"key": "a.pdf", "content_type": "application/pdf", "kind": "document"}],
            local_paths=["/tmp/ubag/a.pdf"],
        )
        events = LiveSessionEngine(selectors).run(payload, driver=driver)
        blocked = [e for e in events if e["type"] == "blocked"]
        self.assertTrue(blocked)
        self.assertEqual(blocked[0]["data"]["reason"], "selector_drift_detected")
        self.assertEqual(blocked[0]["data"]["selector_group"], "upload_files_item")

    def test_chatgpt_and_deepseek_have_no_trigger(self):
        # Verified live 2026-07-23: both render the file input at rest, so they must
        # NOT declare a trigger (the driver would otherwise click a phantom menu).
        for target in ("chatgpt_web", "deepseek_web"):
            selectors = get_provider_selectors(target)
            self.assertEqual(len(selectors.file_attach_trigger), 0, target)

    def test_missing_local_paths_blocks(self):
        selectors = get_provider_selectors("gemini_web")
        driver = MockPageDriver()
        payload = _attachments_payload(
            "gemini_web",
            attachments=[{"key": "a.pdf", "content_type": "application/pdf", "kind": "document"}],
            local_paths=[],
        )
        events = LiveSessionEngine(selectors).run(payload, driver=driver)
        blocked = [e for e in events if e["type"] == "blocked"]

        self.assertTrue(blocked)
        self.assertEqual(blocked[0]["data"]["reason"], "attachment_not_supported_by_target")


if __name__ == "__main__":
    unittest.main()

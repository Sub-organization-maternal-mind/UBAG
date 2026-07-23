"""Tests for the optional audio-attach phase of the live engine.

A job carrying ``input.audio_artifact_key`` + ``input.audio_local_path`` must,
before submitting the prompt, attach the local file to the provider's file input
(verified via ``MockPageDriver.attached_files`` + a ``file.attached`` event).
Text-only jobs must be byte-for-byte unchanged (no attach). Targets without a
``file_input`` selector, or jobs without a materialized local path, must be
blocked with ``audio_not_supported_by_target`` rather than silently transcribing
nothing. A bad artifact key (path separators) is rejected outright.
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


def _audio_payload(
    target,
    *,
    key="dictation.webm",
    local_path="/tmp/ubag/dictation.webm",
    prompt="Transcribe the attached audio verbatim.",
):
    job_input = {"prompt": prompt}
    if key is not None:
        job_input["audio_artifact_key"] = key
    if local_path is not None:
        job_input["audio_local_path"] = local_path
    return {
        "api_version": "2026-05-22",
        "job_id": "job_audio_%s" % target,
        "trace_id": "trace_audio_%s" % target,
        "job": {
            "target": target,
            "command_type": "medical_transcription",
            "input": job_input,
            "context": dict(_MANUAL_CONTEXT),
        },
    }


def _types(events):
    return [event["type"] for event in events]


class AudioAttachTests(unittest.TestCase):
    def test_attaches_audio_then_submits_and_completes(self):
        selectors = get_provider_selectors("gemini_web")
        driver = MockPageDriver(response_text="lungs are clear")
        events = LiveSessionEngine(selectors).run(_audio_payload("gemini_web"), driver=driver)
        types = _types(events)

        self.assertIn("file.attached", types)
        self.assertEqual(driver.attached_files, ["/tmp/ubag/dictation.webm"])
        self.assertEqual(driver.submitted_prompt, "Transcribe the attached audio verbatim.")
        self.assertIn("completed", types)
        # attach precedes prompt submission / completion
        self.assertLess(types.index("file.attached"), types.index("completed"))
        completed = next(e for e in events if e["type"] == "completed")
        self.assertEqual(completed["data"]["result"]["text"], "lungs are clear")

    def test_text_only_job_does_not_attach(self):
        selectors = get_provider_selectors("gemini_web")
        driver = MockPageDriver(response_text="ready")
        payload = _audio_payload("gemini_web", key=None, local_path=None)
        events = LiveSessionEngine(selectors).run(payload, driver=driver)

        self.assertEqual(driver.attached_files, [])
        self.assertNotIn("file.attached", _types(events))
        self.assertIn("completed", _types(events))

    def test_target_without_file_input_is_blocked(self):
        # generic_live_web ships with no file_input selector configured.
        selectors = get_provider_selectors("generic_live_web")
        driver = MockPageDriver()
        events = LiveSessionEngine(selectors).run(
            _audio_payload("generic_live_web"), driver=driver
        )
        blocked = [e for e in events if e["type"] == "blocked"]

        self.assertTrue(blocked)
        self.assertEqual(blocked[0]["data"]["reason"], "attachment_not_supported_by_target")
        self.assertEqual(driver.attached_files, [])

    def test_missing_local_path_is_blocked(self):
        selectors = get_provider_selectors("gemini_web")
        driver = MockPageDriver()
        payload = _audio_payload("gemini_web", local_path=None)
        events = LiveSessionEngine(selectors).run(payload, driver=driver)
        blocked = [e for e in events if e["type"] == "blocked"]

        self.assertTrue(blocked)
        self.assertEqual(blocked[0]["data"]["reason"], "attachment_not_supported_by_target")

    def test_artifact_key_with_separator_is_rejected(self):
        selectors = get_provider_selectors("gemini_web")
        payload = _audio_payload("gemini_web", key="audio/dictation.webm")
        with self.assertRaises(LiveSessionError):
            LiveSessionEngine(selectors).run(payload, driver=MockPageDriver())

    def test_file_input_drift_blocks_with_drift_reason(self):
        selectors = get_provider_selectors("gemini_web")
        driver = MockPageDriver(drift_group="file_input")
        events = LiveSessionEngine(selectors).run(_audio_payload("gemini_web"), driver=driver)
        blocked = [e for e in events if e["type"] == "blocked"]

        self.assertTrue(blocked)
        self.assertEqual(blocked[0]["data"]["reason"], "selector_drift_detected")
        self.assertEqual(blocked[0]["data"]["selector_group"], "file_input")


if __name__ == "__main__":
    unittest.main()

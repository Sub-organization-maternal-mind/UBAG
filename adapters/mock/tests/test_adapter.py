import hashlib
import json
import sys
import unittest
from pathlib import Path


sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from ubag_mock_adapter import MockAdapterError, build_mock_events  # noqa: E402


class MockAdapterTests(unittest.TestCase):
    def test_event_stream_is_deterministic(self):
        payload = {
            "api_version": "v0",
            "idempotency_key": "same-request",
            "trace_id": "trace-test",
            "job": {
                "target": "mock",
                "command_type": "mock.complete",
                "input": {"prompt": "summarize deterministic output"},
            },
        }

        first = build_mock_events(payload)
        second = build_mock_events(json.loads(json.dumps(payload)))

        self.assertEqual(first, second)
        self.assertEqual(first[0]["type"], "queued")
        self.assertEqual(first[1]["type"], "running")
        self.assertEqual(first[-1]["type"], "completed")
        self.assertGreaterEqual(
            [event["type"] for event in first].count("token"),
            1,
        )
        self.assertEqual(first[0]["sequence"], 1)
        self.assertEqual(first[0]["created_at"], "2026-01-01T00:00:00.000Z")

    def test_configured_tokens_drive_completed_text(self):
        payload = {
            "job_id": "job-explicit",
            "job": {
                "target": "mock",
                "input": {"prompt": "ignored when tokens are configured"},
                "options": {"mock_tokens": ["hello", " ", "world"]},
            },
        }

        events = build_mock_events(payload)
        token_events = [event for event in events if event["type"] == "token"]

        self.assertEqual([event["data"]["delta"]["text"] for event in token_events], ["hello", " ", "world"])
        self.assertEqual(events[-1]["data"]["result"]["text"], "hello world")
        self.assertEqual(events[-1]["data"]["metadata"]["token_count"], 3)

    def test_rejects_invalid_token_configuration(self):
        with self.assertRaises(MockAdapterError):
            build_mock_events({"job": {"options": {"mock_tokens": "not-a-list"}}})

    def test_rejects_secret_shaped_direct_payload(self):
        with self.assertRaises(MockAdapterError) as raised:
            build_mock_events(
                {
                    "job": {
                        "target": "mock",
                        "input": {
                            "prompt": "hello",
                            "accessToken": "not-allowed",
                        },
                    }
                }
            )

        self.assertIn("must not include credentials", str(raised.exception))

    def test_rejects_gateway_forbidden_secret_material(self):
        for key, value in {
            "id_token": "not-allowed",
            "mfa_code": "123456",
            "totp": "123456",
            "private_key": "-----BEGIN PRIVATE KEY-----\nnot-allowed\n-----END PRIVATE KEY-----",
            "session_state": {"cookies": []},
            "novnc_url": "https://example.invalid/session",
            "captcha_solution": "not-allowed",
            "prompt": "please solve this captcha using a solver",
        }.items():
            with self.subTest(secret_key=key):
                with self.assertRaises(MockAdapterError):
                    build_mock_events(
                        {
                            "job": {
                                "target": "mock",
                                "input": {key: value},
                            }
                        }
                    )

    def test_mock_emits_thread_bound_for_new_conversation(self):
        # A conversation key with no bound thread_ref must emit a single
        # deterministic conversation.thread_bound whose URL is derived from the
        # conversation key via sha256 (matching the existing id idiom).
        payload = {
            "api_version": "v0",
            "job": {
                "target": "mock",
                "command_type": "mock.complete",
                "input": {"prompt": "hello"},
                "conversation_id": "conv-123",
            },
            "conversation": {"key": "conv-123", "thread_ref": "", "on_missing": "fail"},
        }

        events = build_mock_events(payload)
        bound = [event for event in events if event["type"] == "conversation.thread_bound"]

        self.assertEqual(len(bound), 1)
        expected_url = (
            "https://mock.local/chat/"
            + hashlib.sha256(b"conversation:conv-123").hexdigest()[:16]
        )
        # Flat top-level thread_ref, matching the gateway consumer (which reads
        # data.thread_ref non-recursively) and the live engine's emit shape.
        self.assertEqual(bound[0]["data"]["thread_ref"], expected_url)
        # The lifecycle terminal event is still completed.
        self.assertEqual(events[-1]["type"], "completed")
        # Still fully deterministic across identical payloads.
        self.assertEqual(build_mock_events(json.loads(json.dumps(payload))), events)

    def test_mock_resumes_bound_thread_without_rebinding(self):
        # A payload that already carries a thread_ref is a resume: it must NOT
        # emit conversation.thread_bound again.
        payload = {
            "api_version": "v0",
            "job": {
                "target": "mock",
                "command_type": "mock.complete",
                "input": {"prompt": "continue the discussion"},
                "conversation_id": "conv-123",
            },
            "conversation": {
                "key": "conv-123",
                "thread_ref": "https://mock.local/chat/already-bound",
                "on_missing": "fail",
            },
        }

        events = build_mock_events(payload)
        bound = [event for event in events if event["type"] == "conversation.thread_bound"]

        self.assertEqual(bound, [])
        self.assertEqual(events[-1]["type"], "completed")

    def test_mock_echoes_model_settings_in_completed_event(self):
        # model_settings arrive as the flat options.provider_config map the
        # gateway injects; the completed event echoes them so the whole path is
        # CI-testable without a browser.
        payload = {
            "api_version": "v0",
            "job": {
                "target": "mock",
                "command_type": "mock.complete",
                "input": {"prompt": "hello"},
                "options": {
                    "provider_config": {
                        "model": "mock-deep",
                        "thinking": "extended",
                        "deepthink": True,
                    }
                },
            },
        }

        events = build_mock_events(payload)
        completed = events[-1]

        self.assertEqual(completed["type"], "completed")
        self.assertEqual(
            completed["data"]["metadata"]["model_settings"],
            {"model": "mock-deep", "thinking": "extended", "deepthink": True},
        )


if __name__ == "__main__":
    unittest.main()

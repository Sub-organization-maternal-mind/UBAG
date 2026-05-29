import unittest
from pathlib import Path


import sys


REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.adapter_registry import (  # noqa: E402
    REQUIRED_ADAPTER_IDS,
    events_for_payload,
    instantiate_adapter,
    load_manifest_index,
    load_manifests,
    load_registry,
    resolve_manifest_for_payload,
)


class AdapterRegistryTests(unittest.TestCase):
    def test_registry_contains_required_safe_mode_manifests(self):
        registry = load_registry()
        manifests = load_manifests()

        self.assertEqual(registry["kind"], "mock_registry")
        self.assertEqual(set(REQUIRED_ADAPTER_IDS), set(manifests))

        for adapter_id, manifest in manifests.items():
            safe_mode = manifest["safe_mode"]
            credential_policy = manifest["credential_policy"]
            captcha_policy = manifest["captcha_policy"]
            artifact_policy = manifest["artifact_policy"]

            self.assertTrue(safe_mode["user_owned_sessions_only"])
            self.assertEqual(safe_mode["automated_login"], "forbidden")
            self.assertEqual(safe_mode["credential_scraping"], "forbidden")
            self.assertEqual(safe_mode["credential_storage"], "forbidden")
            self.assertEqual(safe_mode["captcha_solving"], "forbidden")
            self.assertEqual(safe_mode["captcha_bypass"], "forbidden")
            self.assertFalse(credential_policy["collect_credentials"])
            self.assertFalse(credential_policy["read_credentials_from_payload"])
            self.assertFalse(credential_policy["scrape_credentials"])
            self.assertFalse(credential_policy["store_credentials"])
            self.assertFalse(captcha_policy["solve"])
            self.assertFalse(captcha_policy["bypass"])
            self.assertFalse(captcha_policy["delegate_to_solver"])
            self.assertTrue(captcha_policy["manual_only"])

            if adapter_id != "mock":
                self.assertEqual(manifest["status"], "stub")
                self.assertTrue(safe_mode["manual_login_required"])
                self.assertEqual(artifact_policy["screenshots"], "on_failure_only")
                self.assertEqual(artifact_policy["dom_snapshots"], "drift_baseline_only")
                self.assertEqual(artifact_policy["recordings"], "post_login_debug_only")
                self.assertEqual(
                    manifest["resource_policy"]["browser"],
                    "manual_user_owned_session_required",
                )
            else:
                self.assertEqual(artifact_policy["screenshots"], "disabled")
                self.assertEqual(artifact_policy["dom_snapshots"], "disabled")
                self.assertEqual(artifact_policy["recordings"], "disabled")

    def test_aliases_resolve_to_canonical_manifests(self):
        index = load_manifest_index()

        self.assertEqual(index["deepseek"]["id"], "deepseek_web")
        self.assertEqual(index["chatgpt"]["id"], "chatgpt_web")
        self.assertEqual(index["claude"]["id"], "claude_web")
        self.assertEqual(index["gemini"]["id"], "gemini_web")
        self.assertEqual(index["mistral"]["id"], "mistral_lechat")
        self.assertEqual(index["mistral_web"]["id"], "mistral_lechat")
        self.assertEqual(index["perplexity"]["id"], "perplexity_web")
        self.assertEqual(index["generic_chat"]["id"], "generic_chat")
        self.assertEqual(index["generic_form"]["id"], "generic_form")

    def test_mock_target_still_emits_events(self):
        events = events_for_payload(
            {
                "job_id": "job-registry-mock",
                "job": {
                    "target": "mock",
                    "input": {"prompt": "registry smoke"},
                    "options": {"mock_tokens": ["ok"]},
                },
            }
        )

        self.assertEqual(events[0]["type"], "queued")
        self.assertEqual(events[-1]["type"], "completed")
        self.assertEqual(events[-1]["data"]["result"]["text"], "ok")

    def test_stub_targets_fail_closed_without_manual_session_runtime(self):
        manifests = load_manifests()

        for adapter_id, manifest in manifests.items():
            if adapter_id == "mock":
                continue
            adapter = instantiate_adapter(manifest)
            with self.subTest(adapter=adapter_id):
                with self.assertRaises(NotImplementedError) as raised:
                    adapter.run({"job": {"target": adapter_id, "input": {"prompt": "hello"}}})
                message = str(raised.exception)
                self.assertIn("safe-mode scaffold", message)
                self.assertIn("user-owned manual browser session", message)
                self.assertIn("will not collect credentials", message)
                self.assertIn("solve CAPTCHA", message)

    def test_worker_dispatch_requires_manual_session_context_for_stub_alias(self):
        with self.assertRaises(ValueError) as raised:
            events_for_payload({"job": {"target": "deepseek", "input": {"prompt": "hello"}}})

        self.assertIn("requires user-owned manual session context fields", str(raised.exception))

    def test_worker_dispatch_emits_manual_session_events_after_context(self):
        events = events_for_payload(
            {
                "tenant_id": "tenant_123",
                "account_binding_id": "acct_123",
                "consent_ref": "consent_123",
                "automation_scope": ["submit_prompt", "read_response"],
                "job": {"target": "deepseek", "input": {"prompt": "hello"}},
            }
        )

        self.assertEqual(events[0]["type"], "queued")
        self.assertEqual(events[1]["type"], "session.manual_action_required")
        self.assertEqual(events[1]["data"]["account_binding_id"], "acct_123")
        self.assertEqual(events[1]["data"]["consent_ref"], "consent_123")
        self.assertEqual(events[1]["data"]["novnc_url"], "http://127.0.0.1:7900/session/%s" % events[1]["data"]["session_id"])
        self.assertEqual(events[2]["type"], "blocked")

    def test_worker_dispatch_accepts_gateway_envelope_manual_context(self):
        events = events_for_payload(
            {
                "api_version": "2026-05-22",
                "job_id": "job-gateway-manual",
                "tenant_id": "tenant_123",
                "trace_id": "trace_gateway_manual",
                "job": {
                    "target": "deepseek",
                    "input": {"prompt": "hello"},
                    "context": {
                        "account_binding_id": "acct_123",
                        "consent_ref": "consent_123",
                        "automation_scope": ["submit_prompt", "read_response"],
                    },
                },
            }
        )

        self.assertEqual(events[1]["type"], "session.manual_action_required")
        self.assertEqual(events[1]["job_id"], "job-gateway-manual")
        self.assertEqual(events[1]["trace_id"], "trace_gateway_manual")
        self.assertEqual(events[1]["data"]["account_binding_id"], "acct_123")
        self.assertEqual(events[1]["data"]["novnc_url"], "http://127.0.0.1:7900/session/%s" % events[1]["data"]["session_id"])

    def test_worker_dispatch_accepts_nested_gateway_manual_session_context(self):
        events = events_for_payload(
            {
                "api_version": "2026-05-22",
                "job_id": "job-gateway-nested-manual",
                "tenant_id": "tenant_123",
                "job": {
                    "target": "mistral_web",
                    "input": {"prompt": "hello"},
                    "context": {
                        "manual_session": {
                            "account_binding_id": "acct_nested",
                            "consent_ref": "consent_nested",
                            "automation_scope": ["submit_prompt", "read_response"],
                        }
                    },
                },
            }
        )

        self.assertEqual(events[1]["type"], "session.manual_action_required")
        self.assertEqual(events[1]["data"]["account_binding_id"], "acct_nested")
        self.assertEqual(events[1]["data"]["consent_ref"], "consent_nested")

    def test_manual_session_id_rejects_path_confusing_values(self):
        for raw_session_id in [".", "..", "../x", "http://evil", "has/slash", "has?query", "has#fragment", "has%escape"]:
            with self.subTest(session_id=raw_session_id):
                events = events_for_payload(
                    {
                        "tenant_id": "tenant_123",
                        "account_binding_id": "acct_123",
                        "consent_ref": "consent_123",
                        "automation_scope": ["submit_prompt", "read_response"],
                        "job": {"target": "deepseek", "input": {"prompt": "hello"}, "context": {"session_id": raw_session_id}},
                    }
                )
                self.assertTrue(events[1]["data"]["session_id"].startswith("sess_"))

    def test_single_user_edge_mode_uses_structured_local_manual_context(self):
        import os

        previous = os.environ.get("UBAG_WORKER_SINGLE_USER_EDGE")
        os.environ["UBAG_WORKER_SINGLE_USER_EDGE"] = "true"
        try:
            events = events_for_payload({"job": {"target": "deepseek", "input": {"prompt": "hello"}}})
        finally:
            if previous is None:
                os.environ.pop("UBAG_WORKER_SINGLE_USER_EDGE", None)
            else:
                os.environ["UBAG_WORKER_SINGLE_USER_EDGE"] = previous

        self.assertEqual(events[1]["type"], "session.manual_action_required")
        self.assertEqual(events[1]["data"]["account_binding_id"], "single_user_edge")
        self.assertEqual(events[1]["data"]["consent_ref"], "single_user_edge_local_consent")
        self.assertEqual(events[1]["data"]["automation_scope"], ["manual_login", "submit_prompt", "read_response"])

    def test_worker_rejects_secret_material_for_every_target(self):
        with self.assertRaises(ValueError) as raised:
            events_for_payload(
                {
                    "job": {
                        "target": "chatgpt",
                        "input": {"prompt": "hello"},
                        "credentials": {"password": "not-allowed"},
                    }
                }
            )

        self.assertIn("must not include credentials", str(raised.exception))

        with self.assertRaises(ValueError) as mock_raised:
            events_for_payload(
                {
                    "job": {
                        "target": "mock",
                        "input": {"credentials": {"password": "not-allowed"}},
                    }
                }
        )

        self.assertIn("must not include credentials", str(mock_raised.exception))

        with self.assertRaises(ValueError) as camel_raised:
            events_for_payload(
                {
                    "job": {
                        "target": "mock",
                        "input": {"prompt": "hello", "accessToken": "not-allowed"},
                    }
                }
            )

        self.assertIn("must not include credentials", str(camel_raised.exception))

        for key, value in {
            "token": "not-allowed",
            "password_value": "not-allowed",
            "apiKeyValue": "not-allowed",
            "client_secret_value": "not-allowed",
            "cookie_header": "not-allowed",
            "sessionTokenValue": "not-allowed",
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
                with self.assertRaises(ValueError):
                    events_for_payload(
                        {
                            "tenant_id": "tenant_123",
                            "account_binding_id": "acct_123",
                            "consent_ref": "consent_123",
                            "automation_scope": ["submit_prompt", "read_response"],
                            "job": {
                                "target": "deepseek",
                                "input": {key: value},
                            },
                        }
                    )

    def test_worker_secret_policy_allows_manual_session_reference_shape(self):
        events = events_for_payload(
            {
                "tenant_id": "tenant_123",
                "job": {
                    "target": "deepseek",
                    "input": {"prompt": "hello"},
                    "context": {
                        "manual_session": {
                            "account_binding_id": "acct_123",
                            "consent_ref": "consent_123",
                            "automation_scope": ["manual_login", "submit_prompt"],
                            "session_id": "sess_operator_attached",
                        }
                    },
                },
            }
        )

        self.assertEqual(events[1]["type"], "session.manual_action_required")
        self.assertEqual(events[1]["data"]["session_id"], "sess_operator_attached")

    def test_unknown_target_has_no_implicit_fallback(self):
        with self.assertRaises(NotImplementedError):
            resolve_manifest_for_payload({"job": {"target": "not_registered"}})


if __name__ == "__main__":
    unittest.main()

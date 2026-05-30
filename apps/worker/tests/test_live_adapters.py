"""Unit tests for the live manual-session browser automation engine.

These tests exercise the real adapter logic deterministically using a mock page
driver - no browser, no network. They validate selector configuration, the
worker JSONL event protocol, the manual-login flow, drift detection, and the
no-credentials security invariant.
"""

import os
import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.adapter_registry import instantiate_adapter, load_manifests  # noqa: E402
from ubag_worker.live import (  # noqa: E402
    DriftDetectedError,
    LiveSessionEngine,
    LiveSessionError,
    MockPageDriver,
    PlaywrightPageDriver,
    create_default_driver,
    offline_mode_enabled,
)
from ubag_worker.live.page_driver import AUTHENTICATED, LOGIN_REQUIRED  # noqa: E402
from ubag_worker.live.selectors import (  # noqa: E402
    PROVIDER_SELECTORS,
    SelectorGroup,
    get_provider_selectors,
)

_LIVE_PROVIDERS = (
    "chatgpt_web",
    "claude_web",
    "deepseek_web",
    "gemini_web",
    "mistral_lechat",
    "perplexity_web",
)

_MANUAL_CONTEXT = {
    "account_binding_id": "acct_live_123",
    "consent_ref": "consent_live_123",
    "automation_scope": ["manual_login", "submit_prompt", "read_response"],
}


def _payload(target, prompt="Reply with the word ready.", **context):
    job_context = dict(_MANUAL_CONTEXT)
    job_context.update(context)
    return {
        "api_version": "2026-05-22",
        "job_id": "job_live_%s" % target,
        "trace_id": "trace_live_%s" % target,
        "job": {
            "target": target,
            "command_type": "chat.prompt",
            "input": {"prompt": prompt},
            "context": job_context,
        },
    }


def _types(events):
    return [event["type"] for event in events]


class SelectorConfigTests(unittest.TestCase):
    def test_all_live_providers_have_selectors(self):
        # Every real provider is registered; the generic template ships as an
        # additional copy-and-tune entry.
        self.assertTrue(set(_LIVE_PROVIDERS).issubset(set(PROVIDER_SELECTORS)))
        self.assertEqual(
            set(PROVIDER_SELECTORS) - set(_LIVE_PROVIDERS),
            {"generic_live_web"},
        )

    def test_selector_groups_are_non_empty_and_versioned(self):
        for provider_id, selectors in PROVIDER_SELECTORS.items():
            with self.subTest(provider=provider_id):
                self.assertTrue(selectors.target_url.startswith("https://"))
                self.assertTrue(selectors.selector_version)
                for group in selectors.all_groups():
                    self.assertIsInstance(group, SelectorGroup)
                    self.assertTrue(group.candidates, "group %s empty" % group.name)
                    self.assertEqual(group.primary, group.candidates[0])

    def test_get_provider_selectors_unknown_raises(self):
        with self.assertRaises(KeyError):
            get_provider_selectors("nope_web")


class EngineHappyPathTests(unittest.TestCase):
    def test_authenticated_session_streams_and_completes(self):
        selectors = get_provider_selectors("chatgpt_web")
        engine = LiveSessionEngine(selectors)
        driver = MockPageDriver(
            authenticated=True,
            response_text="ready now",
            tokens=["rea", "dy ", "now"],
        )

        events = engine.run(_payload("chatgpt_web"), driver=driver)

        self.assertEqual(
            _types(events),
            [
                "queued",
                "session.opening",
                "session.authenticated",
                "running",
                "token",
                "token",
                "token",
                "completed",
            ],
        )
        completed = events[-1]
        self.assertEqual(completed["data"]["result"]["text"], "ready now")
        self.assertEqual(completed["data"]["metadata"]["token_count"], 3)
        self.assertEqual(
            completed["data"]["metadata"]["selector_version"],
            selectors.selector_version,
        )
        self.assertEqual(driver.submitted_prompt, "Reply with the word ready.")
        self.assertTrue(driver.opened)
        # Injected drivers are owned by the caller; the engine only closes
        # drivers it created itself (the real Playwright path).
        self.assertFalse(driver.closed)

    def test_event_envelope_matches_worker_protocol(self):
        engine = LiveSessionEngine(get_provider_selectors("claude_web"))
        events = engine.run(_payload("claude_web"), driver=MockPageDriver())
        for index, event in enumerate(events, start=1):
            self.assertEqual(event["sequence"], index)
            for field in ("api_version", "event_id", "job_id", "trace_id", "type", "created_at", "data"):
                self.assertIn(field, event)
            self.assertTrue(event["event_id"].startswith("evt_"))
            self.assertEqual(event["job_id"], "job_live_claude_web")

    def test_profile_label_never_leaks_full_path(self):
        engine = LiveSessionEngine(get_provider_selectors("deepseek_web"))
        events = engine.run(
            _payload("deepseek_web", user_data_dir="/home/user/secret/profiles/main"),
            driver=MockPageDriver(),
        )
        opening = next(e for e in events if e["type"] == "session.opening")
        self.assertEqual(opening["data"]["profile"], "main")
        self.assertNotIn("/home/user", str(opening["data"]))


class ManualLoginFlowTests(unittest.TestCase):
    def test_manual_action_required_then_login_completes(self):
        engine = LiveSessionEngine(get_provider_selectors("gemini_web"))
        driver = MockPageDriver(authenticated=False, login_after_wait=True)

        events = engine.run(_payload("gemini_web"), driver=driver)

        types = _types(events)
        self.assertIn("session.manual_action_required", types)
        self.assertIn("session.authenticated", types)
        self.assertEqual(types[-1], "completed")

        manual = next(e for e in events if e["type"] == "session.manual_action_required")
        self.assertEqual(manual["data"]["reason"], "manual_login_required")
        self.assertEqual(manual["data"]["account_binding_id"], "acct_live_123")
        self.assertEqual(manual["data"]["consent_ref"], "consent_live_123")
        self.assertTrue(
            manual["data"]["novnc_url"].startswith("http://127.0.0.1:7900/session/")
        )
        self.assertEqual(
            manual["data"]["novnc_url"],
            "http://127.0.0.1:7900/session/%s" % manual["data"]["session_id"],
        )

    def test_manual_login_timeout_blocks_retryable(self):
        engine = LiveSessionEngine(get_provider_selectors("perplexity_web"))
        driver = MockPageDriver(authenticated=False, login_after_wait=False)

        events = engine.run(_payload("perplexity_web"), driver=driver)

        self.assertEqual(_types(events), ["queued", "session.opening", "session.manual_action_required", "blocked"])
        blocked = events[-1]
        self.assertEqual(blocked["data"]["reason"], "manual_login_required")
        self.assertTrue(blocked["data"]["retryable"])
        self.assertIsNone(driver.submitted_prompt)

    def test_session_id_rejects_path_confusing_values(self):
        engine = LiveSessionEngine(get_provider_selectors("mistral_lechat"))
        for raw in [".", "..", "../x", "http://evil", "has/slash", "has?query"]:
            with self.subTest(session_id=raw):
                events = engine.run(
                    _payload("mistral_lechat", session_id=raw),
                    driver=MockPageDriver(authenticated=False, login_after_wait=False),
                )
                manual = next(e for e in events if e["type"] == "session.manual_action_required")
                self.assertTrue(manual["data"]["session_id"].startswith("sess_"))


class DriftDetectionTests(unittest.TestCase):
    def test_prompt_input_drift_blocks_with_error_code(self):
        engine = LiveSessionEngine(get_provider_selectors("chatgpt_web"))
        driver = MockPageDriver(authenticated=True, drift_group="prompt_input")

        events = engine.run(_payload("chatgpt_web"), driver=driver)

        blocked = events[-1]
        self.assertEqual(blocked["type"], "blocked")
        self.assertEqual(blocked["data"]["reason"], "selector_drift_detected")
        self.assertEqual(blocked["data"]["error_code"], "UBAG-ADAPTER-DRIFT-014")
        self.assertEqual(blocked["data"]["selector_group"], "prompt_input")
        self.assertFalse(blocked["data"]["retryable"])
        # Screenshot artifact captured on failure only.
        self.assertIsNotNone(blocked["data"]["artifact_screenshot"])

    def test_drift_detected_error_carries_code(self):
        err = DriftDetectedError("submit_button", "v1")
        self.assertEqual(err.error_code, "UBAG-ADAPTER-DRIFT-014")
        self.assertEqual(err.group_name, "submit_button")


class SecurityInvariantTests(unittest.TestCase):
    def test_payload_with_cookie_is_rejected(self):
        engine = LiveSessionEngine(get_provider_selectors("chatgpt_web"))
        bad = _payload("chatgpt_web")
        bad["job"]["input"]["cookies"] = "session=abc123"
        with self.assertRaises(LiveSessionError) as raised:
            engine.run(bad, driver=MockPageDriver())
        self.assertIn("must not include credentials", str(raised.exception))

    def test_payload_with_password_is_rejected(self):
        engine = LiveSessionEngine(get_provider_selectors("claude_web"))
        bad = _payload("claude_web")
        bad["job"]["password"] = "hunter2"
        with self.assertRaises(LiveSessionError):
            engine.run(bad, driver=MockPageDriver())

    def test_user_data_dir_path_is_allowed(self):
        engine = LiveSessionEngine(get_provider_selectors("claude_web"))
        events = engine.run(
            _payload("claude_web", user_data_dir="var/profiles/claude_web/alice"),
            driver=MockPageDriver(),
        )
        self.assertEqual(_types(events)[-1], "completed")


class DriverFactoryTests(unittest.TestCase):
    def setUp(self):
        self._saved = {
            key: os.environ.get(key)
            for key in ("UBAG_ADAPTER_OFFLINE", "UBAG_WORKER_OFFLINE")
        }

    def tearDown(self):
        for key, value in self._saved.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value

    def test_offline_flag_yields_mock_driver(self):
        os.environ["UBAG_ADAPTER_OFFLINE"] = "1"
        self.assertTrue(offline_mode_enabled())
        driver = create_default_driver({"offline_response": "hi"})
        self.assertIsInstance(driver, MockPageDriver)
        self.assertEqual(driver.response_text, "hi")

    def test_worker_offline_flag_yields_mock_driver(self):
        os.environ.pop("UBAG_ADAPTER_OFFLINE", None)
        os.environ["UBAG_WORKER_OFFLINE"] = "true"
        self.assertTrue(offline_mode_enabled())
        self.assertIsInstance(create_default_driver(), MockPageDriver)

    def test_default_driver_is_playwright_when_online(self):
        os.environ.pop("UBAG_ADAPTER_OFFLINE", None)
        os.environ.pop("UBAG_WORKER_OFFLINE", None)
        self.assertFalse(offline_mode_enabled())
        self.assertIsInstance(create_default_driver(), PlaywrightPageDriver)

    def test_offline_end_to_end_run_is_deterministic(self):
        os.environ["UBAG_ADAPTER_OFFLINE"] = "1"
        engine = LiveSessionEngine(get_provider_selectors("chatgpt_web"))
        payload = _payload("chatgpt_web")
        payload["job"]["options"] = {"offline_response": "ready", "offline_tokens": ["rea", "dy"]}
        first = engine.run(payload)
        second = engine.run(payload)
        self.assertEqual(first, second)
        self.assertEqual(first[-1]["data"]["result"]["text"], "ready")


class AdapterWiringTests(unittest.TestCase):
    def test_default_run_still_fails_closed(self):
        manifests = load_manifests()
        for provider_id in _LIVE_PROVIDERS:
            adapter = instantiate_adapter(manifests[provider_id])
            with self.subTest(provider=provider_id):
                with self.assertRaises(NotImplementedError) as raised:
                    adapter.run({"job": {"target": provider_id, "input": {"prompt": "hi"}}})
                message = str(raised.exception)
                self.assertIn("safe-mode scaffold", message)
                self.assertIn("user-owned manual browser session", message)
                self.assertIn("will not collect credentials", message)
                self.assertIn("solve CAPTCHA", message)

    def test_run_live_with_injected_driver_completes_for_all_providers(self):
        manifests = load_manifests()
        for provider_id in _LIVE_PROVIDERS:
            adapter = instantiate_adapter(manifests[provider_id])
            with self.subTest(provider=provider_id):
                events = adapter.run_live(
                    _payload(provider_id),
                    driver=MockPageDriver(authenticated=True, response_text="ready"),
                )
                self.assertEqual(events[0]["type"], "queued")
                self.assertEqual(events[-1]["type"], "completed")
                self.assertEqual(events[-1]["data"]["result"]["text"], "ready")
                self.assertEqual(events[-1]["data"]["metadata"]["adapter"], provider_id)


class LiveWebTemplateTests(unittest.TestCase):
    def test_generic_template_is_registered_and_discoverable(self):
        self.assertIn("generic_live_web", PROVIDER_SELECTORS)
        selectors = get_provider_selectors("generic_live_web")
        self.assertEqual(selectors.provider_id, "generic_live_web")
        self.assertTrue(selectors.target_url.startswith("https://"))
        for group in selectors.all_groups():
            self.assertTrue(group.candidates, "group %s empty" % group.name)

    def test_template_runs_manual_flow_through_engine(self):
        from ubag_worker.live import live_web_template

        selectors = live_web_template(
            provider_id="acme_chat_web",
            display_name="Acme Chat",
            target_url="https://chat.acme.example/",
        )
        engine = LiveSessionEngine(selectors)
        driver = MockPageDriver(authenticated=False, login_after_wait=True, response_text="ok")

        events = engine.run(_payload("acme_chat_web"), driver=driver)

        types = _types(events)
        self.assertIn("session.manual_action_required", types)
        self.assertIn("session.authenticated", types)
        self.assertEqual(types[-1], "completed")
        self.assertEqual(events[-1]["data"]["metadata"]["adapter"], "acme_chat_web")

    def test_template_rejects_secret_material(self):
        from ubag_worker.live import live_web_template

        engine = LiveSessionEngine(
            live_web_template("acme_chat_web", "Acme Chat", "https://chat.acme.example/")
        )
        bad = _payload("acme_chat_web")
        bad["job"]["input"]["cookies"] = "session=abc123"
        with self.assertRaises(LiveSessionError):
            engine.run(bad, driver=MockPageDriver())

    def test_template_validates_inputs(self):
        from ubag_worker.live import live_web_template

        with self.assertRaises(ValueError):
            live_web_template("", "Acme", "https://chat.acme.example/")
        with self.assertRaises(ValueError):
            live_web_template("acme", "Acme", "http://insecure.example/")

    def test_template_allows_selector_overrides(self):
        from ubag_worker.live import live_web_template

        selectors = live_web_template(
            provider_id="acme_chat_web",
            display_name="Acme Chat",
            target_url="https://chat.acme.example/",
            prompt_input=("#acme-input",),
        )
        self.assertEqual(selectors.prompt_input.primary, "#acme-input")
        # Untouched groups still carry safe placeholder defaults.
        self.assertTrue(selectors.submit_button.candidates)


if __name__ == "__main__":
    unittest.main()

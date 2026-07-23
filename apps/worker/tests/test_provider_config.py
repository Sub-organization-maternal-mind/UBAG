"""Tests for the pre-submit configuration phase of the live engine.

Before submitting a prompt the engine must, when the provider declares them:

* start a fresh conversation (``session.new_chat`` + ``MockPageDriver.started_new_chat``);
* idempotently enforce the provider's model/mode/reasoning settings
  (``session.configured`` + ``MockPageDriver.ensured_settings``), reading current
  state and reporting ``already_set`` vs ``set``;
* surface a setting that cannot be confirmed as selector drift (blocked), never a
  silent no-op;
* honor env / job ``provider_config`` overrides (desired value + ``_enabled`` /
  ``_new_chat`` gates).

Providers that declare neither a New-chat control nor settings stay byte-for-byte
unchanged. Every new event is additive (it sits between ``session.authenticated``
and prompt submission), so existing flows are preserved.
"""

import os
import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.live import (  # noqa: E402
    LiveSessionEngine,
    MockPageDriver,
)
from ubag_worker.live.engine import (  # noqa: E402
    _flag,
    _resolve_provider_config,
)
from ubag_worker.live.selectors import get_provider_selectors  # noqa: E402

_MANUAL_CONTEXT = {
    "account_binding_id": "acct_live_123",
    "consent_ref": "consent_live_123",
    "automation_scope": ["manual_login", "submit_prompt", "read_response"],
}


def _payload(target, *, prompt="Reply with the word ready.", provider_config=None):
    options = {"return_mode": "final"}
    if provider_config is not None:
        options["provider_config"] = provider_config
    return {
        "api_version": "2026-05-22",
        "job_id": "job_cfg_%s" % target,
        "trace_id": "trace_cfg_%s" % target,
        "job": {
            "target": target,
            "command_type": "chat.prompt",
            "input": {"prompt": prompt},
            "options": options,
            "context": dict(_MANUAL_CONTEXT),
        },
    }


class _FlakyDriver(MockPageDriver):
    """A mock that raises a transient (non-drift) error on the first N submits and
    counts reset() calls, to exercise the engine's interaction-retry path."""

    def __init__(self, *, fails=1, **kwargs):
        super().__init__(**kwargs)
        self._fails = fails
        self.reset_calls = 0

    def reset(self, target_url):  # type: ignore[override]
        self.reset_calls += 1

    def submit_prompt(self, selectors, prompt):  # type: ignore[override]
        if self._fails > 0:
            self._fails -= 1
            raise RuntimeError("transient CDP hiccup")
        return super().submit_prompt(selectors, prompt)


def _types(events):
    return [event["type"] for event in events]


def _event(events, type_):
    return next(e for e in events if e["type"] == type_)


class NewChatAndConfigTests(unittest.TestCase):
    def test_deepseek_emits_new_chat_and_configured_before_submit(self):
        selectors = get_provider_selectors("deepseek_web")
        driver = MockPageDriver(response_text="391")
        events = LiveSessionEngine(selectors).run(_payload("deepseek_web"), driver=driver)
        types = _types(events)

        self.assertTrue(driver.started_new_chat)
        self.assertIn("session.new_chat", types)
        self.assertIn("session.configured", types)
        # ordering: authenticated -> new_chat -> configured -> completed
        self.assertLess(types.index("session.authenticated"), types.index("session.new_chat"))
        self.assertLess(types.index("session.new_chat"), types.index("session.configured"))
        self.assertLess(types.index("session.configured"), types.index("completed"))

        configured = _event(events, "session.configured")
        keys = {s["key"] for s in configured["data"]["settings"]}
        self.assertEqual(keys, {"mode", "deepthink"})
        self.assertEqual(driver.submitted_prompt, "Reply with the word ready.")

    def test_idempotent_already_set_vs_set(self):
        selectors = get_provider_selectors("deepseek_web")
        # DeepThink is already correct; mode must still be applied.
        driver = MockPageDriver(config_already=["deepthink"])
        events = LiveSessionEngine(selectors).run(_payload("deepseek_web"), driver=driver)
        by_key = {s["key"]: s for s in _event(events, "session.configured")["data"]["settings"]}

        self.assertEqual(by_key["deepthink"]["state"], "already_set")
        self.assertEqual(by_key["mode"]["state"], "set")

    def test_setting_drift_blocks_with_setting_group(self):
        selectors = get_provider_selectors("deepseek_web")
        driver = MockPageDriver(drift_group="setting:mode")
        events = LiveSessionEngine(selectors).run(_payload("deepseek_web"), driver=driver)
        blocked = [e for e in events if e["type"] == "blocked"]

        self.assertTrue(blocked)
        self.assertEqual(blocked[0]["data"]["reason"], "selector_drift_detected")
        self.assertEqual(blocked[0]["data"]["selector_group"], "setting:mode")
        # never proceeds to submit / completion on a config drift
        self.assertNotIn("completed", _types(events))

    def test_gemini_pins_3_6_flash_and_disables_extended_thinking(self):
        selectors = get_provider_selectors("gemini_web")
        driver = MockPageDriver(response_text="9")
        events = LiveSessionEngine(selectors).run(_payload("gemini_web"), driver=driver)
        settings = selectors.settings
        self.assertEqual(
            [(setting.key, setting.kind, setting.desired) for setting in settings],
            [("model", "choice", "3.6 Flash"), ("thinking", "toggle", False)],
        )
        self.assertEqual(
            settings[1].on_when,
            ("gem-menu-item.selected:has-text('Extended thinking')",),
        )
        self.assertEqual(
            settings[1].toggle_click,
            ("gem-menu-item:has-text('Extended thinking')",),
        )
        self.assertFalse(selectors.reasoning)
        configured = _event(events, "session.configured")["data"]["settings"]
        self.assertEqual(
            [(setting["key"], setting["desired"]) for setting in configured],
            [("model", "3.6 Flash"), ("thinking", False)],
        )

    def test_provider_config_overrides_desired_value(self):
        selectors = get_provider_selectors("deepseek_web")
        driver = MockPageDriver()
        events = LiveSessionEngine(selectors).run(
            _payload("deepseek_web", provider_config={"mode": "Vision"}),
            driver=driver,
        )
        by_key = {s["key"]: s for s in _event(events, "session.configured")["data"]["settings"]}
        self.assertEqual(by_key["mode"]["desired"], "Vision")

    def test_deepseek_attachment_job_defaults_to_file_capable_instant_mode(self):
        selectors = get_provider_selectors("deepseek_web")
        driver = MockPageDriver()
        payload = _payload("deepseek_web")
        payload["job"]["input"]["attachments"] = [
            {
                "key": "report.txt",
                "content_type": "text/plain",
                "kind": "document",
            }
        ]
        payload["job"]["input"]["attachment_local_paths"] = ["/tmp/report.txt"]

        events = LiveSessionEngine(selectors).run(payload, driver=driver)

        by_key = {
            setting["key"]: setting
            for setting in _event(events, "session.configured")["data"]["settings"]
        }
        self.assertEqual(by_key["mode"]["desired"], "Instant")
        self.assertEqual(driver.attached_files, ["/tmp/report.txt"])

    def test_config_disabled_skips_configured_but_keeps_new_chat(self):
        selectors = get_provider_selectors("deepseek_web")
        driver = MockPageDriver()
        events = LiveSessionEngine(selectors).run(
            _payload("deepseek_web", provider_config={"_enabled": False}),
            driver=driver,
        )
        types = _types(events)
        self.assertNotIn("session.configured", types)
        self.assertIn("session.new_chat", types)
        self.assertIn("completed", types)
        self.assertEqual(driver.ensured_settings, [])

    def test_new_chat_disabled_skips_new_chat_but_keeps_config(self):
        selectors = get_provider_selectors("deepseek_web")
        driver = MockPageDriver()
        events = LiveSessionEngine(selectors).run(
            _payload("deepseek_web", provider_config={"_new_chat": False}),
            driver=driver,
        )
        types = _types(events)
        self.assertNotIn("session.new_chat", types)
        self.assertIn("session.configured", types)
        self.assertFalse(driver.started_new_chat)

    def test_chatgpt_pins_model_and_thinking(self):
        # Supersedes the earlier "no forced model/mode for ChatGPT" decision:
        # the operator now requires every ChatGPT job to run on GPT-5.6 Sol at
        # Medium intelligence, so chatgpt_web enforces both settings (in that
        # order — switching model can reset the intelligence level).
        selectors = get_provider_selectors("chatgpt_web")
        driver = MockPageDriver(response_text="Canberra")
        events = LiveSessionEngine(selectors).run(_payload("chatgpt_web"), driver=driver)
        types = _types(events)
        self.assertIn("session.new_chat", types)
        self.assertIn("session.configured", types)
        self.assertIn("completed", types)
        self.assertEqual(
            [(r["key"], r["desired"]) for r in driver.ensured_settings],
            [("model", "GPT-5.6 Sol"), ("thinking", "Medium")],
        )

    def test_chatgpt_model_and_thinking_are_overridable_per_job(self):
        # The pin is a DEFAULT, not a hard-code: options.provider_config still
        # wins (same precedence as every other provider).
        selectors = get_provider_selectors("chatgpt_web")
        driver = MockPageDriver(response_text="Canberra")
        LiveSessionEngine(selectors).run(
            _payload("chatgpt_web", provider_config={"thinking": "High"}),
            driver=driver,
        )
        self.assertEqual(
            [(r["key"], r["desired"]) for r in driver.ensured_settings],
            [("model", "GPT-5.6 Sol"), ("thinking", "High")],
        )

    def test_transient_interaction_failure_retries_and_completes(self):
        selectors = get_provider_selectors("deepseek_web")
        driver = _FlakyDriver(fails=1, response_text="42")
        events = LiveSessionEngine(selectors).run(_payload("deepseek_web"), driver=driver)
        types = _types(events)

        self.assertIn("completed", types)
        self.assertEqual(driver.reset_calls, 1)  # one reset between the two attempts
        self.assertEqual(_event(events, "completed")["data"]["result"]["text"], "42")
        # Buffered retry must not double-emit pre-submit events.
        self.assertEqual(types.count("completed"), 1)
        self.assertEqual(types.count("session.new_chat"), 1)
        self.assertEqual(types.count("session.configured"), 1)

    def test_transient_failure_exhausts_attempts_and_propagates(self):
        selectors = get_provider_selectors("deepseek_web")
        driver = _FlakyDriver(fails=9, response_text="x")  # always fails
        prior = os.environ.get("UBAG_INTERACTION_ATTEMPTS")
        os.environ["UBAG_INTERACTION_ATTEMPTS"] = "2"  # pin for a deterministic count
        try:
            with self.assertRaises(RuntimeError):
                LiveSessionEngine(selectors).run(_payload("deepseek_web"), driver=driver)
        finally:
            if prior is None:
                os.environ.pop("UBAG_INTERACTION_ATTEMPTS", None)
            else:
                os.environ["UBAG_INTERACTION_ATTEMPTS"] = prior
        self.assertEqual(driver.reset_calls, 1)  # 2 attempts -> exactly one reset

    def test_provider_without_new_chat_or_settings_unchanged(self):
        # mistral_lechat declares neither; the new phase must be a no-op.
        selectors = get_provider_selectors("mistral_lechat")
        driver = MockPageDriver(response_text="ok")
        events = LiveSessionEngine(selectors).run(_payload("mistral_lechat"), driver=driver)
        types = _types(events)
        self.assertNotIn("session.new_chat", types)
        self.assertNotIn("session.configured", types)
        self.assertFalse(driver.started_new_chat)
        self.assertEqual(driver.ensured_settings, [])
        self.assertIn("completed", types)


class ResolverHelperTests(unittest.TestCase):
    def test_flag_coercion(self):
        self.assertTrue(_flag(None, True))
        self.assertFalse(_flag(None, False))
        self.assertFalse(_flag("off", True))
        self.assertFalse(_flag("0", True))
        self.assertFalse(_flag(False, True))
        self.assertTrue(_flag("yes", False))
        self.assertTrue(_flag(True, False))

    def test_resolve_provider_config_env_then_options(self):
        env_key = "UBAG_PROVIDER_CONFIG_DEEPSEEK_WEB"
        prior = os.environ.get(env_key)
        os.environ[env_key] = '{"mode": "Expert", "deepthink": true}'
        try:
            cfg = _resolve_provider_config(
                "deepseek_web", {"provider_config": {"mode": "Vision"}}
            )
        finally:
            if prior is None:
                os.environ.pop(env_key, None)
            else:
                os.environ[env_key] = prior
        # options override env; env supplies the rest
        self.assertEqual(cfg["mode"], "Vision")
        self.assertEqual(cfg["deepthink"], True)

    def test_resolve_provider_config_ignores_bad_env_json(self):
        env_key = "UBAG_PROVIDER_CONFIG_GEMINI_WEB"
        prior = os.environ.get(env_key)
        os.environ[env_key] = "{not valid json"
        try:
            cfg = _resolve_provider_config("gemini_web", {})
        finally:
            if prior is None:
                os.environ.pop(env_key, None)
            else:
                os.environ[env_key] = prior
        self.assertEqual(cfg, {})


if __name__ == "__main__":
    unittest.main()

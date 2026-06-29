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

    def test_gemini_settings_keys(self):
        selectors = get_provider_selectors("gemini_web")
        driver = MockPageDriver(response_text="9")
        events = LiveSessionEngine(selectors).run(_payload("gemini_web"), driver=driver)
        keys = [s["key"] for s in _event(events, "session.configured")["data"]["settings"]]
        self.assertEqual(keys, ["model", "thinking"])

    def test_provider_config_overrides_desired_value(self):
        selectors = get_provider_selectors("deepseek_web")
        driver = MockPageDriver()
        events = LiveSessionEngine(selectors).run(
            _payload("deepseek_web", provider_config={"mode": "Vision"}),
            driver=driver,
        )
        by_key = {s["key"]: s for s in _event(events, "session.configured")["data"]["settings"]}
        self.assertEqual(by_key["mode"]["desired"], "Vision")

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

    def test_chatgpt_has_new_chat_but_no_settings(self):
        selectors = get_provider_selectors("chatgpt_web")
        driver = MockPageDriver(response_text="Canberra")
        events = LiveSessionEngine(selectors).run(_payload("chatgpt_web"), driver=driver)
        types = _types(events)
        self.assertIn("session.new_chat", types)
        self.assertNotIn("session.configured", types)
        self.assertIn("completed", types)

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

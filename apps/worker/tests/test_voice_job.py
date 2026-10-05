"""Voice job tests — all externals faked, no browser, no network."""

from __future__ import annotations

import contextlib
import io
import json
import unittest
from unittest import mock

import run_live_worker
from ubag_worker import runner
from ubag_worker.live import daemon as daemon_module
from ubag_worker.live.daemon import WarmWorkerDaemon
from ubag_worker.voice import voice_job
from ubag_worker.voice.voice_job import is_voice_command, iter_voice_events
from ubag_worker.voice.voice_runner import VoiceActivationResult

ENDPOINT = "http://browser:9223"
GATEWAY_TYPES = {"running", "completed", "failed", "blocked"}
TERMINAL = {"completed", "failed", "blocked"}


class FakePage:
    def __init__(self, url: str, elements: dict | None = None):
        self.url = url
        self.elements = elements or {}
        self.goto_calls: list[str] = []

    def query_visible_element(self, selector: str):
        return self.elements.get(selector)

    def goto(self, url: str) -> None:
        self.goto_calls.append(url)
        self.url = url


class FakeClient:
    def __init__(self, pages: list | None = None):
        self._pages = list(pages or [])
        self.closed = 0

    def pages(self):
        return list(self._pages)

    def new_page(self, url: str):
        page = FakePage(url)
        self._pages.append(page)
        return page

    def close(self) -> None:
        self.closed += 1


def envelope(action="activate", provider="chatgpt_web", **input_overrides) -> dict:
    voice_input = {"provider_id": provider, "cdp_endpoint": ENDPOINT, "action": action}
    voice_input.update(input_overrides)
    return {
        "api_version": "2026-05-22",
        "job_id": "job_voice_1",
        "trace_id": "trace_voice_1",
        "tenant_id": "tenant_a",
        "app_id": "app_a",
        "job": {"target": provider, "command_type": "voice." + action, "input": voice_input},
    }


def allow(hosts="browser", remote=""):
    return mock.patch.dict("os.environ", {
        "UBAG_VOICE_CDP_ALLOWED_HOSTS": hosts,
        "UBAG_REMOTE_BROWSER_ENDPOINT": remote,
    })


def result_for(state: str) -> VoiceActivationResult:
    return VoiceActivationResult(
        provider="chatgpt_web", activated=state == "activated", state=state,
        reason="why_" + state)


def assert_one_terminal(test: unittest.TestCase, events: list) -> dict:
    types = [e["type"] for e in events]
    test.assertTrue(set(types) <= GATEWAY_TYPES, types)
    terminals = [e for e in events if e["type"] in TERMINAL]
    test.assertEqual(len(terminals), 1, types)
    test.assertEqual(events[-1], terminals[0])
    test.assertEqual([e["sequence"] for e in events], list(range(1, len(events) + 1)))
    return terminals[0]


class TerminalMappingTests(unittest.TestCase):
    def run_state(self, action: str, state: str) -> list:
        runner_name = "deactivate_voice" if action == "deactivate" else "activate_voice"
        with allow(), mock.patch.object(voice_job, runner_name, return_value=result_for(state)):
            return list(iter_voice_events(envelope(action), lambda *a: FakeClient()))

    def test_every_state_yields_exactly_one_terminal_event(self) -> None:
        expect = {
            "activated": "completed", "deactivated": "completed",
            "login_wall": "blocked", "selector_drift": "failed",
            "voice_unavailable": "failed", "setup_prompt": "failed", "timeout": "failed",
            "unverified_ready": "failed", "attach_failed": "failed", "no_tab": "failed",
        }
        for state, terminal_type in expect.items():
            action = "deactivate" if state == "deactivated" else "activate"
            with self.subTest(state=state):
                events = self.run_state(action, state)
                self.assertEqual(events[0]["type"], "running")
                terminal = assert_one_terminal(self, events)
                self.assertEqual(terminal["type"], terminal_type)
                self.assertEqual(terminal["data"]["result"]["state"], state)
                self.assertEqual(terminal["data"]["state"], state)
                if terminal_type != "completed":
                    self.assertIs(terminal["data"]["retryable"], False)
                    self.assertEqual(terminal["data"]["reason"], "why_" + state)

    def test_identity_comes_from_the_envelope(self) -> None:
        events = self.run_state("activate", "activated")
        for event in events:
            self.assertEqual(event["job_id"], "job_voice_1")
            self.assertEqual(event["trace_id"], "trace_voice_1")
            self.assertEqual(event["api_version"], "2026-05-22")
            self.assertEqual(event["data"]["tenant_id"], "tenant_a")
            self.assertEqual(event["data"]["app_id"], "app_a")

    def test_real_runner_end_to_end_with_fake_cdp(self) -> None:
        page = FakePage("https://chatgpt.com/c/live")
        with allow():
            events = list(iter_voice_events(envelope("deactivate"), lambda *a: FakeClient([page])))
        terminal = assert_one_terminal(self, events)
        self.assertEqual(terminal["type"], "completed")
        self.assertEqual(terminal["data"]["result"]["state"], "deactivated")
        self.assertEqual(page.goto_calls, ["https://chatgpt.com/"])

    def test_event_data_is_bounded(self) -> None:
        huge = VoiceActivationResult(
            provider="chatgpt_web", activated=False, state="timeout", reason="r",
            details={"error": "x" * 200_000})
        with allow(), mock.patch.object(voice_job, "activate_voice", return_value=huge):
            terminal = list(iter_voice_events(envelope(), lambda *a: FakeClient()))[-1]
        self.assertLess(len(json.dumps(terminal)), 64 * 1024)


class ValidationTests(unittest.TestCase):
    def assert_invalid(self, payload: dict, env=None) -> dict:
        touched = []

        def factory(*args):
            touched.append(args)
            return FakeClient()

        with (env or allow()):
            events = list(iter_voice_events(payload, factory))
        self.assertEqual(touched, [], "browser must not be touched")
        terminal = assert_one_terminal(self, events)
        self.assertEqual(terminal["type"], "failed")
        self.assertEqual(terminal["data"]["state"], "invalid_request")
        self.assertIs(terminal["data"]["retryable"], False)
        return terminal

    def test_host_not_allowlisted(self) -> None:
        self.assert_invalid(envelope(cdp_endpoint="http://evil.example:9223"))

    def test_fails_closed_when_no_allowlist_env_is_set(self) -> None:
        self.assert_invalid(envelope(), env=allow(hosts="", remote=""))

    def test_remote_browser_endpoint_host_is_allowed(self) -> None:
        with allow(hosts="", remote="http://grid.internal:4444"):
            events = list(iter_voice_events(
                envelope(cdp_endpoint="http://grid.internal:9223"), lambda *a: FakeClient()))
        self.assertEqual(events[0]["type"], "running")

    def test_allowlist_is_case_insensitive_exact_host(self) -> None:
        with allow(hosts="Browser"):
            events = list(iter_voice_events(envelope(), lambda *a: FakeClient()))
        self.assertEqual(events[0]["type"], "running")
        self.assert_invalid(envelope(cdp_endpoint="http://browser.evil.example:9223"))

    def test_bad_endpoints(self) -> None:
        for endpoint in (None, "", "browser:9223", "ftp://browser/", "ws://browser:9223",
                         "http://", "http://user:pw@browser:9223", "http://browser:notaport"):
            with self.subTest(endpoint=endpoint):
                self.assert_invalid(envelope(cdp_endpoint=endpoint))

    def test_target_provider_mismatch(self) -> None:
        payload = envelope()
        payload["job"]["target"] = "gemini_web"
        self.assert_invalid(payload)

    def test_unsupported_voice_provider(self) -> None:
        self.assert_invalid(envelope(provider="deepseek_web"))

    def test_action_must_match_command_type(self) -> None:
        payload = envelope("activate")
        payload["job"]["command_type"] = "voice.deactivate"
        self.assert_invalid(payload)
        self.assert_invalid(envelope(action="reboot"))

    def test_bad_context(self) -> None:
        for context in (-1, "-1", "abc", True, 1.5):
            with self.subTest(context=context):
                self.assert_invalid(envelope(context=context))

    def test_context_is_forwarded_as_string(self) -> None:
        seen = []
        with allow():
            list(iter_voice_events(envelope(context=1), lambda *a: seen.append(a) or FakeClient()))
        self.assertEqual(seen, [(ENDPOINT, "1")])

    def test_malformed_payloads_never_raise(self) -> None:
        for payload in ({}, {"job": None}, {"job": {"command_type": "voice.activate"}}):
            with self.subTest(payload=payload), allow():
                assert_one_terminal(self, list(iter_voice_events(payload, lambda *a: FakeClient())))


class ClientLifecycleTests(unittest.TestCase):
    def test_client_closed_on_success(self) -> None:
        client = FakeClient([FakePage("https://chatgpt.com/")])
        with allow():
            list(iter_voice_events(envelope("deactivate"), lambda *a: client))
        self.assertEqual(client.closed, 1)

    def test_client_closed_and_internal_error_on_exception(self) -> None:
        client = FakeClient()
        boom = RuntimeError("secret-cookie=abc")
        with allow(), mock.patch.object(voice_job, "activate_voice", side_effect=boom):
            events = list(iter_voice_events(envelope(), lambda *a: client))
        terminal = assert_one_terminal(self, events)
        self.assertEqual(client.closed, 1)
        self.assertEqual(terminal["type"], "failed")
        self.assertEqual(terminal["data"]["state"], "internal_error")
        self.assertNotIn("secret-cookie", json.dumps(terminal))

    def test_close_failure_does_not_mask_the_outcome(self) -> None:
        client = FakeClient([FakePage("https://chatgpt.com/")])
        client.close = mock.Mock(side_effect=RuntimeError("close boom"))
        with allow():
            events = list(iter_voice_events(envelope("deactivate"), lambda *a: client))
        self.assertEqual(assert_one_terminal(self, events)["type"], "completed")

    def test_factory_failure_is_attach_failed(self) -> None:
        def factory(*args):
            raise ConnectionError("refused")

        with allow():
            events = list(iter_voice_events(envelope(), factory))
        terminal = assert_one_terminal(self, events)
        self.assertEqual(terminal["data"]["state"], "attach_failed")

    def test_default_factory_is_resolved_at_call_time(self) -> None:
        client = FakeClient([FakePage("https://chatgpt.com/")])
        with allow(), mock.patch.object(voice_job, "PlaywrightCdpClient", lambda *a: client):
            events = list(iter_voice_events(envelope("deactivate")))
        self.assertEqual(assert_one_terminal(self, events)["type"], "completed")
        self.assertEqual(client.closed, 1)


class HookTests(unittest.TestCase):
    def test_is_voice_command(self) -> None:
        self.assertTrue(is_voice_command(envelope()))
        self.assertTrue(is_voice_command(envelope("deactivate")))
        self.assertFalse(is_voice_command({"job": {"command_type": "chat.prompt"}}))
        self.assertFalse(is_voice_command({"job": {}}))
        self.assertFalse(is_voice_command({}))
        self.assertFalse(is_voice_command(None))

    def test_run_live_worker_main_routes_voice_before_live_engine(self) -> None:
        client = FakeClient([FakePage("https://chatgpt.com/c/live")])
        out = io.StringIO()
        with allow(), \
                mock.patch.object(voice_job, "PlaywrightCdpClient", lambda *a: client), \
                mock.patch.object(run_live_worker, "LiveSessionEngine") as engine, \
                mock.patch.object(run_live_worker, "emit_jsonl") as registry, \
                contextlib.redirect_stdout(out):
            code = run_live_worker.main(["--payload", json.dumps(envelope("deactivate"))])
        self.assertEqual(code, 0)
        engine.assert_not_called()
        registry.assert_not_called()
        events = [json.loads(line) for line in out.getvalue().splitlines()]
        self.assertEqual(assert_one_terminal(self, events)["data"]["result"]["state"], "deactivated")
        self.assertEqual(client.closed, 1)

    def test_run_live_worker_main_voice_rejection_exits_zero(self) -> None:
        out = io.StringIO()
        with allow(hosts=""), contextlib.redirect_stdout(out):
            code = run_live_worker.main(["--payload", json.dumps(envelope())])
        self.assertEqual(code, 0)
        events = [json.loads(line) for line in out.getvalue().splitlines()]
        self.assertEqual(assert_one_terminal(self, events)["data"]["state"], "invalid_request")

    def test_runner_events_for_payload_routes_voice_away_from_registry(self) -> None:
        client = FakeClient([FakePage("https://chatgpt.com/c/live")])
        with allow(), \
                mock.patch.object(voice_job, "PlaywrightCdpClient", lambda *a: client), \
                mock.patch.object(runner, "_events_for_payload") as registry:
            events = runner.events_for_payload(envelope("deactivate"))
        registry.assert_not_called()
        self.assertEqual(assert_one_terminal(self, events)["type"], "completed")

    def test_daemon_refuses_voice_without_touching_engine_or_driver(self) -> None:
        engine_factory = mock.Mock()
        driver_factory = mock.Mock()
        daemon = WarmWorkerDaemon(driver_factory=driver_factory, engine_factory=engine_factory)
        events = list(daemon.run_job(envelope()))
        engine_factory.assert_not_called()
        driver_factory.assert_not_called()
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0]["type"], "failed")
        self.assertEqual(events[0]["trace_id"], "trace_voice_1")
        self.assertEqual(events[0]["data"]["state"], "unsupported_runner")
        self.assertIn("per-job runner", events[0]["data"]["reason"])
        self.assertIs(events[0]["data"]["retryable"], False)

    def test_voice_job_never_reaches_live_session_engine(self) -> None:
        with allow(), \
                mock.patch.object(voice_job, "PlaywrightCdpClient", lambda *a: FakeClient()), \
                mock.patch.object(daemon_module, "LiveSessionEngine") as engine_cls:
            list(runner.events_for_payload(envelope()))
            list(WarmWorkerDaemon().run_job(envelope()))
        engine_cls.assert_not_called()


if __name__ == "__main__":
    unittest.main()

"""Voice runner tests — all externals faked, no browser, no network."""

from __future__ import annotations

import contextlib
import dataclasses
import io
import json
import sys
import types
import unittest
from pathlib import Path
from unittest import mock

from ubag_worker.live.selectors import PROVIDER_SELECTORS, VoiceReadiness
from ubag_worker.voice import voice_runner
from ubag_worker.voice.voice_runner import (
    EXIT_CODES,
    ContextSelectionError,
    PlaywrightCdpClient,
    VoiceActivationResult,
    VoiceControlDrift,
    _matches_home,
    activate_voice,
    click_voice_control,
    deactivate_voice,
    find_provider_tab,
    pick_context,
    resolve_selectors,
)


class FakeElement:
    def __init__(self, aria_label: str = ""):
        self.aria_label = aria_label
        self.clicked = False

    def click(self) -> None:
        self.clicked = True


class FakePage:
    def __init__(self, url: str, elements: dict | None = None):
        self.url = url
        self.elements = elements or {}
        self.goto_calls: list[str] = []
        self.goto_error: Exception | None = None
        self.redirect_to: str | None = None
        self.on_goto = None  # optional hook, e.g. drop in-call controls

    def query_visible_element(self, selector: str):
        element = self.elements.get(selector)
        return element

    def goto(self, url: str) -> None:
        self.goto_calls.append(url)
        if self.goto_error is not None:
            raise self.goto_error
        self.url = self.redirect_to or url
        if self.on_goto is not None:
            self.on_goto(self)


class FakeCdpClient:
    def __init__(self, pages: list | None = None):
        self._pages = list(pages or [])
        self.opened_urls: list[str] = []

    def pages(self):
        return list(self._pages)

    def new_page(self, url: str):
        self.opened_urls.append(url)
        page = FakePage(url)
        self._pages.append(page)
        return page


def with_readiness(provider: str, **kwargs):
    """Patch a provider's voice_readiness group (selectors ship empty: no in-call capture)."""
    base = PROVIDER_SELECTORS[provider]
    patched = dataclasses.replace(base, voice_readiness=VoiceReadiness(**kwargs))
    return mock.patch.dict(PROVIDER_SELECTORS, {provider: patched})


def chatgpt_page(**extra) -> FakePage:
    elements = {"[aria-label='Start Voice']": FakeElement("Start Voice")}
    elements.update(extra)
    return FakePage("https://chatgpt.com/", elements)


FAST = dict(settle_ms=0, poll_ms=0, ready_timeout_ms=0)


class ClickVoiceControlTests(unittest.TestCase):
    def test_clicks_verified_chatgpt_voice_control(self) -> None:
        selectors = resolve_selectors("chatgpt_web")
        dictate = FakeElement("Dictate")
        start_voice = FakeElement("Start Voice")
        page = FakePage("https://chatgpt.com/", {
            "[aria-label='Start Voice']": start_voice,
            "[aria-label*='Start Voice']": start_voice,
        })
        # Even when a dictate control exists it must never be the one clicked.
        page.elements["[aria-label*='Voice mode']"] = dictate
        used = click_voice_control(page, selectors)
        self.assertEqual(used, "[aria-label='Start Voice']")
        self.assertTrue(start_voice.clicked)
        self.assertFalse(dictate.clicked)

    def test_clicks_verified_gemini_listen_control(self) -> None:
        selectors = resolve_selectors("gemini_web")
        listen = FakeElement("Listen")
        page = FakePage("https://gemini.google.com/app", {
            "[aria-label='Listen']": listen,
        })
        used = click_voice_control(page, selectors)
        self.assertEqual(used, "[aria-label='Listen']")
        self.assertTrue(listen.clicked)

    def test_drift_raises_when_nothing_matches(self) -> None:
        selectors = resolve_selectors("chatgpt_web")
        page = FakePage("https://chatgpt.com/", {})
        with self.assertRaises(VoiceControlDrift):
            click_voice_control(page, selectors)

    def test_all_dictate_matches_is_drift_not_a_click(self) -> None:
        selectors = resolve_selectors("chatgpt_web")
        dictate = FakeElement("Dictate")
        page = FakePage("https://chatgpt.com/", {
            "[aria-label*='Voice mode']": dictate,
        })
        with self.assertRaises(VoiceControlDrift):
            click_voice_control(page, selectors)
        self.assertFalse(dictate.clicked)


class ActivateVoiceTests(unittest.TestCase):
    def test_reuses_existing_provider_tab(self) -> None:
        client = FakeCdpClient([chatgpt_page()])
        result = activate_voice(client, "chatgpt_web", **FAST)
        # Click landed, but no in-call evidence is configured: never claim activated.
        self.assertEqual(result.state, "unverified_ready")
        self.assertFalse(result.activated)
        self.assertEqual(result.clicked_selector, "[aria-label='Start Voice']")
        self.assertEqual(client.opened_urls, [])
        self.assertFalse(result.details["opened_tab"])

    def test_opens_tab_when_none_open(self) -> None:
        client = FakeCdpClient([])

        def make_page(url: str) -> FakePage:
            client.opened_urls.append(url)
            page = FakePage(url, {
                "[aria-label='Start Voice']": FakeElement("Start Voice"),
            })
            client._pages.append(page)
            return page

        client.new_page = lambda url: make_page(url)  # type: ignore[method-assign]
        result = activate_voice(client, "chatgpt_web", **FAST)
        self.assertEqual(result.state, "unverified_ready")
        self.assertEqual(client.opened_urls, ["https://chatgpt.com/"])

    def test_login_wall_fails_closed_without_clicking(self) -> None:
        page = FakePage("https://chatgpt.com/auth/login", {
            "button[data-testid='login-button']": FakeElement("Log in"),
            "[aria-label='Start Voice']": (voice := FakeElement("Start Voice")),
        })
        client = FakeCdpClient([page])
        result = activate_voice(client, "chatgpt_web", **FAST)
        self.assertFalse(result.activated)
        self.assertTrue(result.login_required)
        self.assertEqual(result.state, "login_wall")
        self.assertEqual(result.reason, "manual_login_required")
        self.assertFalse(voice.clicked)

    def test_unsupported_provider_never_touches_cdp(self) -> None:
        client = FakeCdpClient([])
        result = activate_voice(client, "deepseek_web", **FAST)
        self.assertFalse(result.activated)
        self.assertEqual(result.state, "voice_unavailable")
        self.assertEqual(result.reason, "voice_not_supported_by_target")
        self.assertEqual(client.opened_urls, [])

    def test_selector_drift_state(self) -> None:
        client = FakeCdpClient([FakePage("https://chatgpt.com/")])
        result = activate_voice(client, "chatgpt_web", **FAST)
        self.assertEqual(result.state, "selector_drift")
        self.assertFalse(result.activated)

    def test_result_shape(self) -> None:
        result = VoiceActivationResult(provider="chatgpt_web", activated=True, reason="voice_activated")
        payload = result.as_dict()
        for key in ("provider", "state", "activated", "reason", "clicked_selector",
                    "login_required", "tab_url", "elapsed_ms", "details"):
            self.assertIn(key, payload)
        self.assertEqual(payload["state"], "activated")


class ReadinessTests(unittest.TestCase):
    """Post-click readiness is derived from voice_readiness selector groups."""

    def test_in_call_control_means_activated(self) -> None:
        page = chatgpt_page(**{"[data-test='end-call']": FakeElement("End")})
        with with_readiness("chatgpt_web", ready_controls=("[data-test='end-call']",)):
            result = activate_voice(FakeCdpClient([page]), "chatgpt_web", **FAST)
        self.assertEqual(result.state, "activated")
        self.assertTrue(result.activated)

    def test_ready_control_never_appearing_times_out(self) -> None:
        with with_readiness("chatgpt_web", ready_controls=("[data-test='end-call']",)):
            result = activate_voice(FakeCdpClient([chatgpt_page()]), "chatgpt_web", **FAST)
        self.assertEqual(result.state, "timeout")
        self.assertFalse(result.activated)

    def test_setup_prompt_blocks_activation(self) -> None:
        page = chatgpt_page(**{"[data-test='setup']": FakeElement("Set up voice")})
        with with_readiness("chatgpt_web", ready_controls=("[data-test='end-call']",),
                            setup_prompts=("[data-test='setup']",)):
            result = activate_voice(FakeCdpClient([page]), "chatgpt_web", **FAST)
        self.assertEqual(result.state, "setup_prompt")

    def test_unavailable_notice_means_voice_unavailable(self) -> None:
        page = chatgpt_page(**{"[data-test='nope']": FakeElement("Not available")})
        with with_readiness("chatgpt_web", unavailable=("[data-test='nope']",)):
            result = activate_voice(FakeCdpClient([page]), "chatgpt_web", **FAST)
        self.assertEqual(result.state, "voice_unavailable")

    def test_shipped_selectors_carry_no_invented_ready_controls(self) -> None:
        # No in-call DOM was ever captured; guessing selectors is forbidden.
        for provider in ("chatgpt_web", "gemini_web"):
            readiness = resolve_selectors(provider).voice_readiness
            self.assertEqual(tuple(readiness.ready_controls), ())
            self.assertEqual(readiness.baseline_version, "unverified-until-live-probe")


class CapturedDomTests(unittest.TestCase):
    """Real read-only voice-probe captures (tools/provider-refresh/captures) drive the runner.

    These are PRE-call captures: they prove the entry control and the honest
    evidence gaps, not in-call readiness (that needs a human-run
    `voice-probe.mjs --in-call`, see docs/perf-fleet/voice-activation-probe.md).
    """

    CAPTURES = Path(__file__).resolve().parents[3] / "tools" / "provider-refresh" / "captures"

    def page_from_capture(self, name: str, url: str) -> FakePage:
        capture = json.loads((self.CAPTURES / name).read_text(encoding="utf8"))
        elements = {
            f"[aria-label='{c['aria_label']}']": FakeElement(c["aria_label"])
            for c in capture["voiceish_controls"] if c.get("visible") and c.get("aria_label")
        }
        return FakePage(url, elements)

    def test_chatgpt_capture_has_entry_control_but_no_in_call_evidence(self) -> None:
        page = self.page_from_capture("chatgpt_web-voice-2026-10-05T03-44-17-319Z.json", "https://chatgpt.com/")
        result = activate_voice(FakeCdpClient([page]), "chatgpt_web", **FAST)
        self.assertEqual(result.state, "unverified_ready")
        self.assertTrue(page.elements["[aria-label='Start Voice']"].clicked)
        self.assertFalse(page.elements["[aria-label='Dictate']"].clicked)

    def test_gemini_capture_lacks_listen_so_runner_reports_drift(self) -> None:
        # Evidence gap: the 2026-10-05 Gemini capture never listed "Listen".
        page = self.page_from_capture("gemini_web-voice-2026-10-05T03-44-17-991Z.json", "https://gemini.google.com/app")
        with self.assertRaises(VoiceControlDrift):
            click_voice_control(page, resolve_selectors("gemini_web"))


class TabOpeningTests(unittest.TestCase):
    def test_new_tab_that_lands_off_provider_is_never_clicked(self) -> None:
        client = FakeCdpClient([])
        voice = FakeElement("Start Voice")
        client.new_page = lambda url: FakePage(  # type: ignore[method-assign]
            "https://accounts.example/login", {"[aria-label='Start Voice']": voice})
        result = activate_voice(client, "chatgpt_web", **FAST)
        self.assertEqual(result.state, "attach_failed")
        self.assertEqual(result.reason, "provider_url_mismatch")
        self.assertFalse(voice.clicked)

    def test_new_page_failure_is_attach_failed(self) -> None:
        client = FakeCdpClient([])

        def boom(url: str):
            raise TimeoutError("goto timed out")

        client.new_page = boom  # type: ignore[method-assign]
        result = activate_voice(client, "chatgpt_web", **FAST)
        self.assertEqual(result.state, "attach_failed")
        self.assertIn("goto timed out", result.details["error"])


class UrlMatchingTests(unittest.TestCase):
    def test_lookalike_and_bypass_urls_are_rejected(self) -> None:
        for url in (
            "https://chatgpt.com.evil.example/",
            "https://evilchatgpt.com/",
            "https://chatgpt.com@evil.example/",
            "https://evil@chatgpt.com/",
            "https://user:pw@chatgpt.com/",
            "http://chatgpt.com/",
            "https://chatgpt.com:8443/",
            "https://chatgpt.com.:443@evil.example/",
            "https://unrelated.example/?provider=chatgpt_web",
            "https://unrelated.example/chatgpt_web",
            "https://sub.chatgpt.com/",
            "about:blank",
            "",
            None,
        ):
            with self.subTest(url=url):
                self.assertFalse(_matches_home(url, "chatgpt_web"))

    def test_real_provider_urls_match(self) -> None:
        self.assertTrue(_matches_home("https://chatgpt.com/", "chatgpt_web"))
        self.assertTrue(_matches_home("https://chatgpt.com/c/abc?x=1", "chatgpt_web"))
        self.assertTrue(_matches_home("https://CHATGPT.com:443/", "chatgpt_web"))
        self.assertTrue(_matches_home("https://gemini.google.com/app", "gemini_web"))
        self.assertFalse(_matches_home("https://gemini.google.com/app", "chatgpt_web"))
        self.assertFalse(_matches_home("https://chatgpt.com/", "unknown_provider"))

    def test_find_provider_tab_ignores_provider_id_in_query(self) -> None:
        decoy = FakePage("https://unrelated.example/?provider=chatgpt_web")
        lookalike = FakePage("https://chatgpt.com.evil.example/")
        real = FakePage("https://chatgpt.com/c/1")
        self.assertIs(find_provider_tab(FakeCdpClient([decoy, lookalike, real]), "chatgpt_web"), real)
        self.assertIsNone(find_provider_tab(FakeCdpClient([decoy, lookalike]), "chatgpt_web"))


# --- fake playwright (injected via sys.modules; no browser needed) -----------

class FakePwPage:
    def __init__(self) -> None:
        self.url = "about:blank"
        self.goto_calls: list[tuple] = []
        self.closed = False
        self.redirect_to: str | None = None
        self.goto_error: Exception | None = None

    def goto(self, url, timeout=None, wait_until=None):
        self.goto_calls.append((url, timeout, wait_until))
        if self.goto_error is not None:
            raise self.goto_error
        self.url = self.redirect_to or url

    def close(self) -> None:
        self.closed = True


class FakePwContext:
    def __init__(self) -> None:
        self.pages: list[FakePwPage] = []
        self.created: list[FakePwPage] = []

    def new_page(self) -> FakePwPage:
        page = FakePwPage()
        self.created.append(page)
        return page


class FakePwBrowser:
    def __init__(self, n_contexts: int) -> None:
        self.contexts = [FakePwContext() for _ in range(n_contexts)]
        self.closed = False

    def close(self) -> None:
        self.closed = True


def fake_playwright(browser: FakePwBrowser):
    pw = types.SimpleNamespace(
        chromium=types.SimpleNamespace(connect_over_cdp=lambda endpoint: browser),
        stop=lambda: None,
    )
    sync_api = types.ModuleType("playwright.sync_api")
    sync_api.sync_playwright = lambda: types.SimpleNamespace(start=lambda: pw)
    return mock.patch.dict(sys.modules, {
        "playwright": types.ModuleType("playwright"),
        "playwright.sync_api": sync_api,
    })


class PlaywrightClientTests(unittest.TestCase):
    def test_new_page_actually_navigates_and_reports_real_url(self) -> None:
        browser = FakePwBrowser(1)
        with fake_playwright(browser):
            client = PlaywrightCdpClient("http://cdp")
            ctx = browser.contexts[0]
            ctx.new_page = lambda: ctx.created.append(FakePwPage()) or ctx.created[-1]  # type: ignore[method-assign]
            page = client.new_page("https://chatgpt.com/")
        raw = ctx.created[0]
        self.assertEqual(len(raw.goto_calls), 1)
        url, timeout, _ = raw.goto_calls[0]
        self.assertEqual(url, "https://chatgpt.com/")
        self.assertTrue(timeout and timeout <= 60000, "navigation must be time-bounded")
        self.assertEqual(page.url, "https://chatgpt.com/")

    def test_new_page_reports_post_redirect_url_not_requested_url(self) -> None:
        browser = FakePwBrowser(1)
        with fake_playwright(browser):
            client = PlaywrightCdpClient("http://cdp")
            ctx = browser.contexts[0]
            raw = FakePwPage()
            raw.redirect_to = "https://auth.example/login"
            ctx.new_page = lambda: raw  # type: ignore[method-assign]
            page = client.new_page("https://chatgpt.com/")
        self.assertEqual(page.url, "https://auth.example/login")
        self.assertFalse(_matches_home(page.url, "chatgpt_web"))

    def test_failed_navigation_closes_blank_tab_and_raises(self) -> None:
        browser = FakePwBrowser(1)
        with fake_playwright(browser):
            client = PlaywrightCdpClient("http://cdp")
            ctx = browser.contexts[0]
            raw = FakePwPage()
            raw.goto_error = TimeoutError("nav timeout")
            ctx.new_page = lambda: raw  # type: ignore[method-assign]
            with self.assertRaises(TimeoutError):
                client.new_page("https://chatgpt.com/")
        self.assertTrue(raw.closed)


class ContextSelectionTests(unittest.TestCase):
    def test_single_context_is_used_when_unspecified(self) -> None:
        self.assertEqual(pick_context(["only"]), "only")

    def test_multiple_contexts_without_selector_fail_closed(self) -> None:
        with self.assertRaises(ContextSelectionError) as cm:
            pick_context(["a", "b"])
        self.assertIn("context_ambiguous", str(cm.exception))

    def test_no_context_fails_closed(self) -> None:
        with self.assertRaises(ContextSelectionError):
            pick_context([])

    def test_selector_picks_matching_context(self) -> None:
        self.assertEqual(pick_context(["a", "b", "c"], "1"), "b")

    def test_bad_selector_fails_closed(self) -> None:
        for bad in ("7", "-1", "work-profile"):
            with self.subTest(bad=bad), self.assertRaises(ContextSelectionError):
                pick_context(["a", "b"], bad)

    def test_client_binds_selected_context(self) -> None:
        browser = FakePwBrowser(3)
        browser.contexts[2].pages.append(FakePwPage())
        with fake_playwright(browser):
            client = PlaywrightCdpClient("http://cdp", context_id="2")
        self.assertEqual(len(client.pages()), 1)

    def test_client_refuses_ambiguous_contexts(self) -> None:
        with fake_playwright(FakePwBrowser(2)):
            with self.assertRaises(ContextSelectionError):
                PlaywrightCdpClient("http://cdp")


class DeactivateVoiceTests(unittest.TestCase):
    def test_no_tab(self) -> None:
        result = deactivate_voice(FakeCdpClient([]), "chatgpt_web")
        self.assertEqual(result.state, "no_tab")
        self.assertFalse(result.activated)

    def test_navigates_home_and_reports_deactivated(self) -> None:
        page = FakePage("https://chatgpt.com/c/live-call")
        result = deactivate_voice(FakeCdpClient([page]), "chatgpt_web", poll_ms=0, ready_timeout_ms=0)
        self.assertEqual(page.goto_calls, ["https://chatgpt.com/"])
        self.assertEqual(result.state, "deactivated")
        self.assertEqual(result.details["verified_by"], "navigation_only")

    def test_never_clicks_anything(self) -> None:
        voice = FakeElement("Start Voice")
        page = FakePage("https://chatgpt.com/", {"[aria-label='Start Voice']": voice})
        deactivate_voice(FakeCdpClient([page]), "chatgpt_web", poll_ms=0, ready_timeout_ms=0)
        self.assertFalse(voice.clicked)

    def test_verifies_in_call_controls_gone(self) -> None:
        page = FakePage("https://chatgpt.com/", {"[data-test='end-call']": FakeElement("End")})
        page.on_goto = lambda p: p.elements.clear()
        with with_readiness("chatgpt_web", ready_controls=("[data-test='end-call']",)):
            result = deactivate_voice(FakeCdpClient([page]), "chatgpt_web", poll_ms=0, ready_timeout_ms=0)
        self.assertEqual(result.state, "deactivated")
        self.assertEqual(result.details["verified_by"], "ready_controls_absent")

    def test_voice_still_active_is_not_reported_deactivated(self) -> None:
        page = FakePage("https://chatgpt.com/", {"[data-test='end-call']": FakeElement("End")})
        with with_readiness("chatgpt_web", ready_controls=("[data-test='end-call']",)):
            result = deactivate_voice(FakeCdpClient([page]), "chatgpt_web", poll_ms=0, ready_timeout_ms=0)
        self.assertEqual(result.state, "timeout")

    def test_navigation_failure_is_attach_failed(self) -> None:
        page = FakePage("https://chatgpt.com/")
        page.goto_error = RuntimeError("target closed")
        result = deactivate_voice(FakeCdpClient([page]), "chatgpt_web", poll_ms=0, ready_timeout_ms=0)
        self.assertEqual(result.state, "attach_failed")

    def test_redirect_off_provider_is_attach_failed(self) -> None:
        page = FakePage("https://chatgpt.com/")
        page.redirect_to = "https://chatgpt.com.evil.example/"
        result = deactivate_voice(FakeCdpClient([page]), "chatgpt_web", poll_ms=0, ready_timeout_ms=0)
        self.assertEqual(result.state, "attach_failed")


class CliTests(unittest.TestCase):
    def run_main(self, argv, client=None, attach_error=None):
        def factory(endpoint, context_id=None):
            if attach_error is not None:
                raise attach_error
            factory.context_id = context_id
            return client

        factory.context_id = None
        out = io.StringIO()
        with mock.patch.object(voice_runner, "PlaywrightCdpClient", factory), \
                contextlib.redirect_stdout(out):
            code = voice_runner.main(argv)
        return code, out.getvalue(), factory

    class Closable(FakeCdpClient):
        def close(self) -> None:
            pass

    def test_exit_codes_are_distinct_per_failure_state(self) -> None:
        codes = {s: c for s, c in EXIT_CODES.items() if c != 0}
        self.assertEqual(len(set(codes.values())), len(codes))
        # existing codes are preserved
        self.assertEqual((EXIT_CODES["attach_failed"], EXIT_CODES["login_wall"],
                          EXIT_CODES["selector_drift"]), (2, 3, 4))

    def test_attach_failure_exits_2_with_state(self) -> None:
        code, out, _ = self.run_main(
            ["--provider", "chatgpt_web", "--cdp", "http://x", "--json"],
            attach_error=ContextSelectionError("context_ambiguous: 2 contexts open"))
        self.assertEqual(code, 2)
        self.assertEqual(json.loads(out)["state"], "attach_failed")

    def test_context_flag_is_forwarded(self) -> None:
        client = self.Closable([FakePage("https://chatgpt.com/")])
        _, _, factory = self.run_main(
            ["--provider", "chatgpt_web", "--cdp", "http://x", "--context", "1"], client=client)
        self.assertEqual(factory.context_id, "1")

    def test_activate_unverified_ready_exit_code(self) -> None:
        client = self.Closable([chatgpt_page()])
        with mock.patch.object(voice_runner.time, "sleep"):
            code, out, _ = self.run_main(
                ["--provider", "chatgpt_web", "--cdp", "http://x", "--json"], client=client)
        self.assertEqual(code, EXIT_CODES["unverified_ready"])
        self.assertEqual(json.loads(out)["state"], "unverified_ready")

    def test_deactivate_action(self) -> None:
        page = FakePage("https://chatgpt.com/c/x")
        client = self.Closable([page])
        code, out, _ = self.run_main(
            ["--provider", "chatgpt_web", "--cdp", "http://x", "--action", "deactivate", "--json"],
            client=client)
        self.assertEqual(code, 0)
        self.assertEqual(json.loads(out)["state"], "deactivated")
        self.assertEqual(page.goto_calls, ["https://chatgpt.com/"])

    def test_deactivate_without_tab_exit_code(self) -> None:
        code, _, _ = self.run_main(
            ["--provider", "chatgpt_web", "--cdp", "http://x", "--action", "deactivate"],
            client=self.Closable([]))
        self.assertEqual(code, EXIT_CODES["no_tab"])


if __name__ == "__main__":
    unittest.main()

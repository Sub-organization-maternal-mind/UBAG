"""Voice runner tests — all externals faked, no browser, no network."""

from __future__ import annotations

import unittest

from ubag_worker.voice.voice_runner import (
    VoiceActivationResult,
    VoiceControlDrift,
    activate_voice,
    click_voice_control,
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

    def query_visible_element(self, selector: str):
        element = self.elements.get(selector)
        return element


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
        page = FakePage("https://chatgpt.com/", {
            "[aria-label='Start Voice']": FakeElement("Start Voice"),
        })
        client = FakeCdpClient([page])
        result = activate_voice(client, "chatgpt_web", settle_ms=0)
        self.assertTrue(result.activated)
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
        result = activate_voice(client, "chatgpt_web", settle_ms=0)
        self.assertTrue(result.activated)
        self.assertEqual(client.opened_urls, ["https://chatgpt.com/"])

    def test_login_wall_fails_closed_without_clicking(self) -> None:
        page = FakePage("https://chatgpt.com/auth/login", {
            "button[data-testid='login-button']": FakeElement("Log in"),
        })
        client = FakeCdpClient([page])
        result = activate_voice(client, "chatgpt_web", settle_ms=0)
        self.assertFalse(result.activated)
        self.assertTrue(result.login_required)
        self.assertEqual(result.reason, "manual_login_required")

    def test_unsupported_provider_never_touches_cdp(self) -> None:
        client = FakeCdpClient([])
        result = activate_voice(client, "deepseek_web", settle_ms=0)
        self.assertFalse(result.activated)
        self.assertEqual(result.reason, "voice_not_supported_by_target")
        self.assertEqual(client.opened_urls, [])

    def test_result_shape(self) -> None:
        result = VoiceActivationResult(provider="chatgpt_web", activated=True, reason="voice_activated")
        payload = result.as_dict()
        for key in ("provider", "activated", "reason", "clicked_selector",
                    "login_required", "tab_url", "elapsed_ms", "details"):
            self.assertIn(key, payload)


if __name__ == "__main__":
    unittest.main()

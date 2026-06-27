"""Unit tests for safe provider SPA navigation abort handling."""

import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "apps" / "worker"))

from ubag_worker.live.page_driver import (  # noqa: E402
    _is_human_verification_overlay,
    _is_tolerable_navigation_abort,
    _looks_like_manual_overlay,
)


class NavigationAbortTests(unittest.TestCase):
    def test_same_origin_chromium_abort_is_tolerated(self) -> None:
        exc = RuntimeError("Page.goto: net::ERR_ABORTED at https://gemini.google.com/app")
        self.assertTrue(
            _is_tolerable_navigation_abort(
                exc,
                "https://gemini.google.com/app",
                "https://gemini.google.com/app",
            )
        )

    def test_same_origin_firefox_abort_is_tolerated(self) -> None:
        exc = RuntimeError("Page.goto: NS_BINDING_ABORTED")
        self.assertTrue(
            _is_tolerable_navigation_abort(
                exc,
                "https://chat.deepseek.com/a/chat",
                "https://chat.deepseek.com/",
            )
        )

    def test_cross_origin_abort_is_not_tolerated(self) -> None:
        exc = RuntimeError("Page.goto: net::ERR_ABORTED")
        self.assertFalse(
            _is_tolerable_navigation_abort(
                exc,
                "https://accounts.google.com/",
                "https://gemini.google.com/app",
            )
        )

    def test_non_abort_error_is_not_tolerated(self) -> None:
        exc = RuntimeError("Page.goto: net::ERR_NAME_NOT_RESOLVED")
        self.assertFalse(
            _is_tolerable_navigation_abort(
                exc,
                "https://gemini.google.com/app",
                "https://gemini.google.com/app",
            )
        )

    def test_cookie_overlay_click_interception_requires_manual_action(self) -> None:
        exc = RuntimeError(
            "Locator.click: Timeout 30000ms exceeded. "
            "cookie-purpose-list intercepts pointer events from cdk-overlay-container"
        )
        self.assertTrue(_looks_like_manual_overlay(exc))

    def test_plain_click_timeout_is_not_manual_overlay(self) -> None:
        exc = RuntimeError("Locator.click: Timeout 30000ms exceeded.")
        self.assertFalse(_looks_like_manual_overlay(exc))

    def test_captcha_is_human_verification_not_benign_overlay(self) -> None:
        # CAPTCHA / identity challenge: defer to the human, NEVER auto-dismiss.
        exc = RuntimeError(
            "Locator.click: Timeout 30000ms exceeded. recaptcha-anchor "
            "intercepts pointer events (please verify you are not a robot)"
        )
        self.assertTrue(_is_human_verification_overlay(exc))
        self.assertFalse(_looks_like_manual_overlay(exc))

    def test_cookie_banner_is_benign_not_human_verification(self) -> None:
        # Benign consent popup: the system auto-dismisses it, no human needed.
        exc = RuntimeError(
            "Locator.click: Timeout 30000ms exceeded. "
            "cookie-purpose-list intercepts pointer events from cdk-overlay-container"
        )
        self.assertTrue(_looks_like_manual_overlay(exc))
        self.assertFalse(_is_human_verification_overlay(exc))

    def test_plain_timeout_is_neither_overlay_class(self) -> None:
        exc = RuntimeError("Locator.click: Timeout 30000ms exceeded.")
        self.assertFalse(_is_human_verification_overlay(exc))
        self.assertFalse(_looks_like_manual_overlay(exc))


if __name__ == "__main__":
    unittest.main()

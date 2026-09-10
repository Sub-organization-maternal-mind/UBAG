"""Tests for the live driver's per-job latency fixes.

Pins the three behaviours measured on the VPS to cost 12-24s per job:

* a warm tab is NOT fully reloaded every job (only every N, to bound SPA memory);
  New chat is an in-page transition and the page must still be PROVEN empty;
* the engine's start_new_chat right after prepare_for_next_job is a no-op
  (the page was just put on a fresh chat) instead of a second click + settle;
* a turn completes as soon as the provider's own Stop control, seen during the
  turn, disappears (plus a short grace) -- the 4s reasoning settle stays as the
  fallback when no indicator was ever seen.
"""
import time

from ubag_worker.live import page_driver as pd
from ubag_worker.live.page_driver import PlaywrightPageDriver
from ubag_worker.live.selectors import CHATGPT_WEB, GEMINI_WEB

_SUFFIX = " >> visible=true"


def _base(selector: str) -> str:
    return selector[: -len(_SUFFIX)] if selector.endswith(_SUFFIX) else selector


class _Loc:
    def __init__(self, count: int, on_click=None) -> None:
        self._count = count
        self._on_click = on_click

    @property
    def first(self):
        return self

    @property
    def last(self):
        return self

    def all(self):
        return [self] if self._count else []

    def count(self):
        return self._count

    def wait_for(self, **_kw):
        if not self._count:
            raise RuntimeError("not visible")

    def click(self, **_kw):
        if not self._count:
            raise RuntimeError("not visible")
        if self._on_click:
            self._on_click()

    def inner_text(self, **_kw):
        return self.text

    def fill(self, _text):
        pass

    text = ""


class _WarmPage:
    """A logged-in provider tab that still shows the previous job's answer until
    New chat is clicked (an SPA transition, so it clears after `clears_after`
    polls)."""

    def __init__(self, selectors, *, clears_after: int = 1, closed: bool = False) -> None:
        self._prompt = set(selectors.prompt_input.as_list())
        self._submit = set(selectors.submit_button.as_list())
        self._new_chat = set(selectors.new_chat.as_list())
        self._response = set(selectors.response_container.as_list())
        self._clears_after = clears_after
        self._closed = closed
        self.prior_turn_visible = True
        self.goto_calls = []
        self.new_chat_clicks = 0

    def is_closed(self):
        return self._closed

    def goto(self, url, **_kw):
        self.goto_calls.append(url)
        self.prior_turn_visible = False

    def wait_for_timeout(self, _ms):
        pass

    def _clicked_new_chat(self):
        self.new_chat_clicks += 1
        self._pending = self._clears_after

    def locator(self, selector):
        selector = _base(selector)
        if selector in self._prompt or selector in self._submit:
            return _Loc(1)
        if selector in self._new_chat:
            return _Loc(1, on_click=self._clicked_new_chat)
        if selector in self._response:
            if self.prior_turn_visible and self.new_chat_clicks:
                self._pending -= 1
                if self._pending < 0:
                    self.prior_turn_visible = False
            return _Loc(1 if self.prior_turn_visible else 0)
        return _Loc(0)


def _driver(page) -> PlaywrightPageDriver:
    driver = PlaywrightPageDriver()
    driver._page = page
    return driver


class TestWarmPageIsNotReloadedEveryJob:
    def test_new_chat_transition_replaces_the_full_reload(self, monkeypatch):
        monkeypatch.setenv("UBAG_WARM_RELOAD_EVERY", "3")
        page = _WarmPage(CHATGPT_WEB)
        driver = _driver(page)

        assert driver.prepare_for_next_job(CHATGPT_WEB) is True
        assert page.goto_calls == []  # no reload: New chat cleared the page
        assert page.new_chat_clicks == 1
        assert page.prior_turn_visible is False

    def test_full_reload_still_happens_every_nth_job(self, monkeypatch):
        monkeypatch.setenv("UBAG_WARM_RELOAD_EVERY", "3")
        page = _WarmPage(CHATGPT_WEB)
        driver = _driver(page)

        for _ in range(3):
            page.prior_turn_visible = True
            assert driver.prepare_for_next_job(CHATGPT_WEB) is True
        assert page.goto_calls == [CHATGPT_WEB.target_url]  # 3rd job reloaded

    def test_refuses_reuse_when_the_prior_turn_never_clears(self, monkeypatch):
        """The safety property: a page that cannot be PROVEN empty goes cold."""
        monkeypatch.setenv("UBAG_EMPTINESS_PROBE_MS", "200")
        page = _WarmPage(GEMINI_WEB, clears_after=10_000)
        driver = _driver(page)

        assert driver.prepare_for_next_job(GEMINI_WEB) is False
        assert driver._fresh_chat is False

    def test_engine_start_new_chat_is_skipped_right_after_prepare(self):
        page = _WarmPage(CHATGPT_WEB)
        driver = _driver(page)
        assert driver.prepare_for_next_job(CHATGPT_WEB) is True

        assert driver.start_new_chat(CHATGPT_WEB) is True  # consumed the flag
        assert page.new_chat_clicks == 1  # no second click
        driver.start_new_chat(CHATGPT_WEB)
        assert page.new_chat_clicks == 2  # flag was one-shot

    def test_submit_clears_the_fresh_flag(self):
        driver = _driver(_WarmPage(CHATGPT_WEB))
        driver._fresh_chat = True

        driver.submit_prompt(CHATGPT_WEB, "hi")

        assert driver._fresh_chat is False


class _StreamPage:
    """Answer text fully rendered up front; the Stop control is visible for the
    first `stop_polls` indicator probes and then gone."""

    def __init__(self, selectors, *, stop_polls: int) -> None:
        self._indicator = set(selectors.streaming_indicator.as_list())
        self._response = set(selectors.response_container.as_list())
        self._stop_polls = stop_polls
        self.indicator_probes = 0

    def is_closed(self):
        return False

    def locator(self, selector):
        selector = _base(selector)
        if selector in self._indicator:
            self.indicator_probes += 1
            return _Loc(1 if self.indicator_probes <= self._stop_polls else 0)
        if selector in self._response:
            loc = _Loc(1)
            loc.text = "final answer"
            return loc
        return _Loc(0)


class TestProviderSignalledCompletion:
    def test_finishes_shortly_after_the_stop_control_disappears(self, monkeypatch):
        monkeypatch.setenv("UBAG_REASONING_SETTLE_S", "30")  # would take forever
        monkeypatch.setenv("UBAG_INDICATOR_GONE_GRACE_S", "0.3")
        page = _StreamPage(CHATGPT_WEB, stop_polls=3)  # 3 candidates -> 1 poll
        driver = _driver(page)
        driver._response_baseline = {}

        started = time.monotonic()
        text = "".join(driver.stream_response(CHATGPT_WEB, timeout_s=20))

        assert text == "final answer"
        assert time.monotonic() - started < 5  # not the 30s settle

    def test_without_an_indicator_the_settle_window_still_applies(self, monkeypatch):
        """Gemini-style (no indicator ever seen): completion must wait out the
        no-growth settle, never finish on the first read."""
        monkeypatch.setenv("UBAG_INDICATOR_GONE_GRACE_S", "0.01")
        page = _StreamPage(GEMINI_WEB, stop_polls=0)
        driver = PlaywrightPageDriver(response_settle_s=1.2)
        driver._page = page
        driver._response_baseline = {}

        started = time.monotonic()
        text = "".join(driver.stream_response(GEMINI_WEB, timeout_s=20))

        assert text == "final answer"
        assert time.monotonic() - started >= 1.2


class TestProbeBudgets:
    def test_tiny_timeouts_never_become_playwrights_infinite_zero(self):
        """timeout=0 means 'wait forever' to Playwright; a 0/tiny budget must
        still be a bounded probe."""
        calls = []

        class _Page:
            def locator(self, _s):
                class _L:
                    first = property(lambda s: s)

                    def wait_for(self, **kw):
                        calls.append(kw["timeout"])
                        raise RuntimeError("absent")

                return _L()

        driver = _driver(_Page())
        assert driver._present_any(["x"], timeout_ms=0) is False
        assert calls and min(calls) >= 1

    def test_reload_cadence_defaults_and_env(self, monkeypatch):
        monkeypatch.delenv("UBAG_WARM_RELOAD_EVERY", raising=False)
        assert pd._warm_reload_every() == pd._WARM_RELOAD_EVERY
        monkeypatch.setenv("UBAG_WARM_RELOAD_EVERY", "0")
        assert pd._warm_reload_every() == pd._WARM_RELOAD_EVERY
        monkeypatch.setenv("UBAG_WARM_RELOAD_EVERY", "4")
        assert pd._warm_reload_every() == 4


class _Match:
    """One visible match of a duplicated control. A covered match fails its
    click (Playwright's hit-target check) only after the full click timeout."""

    def __init__(self, name: str, *, covered: bool, log) -> None:
        self.name, self.covered, self._log = name, covered, log

    def evaluate(self, _js):
        return not self.covered

    def click(self, **_kw):
        self._log.append(self.name)
        if self.covered:
            raise RuntimeError("element intercepts pointer events")


class TestCoveredDuplicateIsNotClickedFirst:
    def test_hit_target_ok_match_is_tried_before_the_covered_one(self):
        clicks = []
        matches = [
            _Match("covered-icon", covered=True, log=clicks),
            _Match("sidebar-row", covered=False, log=clicks),
        ]

        class _Loc:
            first = property(lambda s: s)

            def wait_for(self, **_kw):
                pass

            def all(self):
                return list(matches)

        class _Page:
            def locator(self, _s):
                return _Loc()

        assert _driver(_Page())._click_any(["a.new-chat"], timeout_ms=4000) is True
        assert clicks == ["sidebar-row"]  # the covered match never cost a click

    def test_hit_test_failure_only_demotes_a_match(self):
        clicks = []

        class _Odd(_Match):
            def evaluate(self, _js):
                raise RuntimeError("detached")

        matches = [_Odd("odd", covered=False, log=clicks)]

        class _Loc:
            first = property(lambda s: s)

            def wait_for(self, **_kw):
                pass

            def all(self):
                return list(matches)

        class _Page:
            def locator(self, _s):
                return _Loc()

        assert _driver(_Page())._click_any(["a.new-chat"], timeout_ms=4000) is True
        assert clicks == ["odd"]  # still attempted, just not first


class TestAttachmentStateClearIsOneRoundTrip:
    class _FileInputs:
        def __init__(self, holding_files: bool, log) -> None:
            self._holding, self._log = holding_files, log

        def evaluate_all(self, _js):
            self._log.append("evaluate_all")
            return self._holding

        def all(self):
            return [self] * 5  # ChatGPT renders five file inputs

        def set_input_files(self, files):
            self._log.append(("set_input_files", files))

    def _page(self, holding_files: bool, log):
        inputs = self._FileInputs(holding_files, log)

        class _Page:
            def is_closed(self):
                return False

            def locator(self, _s):
                return inputs

        return _Page()

    def test_empty_inputs_are_not_cleared_one_by_one(self):
        log = []
        _driver(self._page(False, log)).clear_attachment_state()
        assert log == ["evaluate_all"]

    def test_inputs_holding_files_are_still_cleared(self):
        log = []
        _driver(self._page(True, log)).clear_attachment_state()
        assert log == ["evaluate_all"] + [("set_input_files", [])] * 5

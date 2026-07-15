"""Tests for warm-browser reuse safety (UBAG_WORKER_DAEMON).

UBAG powers a radiology reporting product, so the property under test is not
speed -- it is that a reused tab can NEVER return a prior patient's report. Every
test here pins one branch of that guarantee.

The gate deliberately reuses the already-verified, drift-baselined
``response_container`` as the emptiness probe rather than inventing a new
turn/emptiness selector: a guessed selector would silently match nothing, read
"empty", and let a prior turn bleed into the next job.
"""
import pytest

from ubag_worker.live.page_driver import (
    DriftDetectedError,
    MockPageDriver,
    PageDriver,
    PlaywrightPageDriver,
)
from ubag_worker.live.selectors import GEMINI_WEB


class _StubLocator:
    def __init__(self, visible: bool) -> None:
        self._visible = visible

    @property
    def first(self):
        return self

    def wait_for(self, **_kwargs):
        if not self._visible:
            raise RuntimeError("selector matched nothing")


class _StubPage:
    """Stands in for a live Playwright page; no browser is launched."""

    def __init__(self, *, visible: bool = False, closed: bool = False) -> None:
        self._visible = visible
        self._closed = closed

    def is_closed(self) -> bool:
        return self._closed

    def locator(self, _selector):
        return _StubLocator(self._visible)


class _BareDriver(PageDriver):
    """A driver that implements only the abstract surface -- no emptiness probe."""

    def open(self, *, target_url: str, user_data_dir: str, headless: bool) -> None:
        pass

    def current_url(self) -> str:
        return "about:blank"

    def detect_login_state(self, selectors):
        return "authenticated"

    def await_manual_login(self, selectors, *, timeout_s: float):
        return "authenticated"

    def submit_prompt(self, selectors, prompt: str) -> None:
        pass

    def stream_response(self, selectors, *, timeout_s: float):
        return iter(())

    def read_final_response(self, selectors, *, return_mode="text"):
        return ""

    def dom_signature(self, selectors) -> str:
        return ""

    def capture_screenshot(self, label: str):
        return None

    def close(self) -> None:
        pass


class TestEmptinessProbeDefault:
    def test_driver_without_a_probe_is_never_reused(self):
        """A driver that cannot prove emptiness must refuse reuse, not assume it.

        This is the fail-safe default: adding a new driver should not silently
        opt into warm reuse.
        """
        driver = _BareDriver()

        assert driver.prepare_for_next_job(GEMINI_WEB) is False


class TestPrepareForNextJob:
    def test_refuses_reuse_when_a_prior_turn_is_visible(self):
        """response_container present => a prior turn exists => rebuild cold."""
        driver = MockPageDriver()
        driver.open(target_url=GEMINI_WEB.target_url, user_data_dir="/tmp/p", headless=True)
        driver.response_container_visible = True

        assert driver.prepare_for_next_job(GEMINI_WEB) is False

    def test_allows_reuse_when_page_is_provably_empty(self):
        """response_container absent on a fresh chat => proven empty => reuse."""
        driver = MockPageDriver()
        driver.open(target_url=GEMINI_WEB.target_url, user_data_dir="/tmp/p", headless=True)
        driver.response_container_visible = False

        assert driver.prepare_for_next_job(GEMINI_WEB) is True

    def test_forces_a_fresh_chat_before_probing(self):
        """Reuse must start a new conversation, never continue the previous one."""
        driver = MockPageDriver()
        driver.open(target_url=GEMINI_WEB.target_url, user_data_dir="/tmp/p", headless=True)
        driver.response_container_visible = False

        driver.prepare_for_next_job(GEMINI_WEB)

        assert driver.started_new_chat is True

    def test_refuses_reuse_instead_of_raising_when_the_page_is_broken(self):
        """Any doubt => False (rebuild cold). The caller can always go cold, so a
        dead page must not surface as an exception that fails an otherwise-good job."""
        driver = MockPageDriver()
        driver.open(target_url=GEMINI_WEB.target_url, user_data_dir="/tmp/p", headless=True)
        driver.explode_on_probe = True

        assert driver.prepare_for_next_job(GEMINI_WEB) is False


class TestLiveDriverProbe:
    def test_reports_present_when_a_prior_turn_is_on_the_page(self):
        driver = PlaywrightPageDriver()
        driver._page = _StubPage(visible=True)

        assert driver.response_container_present(GEMINI_WEB) is True

    def test_reports_absent_when_no_container_matches(self):
        """Also the drift case: a drifted selector matches nothing and reads
        "absent" rather than raising -- survivable only because the later read
        raises DriftDetectedError against the same group."""
        driver = PlaywrightPageDriver()
        driver._page = _StubPage(visible=False)

        assert driver.response_container_present(GEMINI_WEB) is False


class TestLiveDriverOpenIsIdempotent:
    def test_open_does_not_reopen_an_already_live_page(self):
        """The daemon reuses one driver across jobs, but engine.iter_events calls
        open() on every job. Without a guard that re-attaches CDP and opens a NEW
        page each time, discarding the warm page and the whole point of reuse.

        user_data_dir="" would raise ValueError if open() ran its normal path, so
        this also proves the guard short-circuits before any launch work.
        """
        driver = PlaywrightPageDriver()
        live_page = _StubPage()
        driver._page = live_page

        driver.open(target_url="https://example.test", user_data_dir="", headless=True)

        assert driver._page is live_page

    def test_a_closed_page_is_not_treated_as_live(self):
        """A crashed/closed tab must not be reused forever -- the guard has to let
        open() rebuild it.

        Asserts the guard predicate rather than open()'s exception: which error
        open() raises depends on whether Playwright happens to be installed, so
        an exception-based test passes locally and fails on CI for reasons that
        have nothing to do with this behaviour.
        """
        driver = PlaywrightPageDriver()
        driver._page = _StubPage(closed=True)

        assert driver._page_is_live() is False

    def test_a_missing_page_is_not_treated_as_live(self):
        driver = PlaywrightPageDriver()

        assert driver._page_is_live() is False

    def test_an_unqueryable_page_is_not_treated_as_live(self):
        """A page whose CDP call throws is a corpse, not a warm page."""

        class _DeadPage:
            def is_closed(self):
                raise RuntimeError("connection lost")

        driver = PlaywrightPageDriver()
        driver._page = _DeadPage()

        assert driver._page_is_live() is False


class TestDriftIsFailLoudNotWrongPatient:
    def test_drifted_container_fails_the_job_rather_than_returning_a_prior_answer(self):
        """The worst case, pinned.

        If response_container ever drifts, the probe matches nothing and reads
        "empty", so the gate wrongly permits reuse. That is survivable ONLY
        because the same drifted selector makes the later read raise
        DriftDetectedError -- the job fails loudly instead of returning whatever
        the previous patient's turn left on the page.
        """
        driver = MockPageDriver(drift_group=GEMINI_WEB.response_container.name)
        driver.open(target_url=GEMINI_WEB.target_url, user_data_dir="/tmp/p", headless=True)
        driver.response_container_visible = True  # a prior turn IS on the page

        # Drift makes the probe read "absent", so reuse is (wrongly) permitted...
        assert driver.prepare_for_next_job(GEMINI_WEB) is True

        # ...but the read that would return the answer refuses to guess.
        with pytest.raises(DriftDetectedError):
            driver.read_final_response(GEMINI_WEB)

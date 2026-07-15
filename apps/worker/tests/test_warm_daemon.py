"""Tests for the warm-browser worker daemon (Layer B).

The daemon keeps PageDrivers alive between jobs so a job stops paying to
re-attach CDP and cold-load the provider SPA. Everything here pins WHEN a driver
may be reused -- reuse is only ever safe when the gate proves the page carries no
prior conversation turn.
"""
import pytest

from ubag_worker.live.daemon import WarmWorkerDaemon
from ubag_worker.live.page_driver import MockPageDriver


class _RecordingFactory:
    """Hands out MockPageDrivers and records how many were built."""

    def __init__(self, *, reusable: bool = True) -> None:
        self.built = []
        self._reusable = reusable

    def __call__(self, options):
        driver = MockPageDriver()
        # response_container absent => prepare_for_next_job() can prove the page
        # empty and permit reuse. Visible => it must refuse.
        driver.response_container_visible = not self._reusable
        self.built.append(driver)
        return driver


class _FakeEngine:
    """Stands in for LiveSessionEngine: records the injected driver."""

    seen_drivers = []
    raise_on_run = False

    def __init__(self, selectors):
        self._selectors = selectors

    def iter_events(self, payload, *, driver=None):
        type(self).seen_drivers.append(driver)
        if type(self).raise_on_run:
            raise RuntimeError("provider blew up mid-job")
        yield {"event_type": "completed", "data": {"ok": True}}


@pytest.fixture(autouse=True)
def _reset_fake_engine():
    _FakeEngine.seen_drivers = []
    _FakeEngine.raise_on_run = False
    yield


def _payload(profile="/profiles/gemini"):
    return {"job": {"target": "gemini_web"}, "user_data_dir": profile}


def _daemon(factory):
    return WarmWorkerDaemon(driver_factory=factory, engine_factory=_FakeEngine)


class TestWarmReuse:
    def test_first_job_builds_a_driver(self):
        factory = _RecordingFactory()
        daemon = _daemon(factory)

        list(daemon.run_job(_payload()))

        assert len(factory.built) == 1

    def test_second_job_reuses_the_same_warm_driver(self):
        """The whole point: no second driver, and the engine gets the warm one."""
        factory = _RecordingFactory(reusable=True)
        daemon = _daemon(factory)

        list(daemon.run_job(_payload()))
        list(daemon.run_job(_payload()))

        assert len(factory.built) == 1
        assert _FakeEngine.seen_drivers[0] is _FakeEngine.seen_drivers[1]

    def test_driver_is_injected_so_the_engine_does_not_close_it(self):
        """engine.iter_events(driver=...) sets owns_driver=False, which is what
        keeps the page alive between jobs."""
        factory = _RecordingFactory()
        daemon = _daemon(factory)

        list(daemon.run_job(_payload()))

        assert _FakeEngine.seen_drivers[0] is factory.built[0]

    def test_refuses_reuse_and_rebuilds_when_a_prior_turn_is_visible(self):
        """Gate says no => discard the warm driver and go cold. Slower, correct."""
        factory = _RecordingFactory(reusable=False)
        daemon = _daemon(factory)

        list(daemon.run_job(_payload()))
        list(daemon.run_job(_payload()))

        assert len(factory.built) == 2
        assert factory.built[0].closed is True


class TestIsolation:
    def test_different_profiles_never_share_a_driver(self):
        """A warm page belongs to one identity; sharing it across profiles would
        cross-contaminate sessions."""
        factory = _RecordingFactory()
        daemon = _daemon(factory)

        list(daemon.run_job(_payload("/profiles/a")))
        list(daemon.run_job(_payload("/profiles/b")))

        assert len(factory.built) == 2
        assert _FakeEngine.seen_drivers[0] is not _FakeEngine.seen_drivers[1]


class TestFailureHandling:
    def test_a_failed_job_never_leaves_its_driver_warm(self):
        """A job that blew up mid-turn leaves the page in an unknown state; it
        must not be handed to the next patient's job."""
        factory = _RecordingFactory()
        daemon = _daemon(factory)
        _FakeEngine.raise_on_run = True

        with pytest.raises(RuntimeError):
            list(daemon.run_job(_payload()))

        assert factory.built[0].closed is True

        # The next job must start from a brand-new driver.
        _FakeEngine.raise_on_run = False
        list(daemon.run_job(_payload()))
        assert len(factory.built) == 2

    def test_close_shuts_down_every_warm_driver(self):
        factory = _RecordingFactory()
        daemon = _daemon(factory)
        list(daemon.run_job(_payload("/profiles/a")))
        list(daemon.run_job(_payload("/profiles/b")))

        daemon.close()

        assert all(d.closed for d in factory.built)

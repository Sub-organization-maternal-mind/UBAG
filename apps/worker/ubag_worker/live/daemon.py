"""Warm-browser worker daemon (Layer B of ``UBAG_WORKER_DAEMON``).

Today every job spawns a fresh worker process, which re-attaches over CDP and
opens a NEW page (``engine.py`` calls ``driver.open()`` per job). The browser and
the operator's logged-in profile already persist, so that per-job cost buys
nothing but latency.

This daemon keeps :class:`PageDriver` instances alive between jobs, keyed by
identity, and injects them into ``engine.iter_events(payload, driver=...)`` so
``owns_driver`` is False and the engine leaves the page open.

**One job in flight at a time, by construction.** Never drive two pages against
one provider account concurrently: sync-Playwright objects are thread-bound, and
concurrent turns on a shared profile risk CAPTCHA/lockout and interleaved output.

A driver is reused ONLY when :meth:`PageDriver.prepare_for_next_job` proves the
page carries no prior conversation turn. Any doubt, and any job that did not
finish cleanly, discards the driver and goes cold -- i.e. today's behaviour.
"""
from __future__ import annotations

from typing import Any, Callable, Dict, Iterator, Mapping, Optional, Tuple

from .engine import LiveSessionEngine, _normalize_payload
from .page_driver import PageDriver, create_default_driver
from .selectors import PROVIDER_SELECTORS

JsonObject = Dict[str, Any]
DriverKey = Tuple[str, str, str]


def _close_quietly(driver: Optional[PageDriver]) -> None:
    """Closing is best-effort: a driver we are discarding anyway must never
    surface its teardown error as the job's outcome."""
    if driver is None:
        return
    try:
        driver.close()
    except Exception:  # noqa: BLE001
        pass


def _target_from_payload(payload: object) -> str:
    # Shared tolerant target extraction (envelope module; never raises).
    from .envelope import _target_from_payload

    return _target_from_payload(payload)


def _driver_key(payload: Mapping[str, Any]) -> DriverKey:
    """Identity of a warm page: (tenant, provider, profile).

    The profile directory is the real session boundary -- two profiles are two
    logged-in identities -- so it is part of the key. A warm page is NEVER shared
    across keys.
    """
    target = _target_from_payload(payload)
    normalized = _normalize_payload(payload, target)
    return (normalized.tenant_id, target, normalized.user_data_dir)


class WarmWorkerDaemon:
    """Holds warm drivers across jobs and runs one job at a time."""

    def __init__(
        self,
        *,
        driver_factory: Callable[[Any], PageDriver] = create_default_driver,
        engine_factory: Callable[[Any], Any] = LiveSessionEngine,
        selectors_by_target: Mapping[str, Any] = PROVIDER_SELECTORS,
        orchestrator: Optional[Any] = None,
    ) -> None:
        self._driver_factory = driver_factory
        self._engine_factory = engine_factory
        self._selectors_by_target = selectors_by_target
        self._warm: Dict[DriverKey, PageDriver] = {}
        # Optional process-level orchestrator (Fleet + ChannelPool + AIMD).
        # None (the default, and what run_worker_daemon constructs unless
        # UBAG_ORCHESTRATOR_ENABLED is truthy) keeps behavior byte-identical.
        self._orchestrator = orchestrator

    def run_job(self, payload: Mapping[str, Any]) -> Iterator[JsonObject]:
        """Drive one job, yielding the engine's events verbatim.

        The event stream is passed through untouched: the daemon changes where
        the page comes from, never what is captured from it.
        """
        target = _target_from_payload(payload)
        if target not in self._selectors_by_target:
            raise ValueError("no live selector configuration for target %r" % target)
        selectors = self._selectors_by_target[target]
        key = _driver_key(payload)

        self._evict_other_keys(key)
        driver = self._checkout(key, selectors, payload)
        if self._orchestrator is None:
            engine = self._engine_factory(selectors)
        else:
            # Factories default to LiveSessionEngine(selectors) — a single
            # positional arg. Only inject the orchestrator when one is wired.
            engine = self._engine_factory(selectors, orchestrator=self._orchestrator)
        completed = False
        try:
            for event in engine.iter_events(payload, driver=driver):
                event_type = str(
                    event.get("type", event.get("event_type", ""))
                    if isinstance(event, Mapping)
                    else ""
                )
                if event_type in ("completed", "completed_with_warnings"):
                    completed = True
                yield event
        except BaseException:
            # Includes GeneratorExit (consumer abandoned the job) and timeouts.
            # The page may hold a half-rendered turn, so it must never be handed
            # to the next job -- which could be a different patient.
            self._warm.pop(key, None)
            _close_quietly(driver)
            raise

        if completed:
            self._warm[key] = driver
        else:
            # A blocked/manual-action result returns normally from the engine but
            # can leave menus, overlays, or a partial turn behind. Never reuse it.
            _close_quietly(driver)

    def _evict_other_keys(self, key: DriverKey) -> None:
        """Keep at most one Sync Playwright manager alive in this thread.

        Real PageDrivers each own a ``sync_playwright().start()`` manager.
        Playwright refuses to start a second Sync manager while the first
        manager's asyncio loop is active in the same daemon thread. Same-key
        jobs retain their warm page; changing tenant/provider/profile closes
        the old page before the new driver is constructed.
        """
        for other_key in list(self._warm):
            if other_key != key:
                _close_quietly(self._warm.pop(other_key))

    def _checkout(
        self, key: DriverKey, selectors: Any, payload: Mapping[str, Any]
    ) -> PageDriver:
        """Return a driver for this job: the warm one when the gate proves the
        page empty, otherwise a brand-new cold one."""
        warm = self._warm.pop(key, None)
        if warm is not None:
            if warm.prepare_for_next_job(selectors):
                try:
                    warm.clear_attachment_state()
                    return warm
                except Exception:  # noqa: BLE001
                    _close_quietly(warm)
            # Could not prove the page empty (prior turn still visible, dead
            # page, or a probe that threw). Discard it and pay for a cold tab --
            # slower, but it cannot return a prior patient's report.
            _close_quietly(warm)

        options = payload.get("options") if isinstance(payload, Mapping) else None
        return self._driver_factory(options)

    def close(self) -> None:
        """Release every warm driver (daemon shutdown)."""
        for driver in list(self._warm.values()):
            _close_quietly(driver)
        self._warm.clear()

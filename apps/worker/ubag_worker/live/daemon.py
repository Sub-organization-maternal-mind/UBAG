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

import os
import time
from typing import Any, Callable, Dict, Iterator, Mapping, Optional, Tuple

from .engine import LiveSessionEngine, _normalize_payload
from .identity_lock import IdentityBusy, IdentityLock, identity_lock_enabled, physical_session_key
from .page_driver import UNKNOWN, DriftDetectedError, PageDriver, create_default_driver
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


def _probe_min_interval_s() -> float:
    try:
        return max(0.0, float(os.environ.get("UBAG_WORKER_PROBE_MIN_INTERVAL_S", "30")))
    except ValueError:
        return 30.0


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
        chat_sink: Optional[Callable[..., Any]] = None,
    ) -> None:
        self._driver_factory = driver_factory
        self._engine_factory = engine_factory
        self._selectors_by_target = selectors_by_target
        self._warm: Dict[DriverKey, PageDriver] = {}
        # Optional process-level orchestrator (Fleet + ChannelPool + AIMD).
        # None (the default, and what run_worker_daemon constructs unless
        # UBAG_ORCHESTRATOR_ENABLED is truthy) keeps behavior byte-identical.
        self._orchestrator = orchestrator
        # Chat-ledger sink (UBAG_CHAT_LEDGER_ENABLED); None keeps behavior unchanged.
        self._chat_sink = chat_sink
        # Readiness-probe rate limit: key -> (monotonic time, reply). See probe_readiness.
        self._probe_cache: Dict[DriverKey, Tuple[float, JsonObject]] = {}
        self._clock: Callable[[], float] = time.monotonic

    def warm_key(self, payload: Mapping[str, Any]) -> str:
        """One-way hash of the physical session (protocol v2 JOB_END ``warm_key``)."""
        target = _target_from_payload(payload)
        return physical_session_key(
            endpoint=os.environ.get("UBAG_REMOTE_BROWSER_ENDPOINT", ""),
            user_data_dir=_normalize_payload(payload, target).user_data_dir,
            target=target,
        )

    def probe_readiness(self, payload: Mapping[str, Any]) -> JsonObject:
        """Read-only provider readiness (proto:2 ``probe`` carrying a payload).

        Reuses ``driver.open`` + ``driver.detect_login_state`` ONLY: it never
        types, fills, submits or starts a login (logins stay human-only), and it
        takes the physical-session identity lock without waiting, so a probe can
        neither overlap nor delay a job on the same identity (``busy`` instead).
        Repeat probes of one identity inside ``UBAG_WORKER_PROBE_MIN_INTERVAL_S``
        (default 30) are answered from the previous reply (``cached``) so a fleet
        manager cannot hammer the provider page. The sender MUST NOT probe while an
        operator login or viewer session is active; the daemon cannot see that.
        """
        target = _target_from_payload(payload)
        selectors = self._selectors_by_target.get(target)
        if selectors is None:
            raise ValueError("no live selector configuration for target %r" % target)
        normalized = _normalize_payload(payload, target)
        key = _driver_key(payload)
        reply: JsonObject = {
            "target": target,
            "selector_version": selectors.selector_version,
        }

        cached = self._probe_cache.get(key)
        interval = _probe_min_interval_s()
        if cached is not None and self._clock() - cached[0] < interval:
            return {**cached[1], "cached": True}

        lock = None
        if identity_lock_enabled():
            lock = IdentityLock(
                physical_session_key(
                    endpoint=os.environ.get("UBAG_REMOTE_BROWSER_ENDPOINT", ""),
                    user_data_dir=normalized.user_data_dir,
                    target=target,
                ),
                blocking=False,
            )
        try:
            if lock is not None:
                lock.__enter__()
        except IdentityBusy:
            # Not cached: busy says nothing about login state.
            return {**reply, "state": "busy"}
        try:
            reply["state"] = "idle"
            reply["login_state"] = self._detect_login_state(key, selectors, payload, normalized)
        finally:
            if lock is not None:
                lock.__exit__(None, None, None)
        self._probe_cache[key] = (self._clock(), reply)
        return dict(reply)

    def _detect_login_state(
        self, key: DriverKey, selectors: Any, payload: Mapping[str, Any], normalized: Any
    ) -> str:
        warm = self._warm.get(key)
        driver = warm
        try:
            if driver is None:
                self._evict_other_keys(key)  # one Sync Playwright manager per thread
                options = payload.get("options") if isinstance(payload, Mapping) else None
                driver = self._driver_factory(options)
                driver.open(
                    target_url=selectors.target_url,
                    user_data_dir=normalized.user_data_dir,
                    headless=normalized.headless,
                )
            return driver.detect_login_state(selectors)
        except DriftDetectedError:
            return UNKNOWN
        except Exception:  # noqa: BLE001 - a probe never fails the daemon or leaks details
            return UNKNOWN
        finally:
            if warm is None:
                _close_quietly(driver)  # a cold probe page is never kept or reused

    def run_job(self, payload: Mapping[str, Any]) -> Iterator[JsonObject]:
        """Drive one job; in slot mode, under the physical-session identity lock."""
        if not identity_lock_enabled():
            yield from self._run_job(payload)
            return
        try:
            target = _target_from_payload(payload)
            normalized = _normalize_payload(payload, target)
            key = physical_session_key(
                endpoint=os.environ.get("UBAG_REMOTE_BROWSER_ENDPOINT", ""),
                user_data_dir=normalized.user_data_dir,
                target=target,
            )
        except Exception:  # noqa: BLE001 - bad envelope: let the unlocked path report it
            yield from self._run_job(payload)
            return
        # One active operation per physical session across slot processes; the
        # kernel drops the flock if this process dies, so no stale locks.
        with IdentityLock(key):
            yield from self._run_job(payload)

    def _run_job(self, payload: Mapping[str, Any]) -> Iterator[JsonObject]:
        """Drive one job, yielding the engine's events verbatim.

        The event stream is passed through untouched: the daemon changes where
        the page comes from, never what is captured from it.
        """
        from ..voice.voice_job import UNSUPPORTED_RUNNER, is_voice_command, refuse_voice_job

        if is_voice_command(payload):
            # PlaywrightCdpClient starts its own sync_playwright, which cannot
            # share this daemon's thread with a warm driver's manager.
            yield from refuse_voice_job(
                payload,
                UNSUPPORTED_RUNNER,
                "voice jobs must run on the per-job runner, not the warm daemon",
            )
            return
        target = _target_from_payload(payload)
        if target not in self._selectors_by_target:
            raise ValueError("no live selector configuration for target %r" % target)
        selectors = self._selectors_by_target[target]
        key = _driver_key(payload)

        self._evict_other_keys(key)
        driver = self._checkout(key, selectors, payload)
        # Factories default to LiveSessionEngine(selectors) — a single
        # positional arg. Only inject the orchestrator/sink when wired.
        extra: Dict[str, Any] = {}
        if self._orchestrator is not None:
            extra["orchestrator"] = self._orchestrator
        if self._chat_sink is not None:
            extra["chat_sink"] = self._chat_sink
        engine = self._engine_factory(selectors, **extra)
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
            if self._resume_in_place(warm, selectors, payload) or warm.prepare_for_next_job(
                selectors
            ):
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

    @staticmethod
    def _resume_in_place(warm: PageDriver, selectors: Any, payload: Mapping[str, Any]) -> bool:
        """UBAG_WARM_RESUME_FASTPATH (default off): skip the new-chat gate only
        when this job resumes the very thread the warm page already shows.

        The warm key (tenant, provider, profile) already matched to get here; the
        driver must additionally prove the live page's URL equals the job's
        conversation thread_ref. Any mismatch or doubt returns False and the full
        ``prepare_for_next_job`` gate runs. ``clear_attachment_state`` still runs
        in the caller; the engine's ``resume_thread`` still re-confirms the turn.
        """
        if os.environ.get("UBAG_WARM_RESUME_FASTPATH", "").strip().lower() not in (
            "1", "true", "yes", "on",
        ):
            return False
        try:
            from .envelope import _conversation_binding

            key, thread_ref, _ = _conversation_binding(payload)
            return bool(
                key is not None and thread_ref and warm.can_resume_in_place(selectors, thread_ref)
            )
        except Exception:  # noqa: BLE001 - any doubt keeps the full gate
            return False

    def close(self) -> None:
        """Release every warm driver (daemon shutdown)."""
        for driver in list(self._warm.values()):
            _close_quietly(driver)
        self._warm.clear()

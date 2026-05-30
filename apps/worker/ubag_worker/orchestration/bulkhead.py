"""Failure isolation bulkheads + single-flight re-login — §12.10, §12.12.

The browser hierarchy is a set of nested bulkheads: the smaller the failure, the
smaller and faster the recovery. :func:`compute_recovery` is a pure function that,
given a crash at a level, returns the blast radius (affected tabs/contexts/
browsers) and an ordered recovery plan, including the job ids to requeue
idempotently.

:class:`SingleFlightRelogin` models §12.10: the first tab to observe a
logged-out context performs ONE re-login while siblings park, then resume — N
tabs never trigger N logins. No real login happens here; manual login stays
human-driven (§12.5).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from typing import Dict, Iterable, List, Optional

from .topology import BrowserInstance, ChannelTab, Fleet, ProviderContext


class CrashLevel(Enum):
    TAB = "tab"
    CONTEXT = "context"
    BROWSER = "browser"
    WORKER = "worker"


class RecoveryAction(Enum):
    REAP_TAB = "reap_tab"
    OPEN_REPLACEMENT_TAB = "open_replacement_tab"
    REQUEUE_JOB = "requeue_job"
    RECREATE_CONTEXT = "recreate_context"
    RESTORE_SNAPSHOT = "restore_snapshot"
    REWARM_MIN_TABS = "rewarm_min_tabs"
    RESPAWN_BROWSER = "respawn_browser"
    RECREATE_CONTEXTS = "recreate_contexts"
    OUTBOX_REQUEUE = "outbox_requeue"
    REASSIGN_WORKER = "reassign_worker"


@dataclass
class RecoveryPlan:
    level: CrashLevel
    affected_browser_ids: List[str] = field(default_factory=list)
    affected_context_ids: List[str] = field(default_factory=list)
    affected_tab_ids: List[str] = field(default_factory=list)
    requeue_job_ids: List[str] = field(default_factory=list)
    actions: List[RecoveryAction] = field(default_factory=list)


def _job_ids(tabs: Iterable[ChannelTab]) -> List[str]:
    return [tab.current_job_id for tab in tabs if tab.current_job_id is not None]


def compute_recovery(
    level: CrashLevel,
    *,
    fleet: Optional[Fleet] = None,
    tab: Optional[ChannelTab] = None,
    context: Optional[ProviderContext] = None,
    browser: Optional[BrowserInstance] = None,
    worker_id: Optional[str] = None,
    tabs: Optional[Iterable[ChannelTab]] = None,
    contexts: Optional[Iterable[ProviderContext]] = None,
) -> RecoveryPlan:
    """Compute the blast radius and recovery plan for a crash at ``level``.

    ``tabs`` / ``contexts`` let callers supply the live topology when a
    :class:`Fleet` is not the source of truth (e.g. tabs live in pools). When a
    ``fleet`` is given, contexts/tabs are derived from it where possible.
    """

    plan = RecoveryPlan(level=level)
    all_tabs = list(tabs) if tabs is not None else []
    all_contexts = list(contexts) if contexts is not None else []

    if level is CrashLevel.TAB:
        if tab is None:
            raise ValueError("tab crash requires the crashed tab")
        plan.affected_tab_ids = [tab.tab_id]
        plan.requeue_job_ids = _job_ids([tab])
        plan.actions = [
            RecoveryAction.REAP_TAB,
            RecoveryAction.REQUEUE_JOB,
            RecoveryAction.OPEN_REPLACEMENT_TAB,
        ]
        return plan

    if level is CrashLevel.CONTEXT:
        if context is None:
            raise ValueError("context crash requires the crashed context")
        impacted = [t for t in all_tabs if t.context_id == context.context_id]
        plan.affected_context_ids = [context.context_id]
        plan.affected_tab_ids = [t.tab_id for t in impacted]
        plan.requeue_job_ids = _job_ids(impacted)
        plan.actions = [
            RecoveryAction.RECREATE_CONTEXT,
            RecoveryAction.RESTORE_SNAPSHOT,
            RecoveryAction.REWARM_MIN_TABS,
            RecoveryAction.REQUEUE_JOB,
        ]
        return plan

    if level is CrashLevel.BROWSER:
        if browser is None:
            raise ValueError("browser crash requires the crashed browser")
        ctx_on_browser = [
            c for c in all_contexts if c.instance_id == browser.instance_id
        ]
        ctx_ids = {c.context_id for c in ctx_on_browser}
        impacted = [t for t in all_tabs if t.context_id in ctx_ids]
        plan.affected_browser_ids = [browser.instance_id]
        plan.affected_context_ids = [c.context_id for c in ctx_on_browser]
        plan.affected_tab_ids = [t.tab_id for t in impacted]
        plan.requeue_job_ids = _job_ids(impacted)
        plan.actions = [
            RecoveryAction.RESPAWN_BROWSER,
            RecoveryAction.RECREATE_CONTEXTS,
            RecoveryAction.RESTORE_SNAPSHOT,
            RecoveryAction.REQUEUE_JOB,
        ]
        return plan

    if level is CrashLevel.WORKER:
        if worker_id is None:
            raise ValueError("worker crash requires the worker_id")
        browsers = (
            [b for b in fleet.browsers if b.worker_id == worker_id]
            if fleet is not None
            else []
        )
        b_ids = {b.instance_id for b in browsers}
        ctx_on_worker = [c for c in all_contexts if c.instance_id in b_ids]
        ctx_ids = {c.context_id for c in ctx_on_worker}
        impacted = [t for t in all_tabs if t.context_id in ctx_ids]
        plan.affected_browser_ids = [b.instance_id for b in browsers]
        plan.affected_context_ids = [c.context_id for c in ctx_on_worker]
        plan.affected_tab_ids = [t.tab_id for t in impacted]
        plan.requeue_job_ids = _job_ids(impacted)
        plan.actions = [
            RecoveryAction.OUTBOX_REQUEUE,
            RecoveryAction.REASSIGN_WORKER,
        ]
        return plan

    raise ValueError("unknown crash level: %r" % (level,))


@dataclass
class ReloginTicket:
    """Result of requesting a re-login on a context's auth mutex."""

    tab_id: str
    is_leader: bool


class SingleFlightRelogin:
    """Per-context ``auth_mutex`` ensuring exactly one re-login (§12.10).

    The first tab to observe ``logged_out`` becomes the leader; siblings park.
    When the leader completes, parked siblings are returned so they can resume.
    """

    def __init__(self) -> None:
        self._in_progress = False
        self._owner: Optional[str] = None
        self._parked: List[str] = []
        self.login_count = 0

    @property
    def in_progress(self) -> bool:
        return self._in_progress

    @property
    def parked(self) -> List[str]:
        return list(self._parked)

    def request(self, tab_id: str) -> ReloginTicket:
        if not self._in_progress:
            self._in_progress = True
            self._owner = tab_id
            return ReloginTicket(tab_id=tab_id, is_leader=True)
        if tab_id != self._owner and tab_id not in self._parked:
            self._parked.append(tab_id)
        return ReloginTicket(tab_id=tab_id, is_leader=False)

    def complete(self, tab_id: str) -> List[str]:
        """Leader finishes the single re-login; returns parked tabs to resume."""

        if tab_id != self._owner:
            raise RuntimeError("only the re-login leader may complete the mutex")
        self.login_count += 1
        resumed = list(self._parked)
        self._parked.clear()
        self._in_progress = False
        self._owner = None
        return resumed


__all__ = [
    "CrashLevel",
    "RecoveryAction",
    "RecoveryPlan",
    "ReloginTicket",
    "SingleFlightRelogin",
    "compute_recovery",
]

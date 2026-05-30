"""Parallel-channel tab pool per (tenant, provider, identity) — §12.8.

A :class:`ChannelPool` turns "more than one request for the same provider" into
"open another tab and run it in parallel, then return the result", governed by
the AIMD ceiling (§12.9), the submit pacer (§12.9), a per-tab memory budget
(§12.11), and the assignment algorithm of §12.8.

Honors INV-1 (sticky one-conversation-per-tab), INV-2 (reuse the authenticated
context for new tabs), and the §12.9 correctness gate: ``spa-singleton`` targets
disable shared-context multi-tab and fan out into one context per channel.

No real browser is driven here; tabs are state-machine records (warm → ready →
busy → ready/draining → closed) and the clock is injectable.
"""

from __future__ import annotations

import heapq
import itertools
import time
from dataclasses import dataclass, field
from enum import Enum
from typing import Callable, Dict, List, Optional

from .aimd import AIMDController
from .pacer import SubmitPacer
from .topology import (
    ChannelTab,
    ConversationModel,
    Lane,
    ProviderContext,
    TabState,
)


class AssignOutcome(Enum):
    ASSIGNED_STICKY = "assigned_sticky"
    ASSIGNED_REUSE = "assigned_reuse"
    ASSIGNED_NEW_TAB = "assigned_new_tab"
    ENQUEUED = "enqueued"
    ENQUEUED_STICKY = "enqueued_sticky"
    QUEUE_FULL = "queue_full"


@dataclass
class Job:
    """A unit of work routed through a pool."""

    job_id: str
    conversation_id: Optional[str] = None
    lane: Lane = Lane.NORMAL
    enqueued_at: float = 0.0


@dataclass
class AssignResult:
    outcome: AssignOutcome
    job_id: str
    tab: Optional[ChannelTab] = None
    conversation_id: Optional[str] = None

    @property
    def assigned(self) -> bool:
        return self.outcome in (
            AssignOutcome.ASSIGNED_STICKY,
            AssignOutcome.ASSIGNED_REUSE,
            AssignOutcome.ASSIGNED_NEW_TAB,
        )


@dataclass
class PoolConfig:
    min_tabs: int = 1
    max_tabs: int = 6
    idle_ttl: float = 60.0
    queue_maxsize: int = 64
    memory_budget_tabs: int = 12
    recycle_after_jobs: int = 50
    recycle_after_seconds: float = 600.0
    rss_budget: int = 250 * 1024 * 1024


class ChannelPool:
    """Auto-scaling tab pool for a single (tenant, provider, identity)."""

    def __init__(
        self,
        *,
        tenant_id: str,
        provider_id: str,
        identity_ref: str,
        context: ProviderContext,
        config: Optional[PoolConfig] = None,
        aimd: Optional[AIMDController] = None,
        pacer: Optional[SubmitPacer] = None,
        clock: Callable[[], float] = time.monotonic,
        conversation_model: Optional[ConversationModel] = None,
        context_factory: Optional[Callable[[], ProviderContext]] = None,
        id_prefix: str = "tab",
    ) -> None:
        self.tenant_id = tenant_id
        self.provider_id = provider_id
        self.identity_ref = identity_ref
        self._context = context
        self.config = config if config is not None else PoolConfig()
        self._aimd = aimd if aimd is not None else AIMDController(clock=clock)
        self._pacer = pacer if pacer is not None else SubmitPacer(clock=clock)
        self._clock = clock
        self._conversation_model = (
            conversation_model
            if conversation_model is not None
            else context.conversation_model
        )
        self._context_factory = context_factory
        self._id_prefix = id_prefix

        self._tabs: Dict[str, ChannelTab] = {}
        self._contexts: List[ProviderContext] = [context]
        self._conversation_tab: Dict[str, str] = {}
        self._sticky_queue: Dict[str, List[Job]] = {}
        self._created_at: Dict[str, float] = {}
        self._last_used: Dict[str, float] = {}
        self._counter = itertools.count(1)
        # Bounded priority queue entries: (lane, enqueued_at, seq, job).
        self._queue: List = []
        self._seq = itertools.count(1)

    # -- introspection -----------------------------------------------------
    @property
    def aimd(self) -> AIMDController:
        return self._aimd

    @property
    def pacer(self) -> SubmitPacer:
        return self._pacer

    @property
    def tabs(self) -> List[ChannelTab]:
        return list(self._tabs.values())

    @property
    def contexts(self) -> List[ProviderContext]:
        return list(self._contexts)

    @property
    def queue_depth(self) -> int:
        return len(self._queue) + sum(len(v) for v in self._sticky_queue.values())

    def effective_max(self) -> int:
        return min(self.config.max_tabs, self._aimd.cap, self.config.memory_budget_tabs)

    def _now(self, now: Optional[float]) -> float:
        return self._clock() if now is None else now

    # -- assignment (§12.8) -----------------------------------------------
    def submit(self, job: Job, now: Optional[float] = None) -> AssignResult:
        now = self._now(now)
        if not job.enqueued_at:
            job.enqueued_at = now

        # INV-1 sticky routing for an already-pinned conversation.
        if job.conversation_id and job.conversation_id in self._conversation_tab:
            tab = self._tabs.get(self._conversation_tab[job.conversation_id])
            if tab is not None and tab.state == TabState.READY:
                self._assign(job, tab, now)
                return AssignResult(
                    AssignOutcome.ASSIGNED_STICKY, job.job_id, tab, job.conversation_id
                )
            # Same conversation cannot parallelize: queue behind its owner tab.
            self._sticky_queue.setdefault(job.conversation_id, []).append(job)
            return AssignResult(
                AssignOutcome.ENQUEUED_STICKY, job.job_id, None, job.conversation_id
            )

        # Reuse a ready idle tab — fastest path (INV-2 already paid the login).
        reuse = self._find_ready_tab()
        if reuse is not None:
            self._assign(job, reuse, now)
            return AssignResult(
                AssignOutcome.ASSIGNED_REUSE, job.job_id, reuse, job.conversation_id
            )

        # Auto-open a new parallel tab if under the effective ceiling and the
        # pacer allows another submission right now.
        if len(self._tabs) < self.effective_max() and self._pacer.allow(now):
            tab = self._open_tab(now)
            self._pacer.acquire(now)
            self._assign(job, tab, now)
            return AssignResult(
                AssignOutcome.ASSIGNED_NEW_TAB, job.job_id, tab, job.conversation_id
            )

        # Backpressure — bounded queue, never unbounded.
        return self._enqueue(job)

    def _enqueue(self, job: Job) -> AssignResult:
        if len(self._queue) >= self.config.queue_maxsize:
            return AssignResult(
                AssignOutcome.QUEUE_FULL, job.job_id, None, job.conversation_id
            )
        heapq.heappush(
            self._queue, (int(job.lane), job.enqueued_at, next(self._seq), job)
        )
        return AssignResult(
            AssignOutcome.ENQUEUED, job.job_id, None, job.conversation_id
        )

    def _find_ready_tab(self) -> Optional[ChannelTab]:
        for tab in self._tabs.values():
            if tab.state == TabState.READY and tab.current_job_id is None:
                return tab
        return None

    def _open_tab(self, now: float) -> ChannelTab:
        if self._conversation_model == ConversationModel.SPA_SINGLETON:
            if self._context_factory is None:
                raise RuntimeError(
                    "spa-singleton pool requires a context_factory to fan out"
                )
            context = self._context_factory()
            self._contexts.append(context)
            context_id = context.context_id
        else:
            context_id = self._context.context_id
        tab_id = "%s_%04d" % (self._id_prefix, next(self._counter))
        tab = ChannelTab(tab_id=tab_id, context_id=context_id, state=TabState.WARMING)
        # warm → ready (no real navigation; modeled as immediate readiness).
        tab.state = TabState.READY
        self._tabs[tab_id] = tab
        self._created_at[tab_id] = now
        self._last_used[tab_id] = now
        return tab

    def _assign(self, job: Job, tab: ChannelTab, now: float) -> None:
        tab.state = TabState.BUSY
        tab.current_job_id = job.job_id
        if job.conversation_id:
            tab.conversation_id = job.conversation_id
            self._conversation_tab[job.conversation_id] = tab.tab_id
        self._last_used[tab.tab_id] = now

    # -- prewarm / lifecycle ----------------------------------------------
    def prewarm(self, now: Optional[float] = None) -> List[ChannelTab]:
        """Open tabs up to ``min_tabs`` to keep p95 low for bursty providers."""

        now = self._now(now)
        opened: List[ChannelTab] = []
        while len(self._tabs) < self.config.min_tabs and len(
            self._tabs
        ) < self.effective_max():
            opened.append(self._open_tab(now))
        return opened

    def complete(self, tab: ChannelTab, now: Optional[float] = None) -> Optional[AssignResult]:
        """Mark the tab's job done, then recycle and/or dispatch queued work."""

        now = self._now(now)
        tab.jobs_completed += 1
        tab.current_job_id = None
        self._last_used[tab.tab_id] = now

        recycled = False
        if self._should_recycle(tab, now):
            self._recycle(tab, now)
            recycled = True
        else:
            tab.state = TabState.READY

        # Sticky follow-ups resume on their owning tab first (INV-1).
        conv = tab.conversation_id
        if not recycled and conv and self._sticky_queue.get(conv):
            job = self._sticky_queue[conv].pop(0)
            if not self._sticky_queue[conv]:
                del self._sticky_queue[conv]
            self._assign(job, tab, now)
            return AssignResult(AssignOutcome.ASSIGNED_STICKY, job.job_id, tab, conv)

        # Otherwise work-steal the head of the pool queue (§12.13).
        return self._dispatch_one(now)

    def on_tab_free(self, now: Optional[float] = None) -> Optional[AssignResult]:
        """Work-stealing hook: a ready tab pulls the head of the pool queue."""

        return self._dispatch_one(self._now(now))

    def _dispatch_one(self, now: float) -> Optional[AssignResult]:
        if not self._queue:
            return None
        tab = self._find_ready_tab()
        if tab is None:
            return None
        _, _, _, job = heapq.heappop(self._queue)
        self._assign(job, tab, now)
        return AssignResult(AssignOutcome.ASSIGNED_REUSE, job.job_id, tab, job.conversation_id)

    # -- scale-down / recycle (§12.8, §12.11) -----------------------------
    def reap_idle(self, now: Optional[float] = None) -> List[str]:
        """Close idle tabs past ``idle_ttl`` down to ``min_tabs``."""

        now = self._now(now)
        idle = [
            tab
            for tab in self._tabs.values()
            if tab.state == TabState.READY and tab.current_job_id is None
        ]
        idle.sort(key=lambda t: self._last_used.get(t.tab_id, 0.0))
        excess = len(self._tabs) - self.config.min_tabs
        closed: List[str] = []
        for tab in idle:
            if excess <= 0:
                break
            if now - self._last_used.get(tab.tab_id, now) >= self.config.idle_ttl:
                self._close(tab)
                closed.append(tab.tab_id)
                excess -= 1
        return closed

    def _should_recycle(self, tab: ChannelTab, now: float) -> bool:
        if tab.jobs_completed >= self.config.recycle_after_jobs:
            return True
        age = now - self._created_at.get(tab.tab_id, now)
        if age >= self.config.recycle_after_seconds:
            return True
        if tab.rss_bytes > self.config.rss_budget:
            return True
        return False

    def _recycle(self, tab: ChannelTab, now: float) -> ChannelTab:
        tab.state = TabState.DRAINING
        self._close(tab)
        return self._open_tab(now)

    def _close(self, tab: ChannelTab) -> None:
        tab.state = TabState.CLOSED
        if tab.conversation_id and self._conversation_tab.get(tab.conversation_id) == tab.tab_id:
            del self._conversation_tab[tab.conversation_id]
        self._tabs.pop(tab.tab_id, None)
        self._created_at.pop(tab.tab_id, None)
        self._last_used.pop(tab.tab_id, None)

    def quarantine(self, tab: ChannelTab) -> None:
        """Health-probe failure: pull the tab out of rotation (§12.11)."""

        tab.state = TabState.QUARANTINED


__all__ = [
    "AssignOutcome",
    "AssignResult",
    "ChannelPool",
    "Job",
    "PoolConfig",
]

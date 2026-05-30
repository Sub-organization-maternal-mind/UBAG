"""Process-level orchestration coordinator for the live worker runtime.

This module is the *runtime glue* that connects the live manual-session engine
(:mod:`ubag_worker.live.engine`) to the pure orchestration algorithms in
:mod:`ubag_worker.orchestration` (Fleet, ChannelPool, AIMD, pacer). It owns a
single worker-scoped :class:`~ubag_worker.orchestration.topology.Fleet` and one
:class:`~ubag_worker.orchestration.channel_pool.ChannelPool` per
``(tenant, provider, identity)`` with a persistent
:class:`~ubag_worker.orchestration.aimd.AIMDController`, so adaptive concurrency
and the browser → context → tab topology are modeled for every live job.

POLICY (unchanged): orchestration only. There is **no** scraping, **no** CAPTCHA
solving, and **no** credential/cookie/storage-state ingestion here. The
coordinator never touches a real browser or the network; the clock is injectable
for deterministic tests. It only decides *which tab a job runs on* and records
the AIMD/topology state that the worker reports to the gateway as telemetry.

Invariants honored (blueprint §12.6): INV-1 (one conversation per tab via the
pool's sticky routing), INV-2 (reuse the authenticated context for new tabs),
INV-3 (provider+identity ⇒ isolated context), INV-5 (tenant isolation — pools
and contexts are keyed by tenant and never shared across tenants).
"""

from __future__ import annotations

import threading
import time
from dataclasses import dataclass
from typing import Callable, Dict, List, Optional, Tuple

from ..orchestration.aimd import AIMDController, CapChange, NegativeSignal
from ..orchestration.channel_pool import (
    AssignResult,
    ChannelPool,
    Job,
    PoolConfig,
)
from ..orchestration.topology import (
    CeilingExceededError,
    ChannelTab,
    ConversationModel,
    Fleet,
    Lane,
    ProviderContext,
    TabState,
)

_Key = Tuple[str, str, str]


@dataclass
class LiveLease:
    """The orchestration outcome for a single live job.

    Carries the assigned :class:`ChannelTab` (``None`` when the job was queued
    under backpressure), its owning :class:`ProviderContext`, and the raw
    :class:`AssignResult` so callers can branch on the assignment outcome.
    """

    key: _Key
    tenant_id: str
    provider_id: str
    identity_ref: str
    pool: ChannelPool
    context: ProviderContext
    result: AssignResult

    @property
    def tab(self) -> Optional[ChannelTab]:
        return self.result.tab

    @property
    def assigned(self) -> bool:
        return self.result.assigned


@dataclass(frozen=True)
class ConcurrencyState:
    """A point-in-time projection of a pool's AIMD ceiling for telemetry."""

    current_cap: int
    minimum: int
    maximum: Optional[int]
    in_flight: int


class LiveOrchestrator:
    """Worker-scoped Fleet + per-(tenant, provider, identity) channel pools.

    Thread-safe: a single re-entrant lock guards the fleet and pool registry so
    concurrent live jobs on the same worker process share adaptive-concurrency
    state safely. A persistent :class:`AIMDController` per key means a negative
    signal (e.g. an unexpected logout) lowers the ceiling for subsequent jobs of
    the same provider+identity, and sustained successes raise it back.
    """

    def __init__(
        self,
        *,
        clock: Callable[[], float] = time.monotonic,
        fleet: Optional[Fleet] = None,
        pool_config: Optional[PoolConfig] = None,
        worker_id: str = "worker-1",
    ) -> None:
        self._clock = clock
        self._fleet = fleet if fleet is not None else Fleet()
        self._pool_config = pool_config
        self._worker_id = worker_id
        self._pools: Dict[_Key, ChannelPool] = {}
        self._aimd: Dict[_Key, AIMDController] = {}
        self._lock = threading.RLock()

    @property
    def fleet(self) -> Fleet:
        return self._fleet

    def lease(
        self,
        *,
        tenant_id: str,
        provider_id: str,
        identity_ref: str,
        job_id: str,
        conversation_id: Optional[str] = None,
        lane: Lane = Lane.NORMAL,
        conversation_model: ConversationModel = ConversationModel.URL,
    ) -> LiveLease:
        """Acquire an orchestration lease (context + tab) for one live job."""

        with self._lock:
            key = (tenant_id, provider_id, identity_ref)
            context = self._fleet.get_or_create_context(
                tenant_id=tenant_id,
                target_id=provider_id,
                identity_ref=identity_ref,
                worker_id=self._worker_id,
                conversation_model=conversation_model,
            )
            pool = self._pools.get(key)
            if pool is None:
                aimd = self._aimd.get(key)
                if aimd is None:
                    aimd = AIMDController(clock=self._clock)
                    self._aimd[key] = aimd
                pool = ChannelPool(
                    tenant_id=tenant_id,
                    provider_id=provider_id,
                    identity_ref=identity_ref,
                    context=context,
                    config=self._pool_config,
                    aimd=aimd,
                    clock=self._clock,
                    conversation_model=context.conversation_model,
                )
                self._pools[key] = pool

            job = Job(job_id=job_id, conversation_id=conversation_id, lane=lane)
            result = pool.submit(job, now=self._clock())
            if result.tab is not None:
                # Tab accounting for the topology snapshot; ceiling overflow is a
                # soft signal here (the pool already enforces its own ceilings).
                try:
                    self._fleet.register_tab(result.tab)
                except CeilingExceededError:
                    pass
            return LiveLease(
                key=key,
                tenant_id=tenant_id,
                provider_id=provider_id,
                identity_ref=identity_ref,
                pool=pool,
                context=context,
                result=result,
            )

    def record_outcome(
        self,
        lease: LiveLease,
        *,
        success: bool,
        signal: Optional[NegativeSignal] = None,
    ) -> Optional[CapChange]:
        """Drive AIMD from the job outcome and release the leased tab.

        A negative ``signal`` multiplicatively cuts the ceiling (always emitting a
        :class:`CapChange`); otherwise a success additively increases it once the
        success window is met. Returns the emitted :class:`CapChange`, if any, so
        the caller can report it to the gateway as ``concurrency.cap_changed``.
        """

        with self._lock:
            change: Optional[CapChange] = None
            if signal is not None:
                change = lease.pool.aimd.record_signal(signal, now=self._clock())
            elif success:
                change = lease.pool.aimd.record_success(now=self._clock())

            if lease.result.tab is not None:
                lease.pool.complete(lease.result.tab, now=self._clock())
                self._fleet.unregister_tab(lease.result.tab)
            return change

    def concurrency_state(self, lease: LiveLease) -> ConcurrencyState:
        """Project the lease's pool ceiling for a ``concurrency.cap_changed`` event."""

        with self._lock:
            pool = lease.pool
            in_flight = sum(1 for tab in pool.tabs if tab.state == TabState.BUSY)
            return ConcurrencyState(
                current_cap=pool.aimd.cap,
                minimum=pool.aimd.floor,
                maximum=pool.config.max_tabs,
                in_flight=in_flight,
            )

    def topology_snapshot(self, tenant_id: Optional[str] = None) -> Dict[str, List[dict]]:
        """Build a tenant-scoped browser→context→tab snapshot for telemetry.

        The returned dict mirrors the gateway's ``gateway_browser_*`` JSON shape
        (``instances`` / ``contexts`` / ``tabs``). Storage-state material is never
        included — only a ``has_storage_state`` boolean, which is always ``False``
        here because the coordinator never reads any credential/cookie blob.
        """

        with self._lock:
            instances: List[dict] = []
            contexts: List[dict] = []
            tabs: List[dict] = []

            for browser in self._fleet.browsers:
                if tenant_id is not None and browser.tenant_id != tenant_id:
                    continue
                instances.append(
                    {
                        "instance_id": browser.instance_id,
                        "worker_id": browser.worker_id,
                        "engine": browser.engine,
                        "remote_endpoint": browser.remote_endpoint or "",
                        "state": browser.state.value,
                        "context_count": browser.context_count,
                        "tab_count": browser.tab_count,
                    }
                )

            for context in self._fleet.contexts:
                if tenant_id is not None and context.tenant_id != tenant_id:
                    continue
                contexts.append(
                    {
                        "context_id": context.context_id,
                        "instance_id": context.instance_id,
                        "target_id": context.target_id,
                        "identity_ref": context.identity_ref,
                        "login_state": context.login_state,
                        "conversation_model": context.conversation_model.value,
                        "max_tabs": context.max_tabs,
                        "has_storage_state": False,
                    }
                )

            context_tenant = {c["context_id"]: c for c in contexts}
            for key, pool in self._pools.items():
                if tenant_id is not None and key[0] != tenant_id:
                    continue
                for tab in pool.tabs:
                    if tab.context_id not in context_tenant:
                        continue
                    tabs.append(
                        {
                            "tab_id": tab.tab_id,
                            "context_id": tab.context_id,
                            "state": tab.state.value,
                            "conversation_id": tab.conversation_id or "",
                            "current_job_id": tab.current_job_id or "",
                            "jobs_completed": tab.jobs_completed,
                        }
                    )

            return {"instances": instances, "contexts": contexts, "tabs": tabs}


__all__ = [
    "ConcurrencyState",
    "LiveLease",
    "LiveOrchestrator",
]

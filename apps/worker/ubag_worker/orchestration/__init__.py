"""ToS-safe multi-tab browser orchestration engine (UBAG v2.1, §12.6–§12.13).

This subpackage is a pure-Python, in-memory **orchestration / scheduling** layer.
It models the browser → provider-context → channel-tab hierarchy, adaptive
concurrency, submit pacing, failure isolation, and fair scheduling so the worker
can decide *which tab runs which job, when, and how many tabs to open*.

POLICY (do not change): this is orchestration ONLY. There is NO scraping, NO
CAPTCHA auto-solving, and NO credential/cookie/storage-state ingestion here.
Manual login stays human-driven (see blueprint §12.5). These classes never touch
a real browser or the network; clocks and RNGs are injectable for determinism.

The model is governed by five invariants (blueprint §12.6) honored throughout:

    INV-1  One conversation per tab. A tab runs exactly one conversation at a
           time; a follow-up carrying a ``conversation_id`` is pinned to its
           owning tab and queues *behind* it (never parallelizes a thread).
    INV-2  Same provider + identity ⇒ shared login, new tab. Extra requests for
           an already-authenticated provider open a new tab in the existing
           context (no re-login, warm start).
    INV-3  Different provider (or identity) ⇒ new context, same browser. Each
           provider+identity co-resides as an isolated context (cookies,
           storage, fingerprint, proxy) until the per-browser context ceiling
           spills it onto another browser.
    INV-4  Provider patience is a managed resource. Per (provider, identity)
           there is an adaptive ceiling on concurrent tabs (AIMD) plus a global
           submit pacer.
    INV-5  Tenant isolation is absolute. Contexts and tabs are NEVER shared
           across tenants; a multi-provider browser belongs to exactly one
           tenant.
"""

from __future__ import annotations

from .aimd import AIMDController, CapChange, NegativeSignal
from .bulkhead import (
    BulkheadConfig,
    BulkheadRegistry,
    CrashLevel,
    RecoveryAction,
    RecoveryPlan,
    ReloginTicket,
    SingleFlightRelogin,
    compute_recovery,
)
from .channel_pool import (
    AssignOutcome,
    AssignResult,
    ChannelPool,
    Job,
    PoolConfig,
)
from .pacer import SubmitPacer, SubmitPacerRegistry
from .scheduler import Lane, ScheduledJob, WeightedScheduler
from .telemetry import CONCURRENCY_CHANGE_EVENT_TYPE, concurrency_change_data
from .topology import (
    BrowserInstance,
    ChannelTab,
    ConversationModel,
    Fleet,
    InstanceState,
    ProviderContext,
    TabState,
    TenantIsolationError,
)

__all__ = [
    # topology
    "BrowserInstance",
    "ChannelTab",
    "ConversationModel",
    "Fleet",
    "InstanceState",
    "ProviderContext",
    "TabState",
    "TenantIsolationError",
    "Lane",
    # aimd
    "AIMDController",
    "CapChange",
    "NegativeSignal",
    # pacer
    "SubmitPacer",
    "SubmitPacerRegistry",
    # telemetry
    "CONCURRENCY_CHANGE_EVENT_TYPE",
    "concurrency_change_data",
    # channel pool
    "AssignOutcome",
    "AssignResult",
    "ChannelPool",
    "Job",
    "PoolConfig",
    # bulkhead
    "BulkheadConfig",
    "BulkheadRegistry",
    "CrashLevel",
    "RecoveryAction",
    "RecoveryPlan",
    "ReloginTicket",
    "SingleFlightRelogin",
    "compute_recovery",
    # scheduler
    "ScheduledJob",
    "WeightedScheduler",
]

"""Three-level browser topology: Browser → ProviderContext → ChannelTab.

This module is the in-memory registry for the orchestration hierarchy. It
enforces tenant isolation (INV-5), lazy context creation with shared-login reuse
(INV-2), per-provider/identity context separation (INV-3), and per-browser
context/tab ceilings with spillover onto sibling browser instances.

No real browser is launched here; these are plain dataclasses describing fleet
state that the scheduler and channel pools reason about.
"""

from __future__ import annotations

import itertools
from dataclasses import dataclass, field
from enum import Enum, IntEnum
from typing import Callable, Dict, Optional, Tuple


class TenantIsolationError(RuntimeError):
    """Raised when an operation would share a context/tab across tenants (INV-5)."""


class CeilingExceededError(RuntimeError):
    """Raised when a per-browser context/tab ceiling would be violated."""


class InstanceState(Enum):
    SPAWNING = "spawning"
    READY = "ready"
    DRAINING = "draining"
    CLOSED = "closed"


class TabState(Enum):
    WARMING = "warming"
    READY = "ready"
    BUSY = "busy"
    DRAINING = "draining"
    QUARANTINED = "quarantined"
    CLOSED = "closed"


class ConversationModel(Enum):
    """How a target handles parallel conversations (correctness gate, §12.9)."""

    URL = "url"  # new conversation == new route; safe for shared-context multi-tab
    TABBED = "tabbed"  # same as URL for orchestration purposes
    SPA_SINGLETON = "spa-singleton"  # one active conversation per login; fan out to contexts


class Lane(IntEnum):
    """Priority lanes (§14.4 / §12.13). Lower value == higher priority."""

    CRITICAL = 0
    HIGH = 1
    NORMAL = 2
    LOW = 3
    BULK = 4


@dataclass
class BrowserInstance:
    """A single browser process pinned to exactly one tenant (INV-5)."""

    instance_id: str
    worker_id: str
    tenant_id: str
    engine: str = "chromium"
    remote_endpoint: Optional[str] = None
    state: InstanceState = InstanceState.SPAWNING
    context_count: int = 0
    tab_count: int = 0
    rss_bytes: int = 0
    recycle_at: Optional[float] = None


@dataclass
class ProviderContext:
    """An isolated provider+identity context (cookies/storage/fingerprint/proxy).

    Unique per (tenant_id, target_id, identity_ref). ``storage_state_uri`` is an
    opaque reference only — no credential or cookie material is ever read here.
    """

    context_id: str
    instance_id: str
    tenant_id: str
    target_id: str
    identity_ref: str
    login_state: str = "unknown"
    conversation_model: ConversationModel = ConversationModel.URL
    fingerprint_id: Optional[str] = None
    proxy_id: Optional[str] = None
    storage_state_uri: Optional[str] = None
    max_tabs: int = 2


@dataclass
class ChannelTab:
    """A single tab running at most one conversation at a time (INV-1)."""

    tab_id: str
    context_id: str
    state: TabState = TabState.WARMING
    conversation_id: Optional[str] = None
    current_job_id: Optional[str] = None
    jobs_completed: int = 0
    rss_bytes: int = 0


@dataclass
class Fleet:
    """Worker-scoped registry of browsers, contexts, and tabs.

    Contexts are created lazily on first use and reused for the same
    (tenant, target, identity) tuple (INV-2). A different provider or identity
    yields a new context on a tenant-matched browser (INV-3); when a browser's
    ``context_ceiling`` is reached, new contexts spill onto another browser.
    """

    context_ceiling: int = 6
    tab_ceiling: int = 12
    id_factory: Optional[Callable[[str], str]] = None
    _browsers: Dict[str, BrowserInstance] = field(default_factory=dict)
    _contexts: Dict[str, ProviderContext] = field(default_factory=dict)
    _context_index: Dict[Tuple[str, str, str], str] = field(default_factory=dict)
    _counter: "itertools.count[int]" = field(default_factory=lambda: itertools.count(1))

    # -- id helpers --------------------------------------------------------
    def _new_id(self, prefix: str) -> str:
        if self.id_factory is not None:
            return self.id_factory(prefix)
        return "%s_%04d" % (prefix, next(self._counter))

    # -- introspection -----------------------------------------------------
    @property
    def browsers(self) -> Tuple[BrowserInstance, ...]:
        return tuple(self._browsers.values())

    @property
    def contexts(self) -> Tuple[ProviderContext, ...]:
        return tuple(self._contexts.values())

    def get_browser(self, instance_id: str) -> BrowserInstance:
        return self._browsers[instance_id]

    def get_context(self, context_id: str) -> ProviderContext:
        return self._contexts[context_id]

    # -- browser/context creation -----------------------------------------
    def create_browser(
        self,
        *,
        worker_id: str,
        tenant_id: str,
        engine: str = "chromium",
        remote_endpoint: Optional[str] = None,
    ) -> BrowserInstance:
        browser = BrowserInstance(
            instance_id=self._new_id("br"),
            worker_id=worker_id,
            tenant_id=tenant_id,
            engine=engine,
            remote_endpoint=remote_endpoint,
            state=InstanceState.READY,
        )
        self._browsers[browser.instance_id] = browser
        return browser

    def _select_browser_for(self, tenant_id: str, worker_id: str) -> BrowserInstance:
        """Pick a tenant-matched browser with context headroom, else spawn one.

        Enforces INV-5: a browser for a different tenant is never selected, even
        if it has spare capacity.
        """

        for browser in self._browsers.values():
            if (
                browser.tenant_id == tenant_id
                and browser.state == InstanceState.READY
                and browser.context_count < self.context_ceiling
            ):
                return browser
        return self.create_browser(worker_id=worker_id, tenant_id=tenant_id)

    def get_or_create_context(
        self,
        *,
        tenant_id: str,
        target_id: str,
        identity_ref: str,
        worker_id: str = "worker-1",
        conversation_model: ConversationModel = ConversationModel.URL,
        fingerprint_id: Optional[str] = None,
        proxy_id: Optional[str] = None,
        storage_state_uri: Optional[str] = None,
        max_tabs: int = 2,
    ) -> ProviderContext:
        key = (tenant_id, target_id, identity_ref)
        existing = self._context_index.get(key)
        if existing is not None:
            return self._contexts[existing]

        browser = self._select_browser_for(tenant_id, worker_id)
        context = ProviderContext(
            context_id=self._new_id("ctx"),
            instance_id=browser.instance_id,
            tenant_id=tenant_id,
            target_id=target_id,
            identity_ref=identity_ref,
            conversation_model=conversation_model,
            fingerprint_id=fingerprint_id,
            proxy_id=proxy_id,
            storage_state_uri=storage_state_uri,
            max_tabs=max_tabs,
        )
        browser.context_count += 1
        self._contexts[context.context_id] = context
        self._context_index[key] = context.context_id
        return context

    def place_context_on_browser(
        self, context: ProviderContext, browser: BrowserInstance
    ) -> None:
        """Explicit cross-check used to assert INV-5 / ceilings.

        Raises :class:`TenantIsolationError` if tenants differ, or
        :class:`CeilingExceededError` if the browser is full.
        """

        if context.tenant_id != browser.tenant_id:
            raise TenantIsolationError(
                "context tenant %r cannot be placed on browser tenant %r"
                % (context.tenant_id, browser.tenant_id)
            )
        if browser.context_count >= self.context_ceiling:
            raise CeilingExceededError(
                "browser %s is at context ceiling %d"
                % (browser.instance_id, self.context_ceiling)
            )
        context.instance_id = browser.instance_id
        browser.context_count += 1

    # -- tab accounting ----------------------------------------------------
    def register_tab(self, tab: ChannelTab) -> None:
        context = self._contexts[tab.context_id]
        browser = self._browsers[context.instance_id]
        if browser.tab_count >= self.tab_ceiling:
            raise CeilingExceededError(
                "browser %s is at tab ceiling %d" % (browser.instance_id, self.tab_ceiling)
            )
        browser.tab_count += 1

    def unregister_tab(self, tab: ChannelTab) -> None:
        context = self._contexts.get(tab.context_id)
        if context is None:
            return
        browser = self._browsers.get(context.instance_id)
        if browser is not None and browser.tab_count > 0:
            browser.tab_count -= 1


__all__ = [
    "BrowserInstance",
    "ChannelTab",
    "CeilingExceededError",
    "ConversationModel",
    "Fleet",
    "InstanceState",
    "Lane",
    "ProviderContext",
    "TabState",
    "TenantIsolationError",
]

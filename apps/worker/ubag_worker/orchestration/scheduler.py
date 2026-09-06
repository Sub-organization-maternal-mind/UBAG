"""Scheduling & fairness across tab pools — §12.13.

Provides weighted scheduling across pools on a browser that respects the five
priority lanes (§14.4) and per-provider concurrency tokens (§12.9), so a
``bulk``-lane provider can't starve a ``critical`` job on another provider. Adds
anti-starvation (queued jobs gain priority with age) and sticky multi-turn
(a job whose conversation is currently busy queues behind its owning tab,
INV-1) via an injectable ``is_conversation_busy`` callback plus internal
per-conversation FIFO serialization: jobs sharing a conversation key run
strictly in submission order and never in parallel, while distinct
conversations stay parallel under the existing per-provider caps.

Work-stealing *within* a single pool is implemented in
:mod:`ubag_worker.orchestration.channel_pool` (a ready tab pulls the head of its
pool queue); this module coordinates *across* pools.
"""

from __future__ import annotations

import time
from dataclasses import dataclass
from typing import Callable, Dict, List, Optional

from .topology import Lane


@dataclass
class ScheduledJob:
    """A job waiting for a cross-pool scheduling decision."""

    job_id: str
    provider: str  # provider+identity scheduling key
    lane: Lane = Lane.NORMAL
    enqueued_at: float = 0.0
    conversation_id: Optional[str] = None


class WeightedScheduler:
    """Lane-weighted, token-limited, anti-starvation scheduler.

    Parameters
    ----------
    provider_limits:
        Maximum concurrent in-flight jobs per provider key (concurrency tokens).
        A missing provider defaults to ``default_limit``.
    default_limit:
        Token budget for providers not present in ``provider_limits``.
    aging_interval:
        Seconds of age that buy one full lane of priority (anti-starvation).
    clock:
        Injectable clock returning seconds.
    is_conversation_busy:
        Optional callback; when it returns ``True`` for a job's
        ``conversation_id`` the job is held back (sticky multi-turn, INV-1).
    """

    def __init__(
        self,
        *,
        provider_limits: Optional[Dict[str, int]] = None,
        default_limit: int = 1,
        aging_interval: float = 10.0,
        clock: Callable[[], float] = time.monotonic,
        is_conversation_busy: Optional[Callable[[str], bool]] = None,
    ) -> None:
        if aging_interval <= 0:
            raise ValueError("aging_interval must be > 0")
        self._limits = dict(provider_limits or {})
        self._default_limit = int(default_limit)
        self._aging_interval = float(aging_interval)
        self._clock = clock
        self._is_conversation_busy = is_conversation_busy
        self._queue: List[ScheduledJob] = []
        self._inflight: Dict[str, int] = {}
        # Per-conversation FIFO serialization (INV-1). The serialization key is
        # (provider scheduling key, conversation_id) — the scheduler's available
        # projection of (tenant, app, target, conversation_key). At most one job
        # per key is ever in flight, and among queued same-key jobs only the
        # earliest-submitted (queue head) is eligible, so a conversation's turns
        # run strictly in submission order and never interleave. Jobs with no
        # conversation_id carry no key and take the existing path unchanged.
        self._conversation_inflight: set = set()

    # -- introspection -----------------------------------------------------
    @property
    def pending(self) -> int:
        return len(self._queue)

    def inflight(self, provider: str) -> int:
        return self._inflight.get(provider, 0)

    def limit_for(self, provider: str) -> int:
        return self._limits.get(provider, self._default_limit)

    def _now(self, now: Optional[float]) -> float:
        return self._clock() if now is None else now

    # -- queue management --------------------------------------------------
    def submit(self, job: ScheduledJob, now: Optional[float] = None) -> None:
        if not job.enqueued_at:
            job.enqueued_at = self._now(now)
        self._queue.append(job)

    def effective_priority(self, job: ScheduledJob, now: float) -> float:
        """Lower is more urgent. Age subtracts from the lane value (boost)."""

        age = max(0.0, now - job.enqueued_at)
        boost = age / self._aging_interval
        return float(int(job.lane)) - boost

    def _conversation_key(self, job: ScheduledJob):
        """Serialization key for a job, or ``None`` when it has no conversation.

        Combines the provider scheduling key with ``conversation_id`` so that
        distinct conversations (and jobs on different providers) never serialize
        against each other, while every turn of one conversation shares a key.
        """

        if not job.conversation_id:
            return None
        return (job.provider, job.conversation_id)

    def _conversation_head(self, conv_key) -> Optional[ScheduledJob]:
        """The earliest-submitted queued job sharing ``conv_key`` (FIFO head).

        ``self._queue`` preserves submission order, so the first match is the
        job that must run next for that conversation.
        """

        for job in self._queue:
            if self._conversation_key(job) == conv_key:
                return job
        return None

    def _eligible(self, job: ScheduledJob) -> bool:
        if self.inflight(job.provider) >= self.limit_for(job.provider):
            return False
        if (
            job.conversation_id
            and self._is_conversation_busy is not None
            and self._is_conversation_busy(job.conversation_id)
        ):
            return False
        conv_key = self._conversation_key(job)
        if conv_key is not None:
            # Strict per-conversation FIFO: a turn is ineligible while another
            # turn of the same conversation is in flight, or while an
            # earlier-submitted turn of it is still queued ahead of it.
            if conv_key in self._conversation_inflight:
                return False
            if self._conversation_head(conv_key) is not job:
                return False
        return True

    def pick_next(self, now: Optional[float] = None) -> Optional[ScheduledJob]:
        """Select and dispatch the most urgent eligible job, consuming a token."""

        now = self._now(now)
        best: Optional[ScheduledJob] = None
        best_key = None
        for job in self._queue:
            if not self._eligible(job):
                continue
            key = (self.effective_priority(job, now), job.enqueued_at)
            if best is None or key < best_key:
                best = job
                best_key = key
        if best is None:
            return None
        self._queue.remove(best)
        self._inflight[best.provider] = self.inflight(best.provider) + 1
        conv_key = self._conversation_key(best)
        if conv_key is not None:
            self._conversation_inflight.add(conv_key)
        return best

    def complete(self, job: ScheduledJob) -> None:
        current = self.inflight(job.provider)
        if current > 0:
            self._inflight[job.provider] = current - 1
        conv_key = self._conversation_key(job)
        if conv_key is not None:
            self._conversation_inflight.discard(conv_key)


__all__ = ["Lane", "ScheduledJob", "WeightedScheduler"]

"""Global submit pacer per (provider, identity) — §12.9.

A token-bucket-style spacer that enforces a jittered minimum gap between prompt
submissions across *all* tabs of a provider+identity, so N tabs never fire
simultaneously (the #1 cause of CAPTCHAs/bans). The RNG and clock are injectable
so jitter is fully deterministic in tests.

This is scheduling only — it decides *when* a submit may proceed, never *what* is
submitted, and touches no browser or network.
"""

from __future__ import annotations

import random
import time
from typing import Callable, Dict, Optional, Tuple


class SubmitPacer:
    """Spaces submissions for a single (provider, identity) key.

    Parameters
    ----------
    base_gap:
        Minimum gap between submissions in seconds (default 0.8s == 800ms).
    jitter:
        Symmetric jitter magnitude in seconds (default 0.4s == ±400ms).
    rng:
        Injectable :class:`random.Random` for deterministic jitter.
    clock:
        Injectable clock returning seconds.
    """

    def __init__(
        self,
        *,
        base_gap: float = 0.8,
        jitter: float = 0.4,
        rng: Optional[random.Random] = None,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        if base_gap < 0:
            raise ValueError("base_gap must be >= 0")
        if jitter < 0:
            raise ValueError("jitter must be >= 0")
        self._base_gap = float(base_gap)
        self._jitter = float(jitter)
        self._rng = rng if rng is not None else random.Random()
        self._clock = clock
        self._next_allowed = 0.0

    @property
    def next_allowed(self) -> float:
        return self._next_allowed

    def allow(self, now: Optional[float] = None) -> bool:
        """Return whether a submit may proceed at ``now`` without consuming."""

        now = self._clock() if now is None else now
        return now >= self._next_allowed

    def _next_gap(self) -> float:
        if self._jitter == 0:
            return self._base_gap
        return self._base_gap + self._rng.uniform(-self._jitter, self._jitter)

    def acquire(self, now: Optional[float] = None) -> float:
        """Reserve the next submit slot.

        Returns the timestamp at which the submit may proceed (>= ``now``) and
        advances the internal gate by a freshly jittered gap.
        """

        now = self._clock() if now is None else now
        proceed_at = max(now, self._next_allowed)
        self._next_allowed = proceed_at + self._next_gap()
        return proceed_at


class SubmitPacerRegistry:
    """Lazily creates and caches one :class:`SubmitPacer` per provider+identity."""

    def __init__(
        self,
        *,
        base_gap: float = 0.8,
        jitter: float = 0.4,
        rng_factory: Optional[Callable[[Tuple[str, str]], random.Random]] = None,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        self._base_gap = base_gap
        self._jitter = jitter
        self._rng_factory = rng_factory
        self._clock = clock
        self._pacers: Dict[Tuple[str, str], SubmitPacer] = {}

    def get(self, provider_id: str, identity_ref: str) -> SubmitPacer:
        key = (provider_id, identity_ref)
        pacer = self._pacers.get(key)
        if pacer is None:
            rng = self._rng_factory(key) if self._rng_factory is not None else None
            pacer = SubmitPacer(
                base_gap=self._base_gap,
                jitter=self._jitter,
                rng=rng,
                clock=self._clock,
            )
            self._pacers[key] = pacer
        return pacer


__all__ = ["SubmitPacer", "SubmitPacerRegistry"]

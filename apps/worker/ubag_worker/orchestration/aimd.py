"""AIMD adaptive concurrency controller per (provider, identity) — §12.9.

Treats each provider+identity as a system with unknown, changing "patience".
Additive-increase the concurrent-tab ceiling after a sustained success window;
multiplicative-decrease (and cooldown) the instant a negative signal arrives.
Every cap change is emitted as a :class:`CapChange` record for observability.

INV-4: provider patience is a managed resource. This controller never touches a
real browser; the clock is injectable for deterministic tests.
"""

from __future__ import annotations

import time
from dataclasses import dataclass
from enum import Enum
from typing import Callable, List, Optional


class NegativeSignal(Enum):
    """Structured back-off signals reported by adapters (§12.9)."""

    HTTP_429 = "http_429"
    CAPTCHA = "captcha"
    UNEXPECTED_LOGOUT = "unexpected_logout"
    THROTTLE_KEYWORD = "throttle_keyword"
    LATENCY_GUARD = "latency_guard"
    ERROR_RATE_SPIKE = "error_rate_spike"


@dataclass(frozen=True)
class CapChange:
    """An observable record of a tab-ceiling change."""

    old: int
    new: int
    reason: str
    ts: float


class AIMDController:
    """Additive-increase / multiplicative-decrease tab-ceiling controller.

    Parameters
    ----------
    start:
        Initial conservative ceiling (default 2).
    floor:
        Minimum ceiling after cuts (default 1).
    ceiling_max:
        Optional hard upper bound for additive increase (default unbounded).
    success_window:
        Number of consecutive successes required before a +1 increase.
    cooldown:
        Seconds after a negative signal during which increases are suppressed.
    decrease_factor:
        Multiplicative cut applied on a negative signal (default 0.5).
    clock:
        Injectable monotonic-ish clock returning seconds.
    """

    def __init__(
        self,
        *,
        start: int = 2,
        floor: int = 1,
        ceiling_max: Optional[int] = None,
        success_window: int = 5,
        cooldown: float = 30.0,
        decrease_factor: float = 0.5,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        if start < floor:
            raise ValueError("start must be >= floor")
        self._cap = int(start)
        self._floor = int(floor)
        self._ceiling_max = ceiling_max
        self._success_window = int(success_window)
        self._cooldown = float(cooldown)
        self._decrease_factor = float(decrease_factor)
        self._clock = clock
        self._successes = 0
        self._cooldown_until = 0.0
        self.cap_changes: List[CapChange] = []

    @property
    def cap(self) -> int:
        return self._cap

    @property
    def floor(self) -> int:
        return self._floor

    def in_cooldown(self, now: Optional[float] = None) -> bool:
        now = self._clock() if now is None else now
        return now < self._cooldown_until

    def record_success(self, now: Optional[float] = None) -> Optional[CapChange]:
        """Count a success; additively increase by 1 once the window is met.

        Increases are suppressed while in cooldown. Returns the emitted
        :class:`CapChange` when the ceiling grows, else ``None``.
        """

        now = self._clock() if now is None else now
        self._successes += 1
        if self.in_cooldown(now):
            return None
        if self._successes < self._success_window:
            return None
        self._successes = 0
        if self._ceiling_max is not None and self._cap >= self._ceiling_max:
            return None
        old = self._cap
        self._cap = old + 1
        change = CapChange(old=old, new=self._cap, reason="additive_increase", ts=now)
        self.cap_changes.append(change)
        return change

    def record_signal(
        self, signal: NegativeSignal, now: Optional[float] = None
    ) -> CapChange:
        """Multiplicatively cut the ceiling and enter cooldown.

        Always emits a :class:`CapChange` (even when already at the floor) so the
        cooldown entry is observable.
        """

        now = self._clock() if now is None else now
        old = self._cap
        new = max(self._floor, int(old * self._decrease_factor))
        self._cap = new
        self._successes = 0
        self._cooldown_until = now + self._cooldown
        change = CapChange(old=old, new=new, reason=signal.value, ts=now)
        self.cap_changes.append(change)
        return change


__all__ = ["AIMDController", "CapChange", "NegativeSignal"]

"""Humanized browser interaction helpers (§13.2).

Provides variable-speed typing and Bézier-curve mouse-movement calculations
that make automated browser sessions harder to detect as bots.

All functions are pure Python (no browser library required) and are
fully testable without a browser installed.

Security note: these helpers solely simulate *human timing/movement* for
accessibility-owned UI flows.  They do not bypass CAPTCHAs, fill credentials,
or perform any action the user could not perform manually.
"""

from __future__ import annotations

import math
import random
import time
from dataclasses import dataclass, field
from typing import List, Optional, Tuple


# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------


@dataclass
class HumanizedConfig:
    """Parameters that control human-simulation behaviour."""

    # Typing speed in characters per second (normal human: 40–70 cps ≈ 200–350 wpm)
    cps_base: float = 8.0
    # ±fraction jitter applied to each inter-character delay
    cps_jitter: float = 0.35
    # Probability of a "typo + backspace" on any given character [0, 1]
    typo_probability: float = 0.02
    # Fraction of mouse-path control-point randomisation
    bezier_jitter: float = 0.25
    # Number of discrete steps in a Bézier mouse path
    bezier_steps: int = 20


_DEFAULT_CONFIG = HumanizedConfig()

# Known ad/tracker domains blocked by the resource filter.
_BLOCKED_DOMAINS: Tuple[str, ...] = (
    "doubleclick.net",
    "googlesyndication.com",
    "googletagmanager.com",
    "googletagservices.com",
    "google-analytics.com",
    "analytics.google.com",
    "adservice.google.com",
    "hotjar.com",
    "scorecardresearch.com",
    "moatads.com",
    "adsystem.amazon.com",
    "amazon-adsystem.com",
    "criteo.com",
    "outbrain.com",
    "taboola.com",
)


# ---------------------------------------------------------------------------
# Typing delay generator
# ---------------------------------------------------------------------------


def typing_delays(
    text: str,
    config: Optional[HumanizedConfig] = None,
    rng: Optional[random.Random] = None,
) -> List[float]:
    """Return per-character inter-keystroke delays (in seconds) for ``text``.

    The delays simulate a human typist: each character has a base delay derived
    from ``config.cps_base`` with ±``config.cps_jitter`` random variation.
    A small probability ``config.typo_probability`` injects an extra pair of
    delays (the typo character + backspace correction).

    >>> delays = typing_delays("hi", HumanizedConfig(cps_base=10, cps_jitter=0.0, typo_probability=0.0))
    >>> len(delays) == 2
    True
    """
    cfg = config or _DEFAULT_CONFIG
    r = rng or random.Random()
    base = 1.0 / max(cfg.cps_base, 0.1)
    delays: List[float] = []
    for _ in text:
        jitter = r.uniform(-cfg.cps_jitter, cfg.cps_jitter)
        delay = max(0.01, base * (1.0 + jitter))
        # Inject typo + backspace delay pair
        if r.random() < cfg.typo_probability:
            delays.append(max(0.01, base * r.uniform(0.8, 1.2)))  # typo char
            delays.append(max(0.01, base * r.uniform(0.5, 0.9)))  # backspace
        delays.append(delay)
    return delays


def type_with_delays(
    text: str,
    type_char_fn,  # callable(char: str) -> None
    config: Optional[HumanizedConfig] = None,
    rng: Optional[random.Random] = None,
    clock=None,
) -> None:
    """Call ``type_char_fn(char)`` for each character in ``text`` with
    human-like inter-keystroke delays.

    :param type_char_fn: Called once per character (and once per backspace
        when a simulated typo is corrected). Receive the character string.
    :param clock: Optionally injectable ``time.sleep``-compatible callable
        for testing.
    """
    cfg = config or _DEFAULT_CONFIG
    r = rng or random.Random()
    sleep = clock if clock is not None else time.sleep
    base = 1.0 / max(cfg.cps_base, 0.1)

    for ch in text:
        # Maybe simulate a typo
        if r.random() < cfg.typo_probability:
            typo = _random_adjacent_key(ch, r)
            type_char_fn(typo)
            sleep(max(0.01, base * r.uniform(0.8, 1.2)))
            type_char_fn("\x08")  # backspace
            sleep(max(0.01, base * r.uniform(0.5, 0.9)))

        jitter = r.uniform(-cfg.cps_jitter, cfg.cps_jitter)
        sleep(max(0.01, base * (1.0 + jitter)))
        type_char_fn(ch)


def _random_adjacent_key(ch: str, rng: random.Random) -> str:
    """Return a plausible typo character adjacent to ``ch`` on a QWERTY layout."""
    adjacency = {
        "a": "sqwz", "b": "vghn", "c": "xdfv", "d": "erfsxc",
        "e": "wrsdf", "f": "rtgdce", "g": "tyhfv", "h": "yugjbn",
        "i": "uojko", "j": "uikhbn", "k": "iolmj", "l": "opk",
        "m": "njk", "n": "bhjm", "o": "iplk", "p": "ol",
        "q": "wa", "r": "etfd", "s": "qwaedz", "t": "rfgy",
        "u": "yhji", "v": "cfgb", "w": "qase", "x": "zsdc",
        "y": "tghu", "z": "asx",
    }
    options = adjacency.get(ch.lower(), ch)
    if not options:
        return ch
    result = rng.choice(options)
    return result.upper() if ch.isupper() else result


# ---------------------------------------------------------------------------
# Bézier mouse path
# ---------------------------------------------------------------------------


@dataclass
class Point:
    """2-D screen coordinate."""
    x: float
    y: float


def bezier_path(
    start: Point,
    end: Point,
    config: Optional[HumanizedConfig] = None,
    rng: Optional[random.Random] = None,
) -> List[Point]:
    """Generate a cubic Bézier mouse path from ``start`` to ``end``.

    Control points are placed at ⅓ and ⅔ of the straight line and perturbed
    by a fraction of the total distance, producing a natural-looking curved
    trajectory.

    Returns a list of ``config.bezier_steps`` points (including start/end).
    """
    cfg = config or _DEFAULT_CONFIG
    r = rng or random.Random()
    dx = end.x - start.x
    dy = end.y - start.y
    dist = math.hypot(dx, dy) or 1.0

    # Control points with ±bezier_jitter × dist perpendicular perturbation
    perp_x, perp_y = -dy / dist, dx / dist  # unit perpendicular

    def jittered_cp(frac: float) -> Point:
        cx = start.x + dx * frac + perp_x * dist * r.uniform(-cfg.bezier_jitter, cfg.bezier_jitter)
        cy = start.y + dy * frac + perp_y * dist * r.uniform(-cfg.bezier_jitter, cfg.bezier_jitter)
        return Point(cx, cy)

    p1 = jittered_cp(1 / 3)
    p2 = jittered_cp(2 / 3)

    steps = max(cfg.bezier_steps, 2)
    path: List[Point] = []
    for i in range(steps):
        t = i / (steps - 1)
        t_ = 1 - t
        x = (t_ ** 3) * start.x + 3 * (t_ ** 2) * t * p1.x + 3 * t_ * (t ** 2) * p2.x + (t ** 3) * end.x
        y = (t_ ** 3) * start.y + 3 * (t_ ** 2) * t * p1.y + 3 * t_ * (t ** 2) * p2.y + (t ** 3) * end.y
        path.append(Point(x, y))
    return path


# ---------------------------------------------------------------------------
# Resource filtering
# ---------------------------------------------------------------------------


def should_block_url(url: str) -> bool:
    """Return True if ``url`` matches a known ad/tracker domain (§13.2).

    Pure function — no network I/O.  Case-insensitive domain check.
    """
    lowered = url.lower()
    return any(domain in lowered for domain in _BLOCKED_DOMAINS)


__all__ = [
    "HumanizedConfig",
    "Point",
    "bezier_path",
    "should_block_url",
    "type_with_delays",
    "typing_delays",
]

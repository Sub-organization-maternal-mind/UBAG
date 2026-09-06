"""Output guards — quality checks for normalized responses (§16)."""

from __future__ import annotations

from typing import List


def check_length(text: str, min_chars: int = 10, max_chars: int = 50_000) -> bool:
    """Return True if ``text`` length is within [min_chars, max_chars]."""
    n = len(text)
    return min_chars <= n <= max_chars


def check_forbidden_phrases(text: str, phrases: List[str]) -> List[str]:
    """Return the subset of ``phrases`` found (case-insensitive) in ``text``.

    Returns an empty list when none match.
    """
    lower = text.lower()
    return [p for p in phrases if p.lower() in lower]

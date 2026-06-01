"""DOM signature baseline diffing (§13.5/§13.7).

Adapters call :func:`detect_drift` periodically to compare the live DOM
selector fingerprint against a known-good baseline.  The result drives
the worker's automatic quarantine and canary-rollback logic.

All computations are pure (no browser imports) so the module is fully
testable without Playwright.
"""

from __future__ import annotations

import enum
import hashlib
from dataclasses import dataclass, field
from typing import Dict, List, Optional


# ---------------------------------------------------------------------------
# Data types
# ---------------------------------------------------------------------------


class DriftSignal(str, enum.Enum):
    """Categorised drift observations."""

    SELECTOR_GONE = "selector_gone"
    """A selector from the baseline is no longer present in the live DOM."""

    SELECTOR_CHANGED = "selector_changed"
    """A selector is present but its DOM fingerprint hash has changed."""

    LAYOUT_SHIFT = "layout_shift"
    """The relative composition of elements has shifted significantly."""

    NEW_ELEMENT = "new_element"
    """A new, previously unseen selector appeared in the live DOM."""


@dataclass
class BaselineSnapshot:
    """Known-good DOM fingerprint for a provider at a specific adapter version."""

    provider_id: str
    """Provider identifier (e.g. 'chatgpt_web')."""

    version: str
    """Adapter version this baseline was captured for (e.g. '0.3.1')."""

    selector_hashes: Dict[str, str]
    """Map of selector key → SHA-256(element_count + tag_names) hash."""

    captured_at: str = ""
    """ISO-8601 UTC timestamp when the baseline was captured."""


@dataclass
class DriftReport:
    """Result of comparing a live DOM fingerprint to a baseline."""

    provider_id: str
    baseline_version: str
    signals: List[DriftSignal] = field(default_factory=list)
    details: Dict[str, str] = field(default_factory=dict)
    severity: float = 0.0
    """Normalised severity in [0.0, 1.0].  0 = no drift, 1 = all selectors gone."""

    @property
    def has_drift(self) -> bool:
        """True when any drift signal was detected."""
        return len(self.signals) > 0

    @property
    def is_critical(self) -> bool:
        """True when severity exceeds 0.5 (more than half of selectors affected)."""
        return self.severity > 0.5


# ---------------------------------------------------------------------------
# Public API
# ---------------------------------------------------------------------------


def build_baseline(
    provider_id: str,
    version: str,
    selectors: Dict[str, str],
    captured_at: str = "",
) -> BaselineSnapshot:
    """Construct a :class:`BaselineSnapshot` from a raw ``selectors`` map.

    ``selectors`` maps selector key → raw value (e.g. a count or tag name).
    The value is hashed so the baseline never stores raw DOM data.
    """
    hashed = {key: _hash_value(val) for key, val in selectors.items()}
    return BaselineSnapshot(
        provider_id=provider_id,
        version=version,
        selector_hashes=hashed,
        captured_at=captured_at,
    )


def detect_drift(
    live_selectors: Dict[str, str],
    baseline: BaselineSnapshot,
) -> DriftReport:
    """Compare ``live_selectors`` to ``baseline`` and return a :class:`DriftReport`.

    ``live_selectors`` must have the same key space as ``baseline.selector_hashes``
    to allow a meaningful comparison.

    Severity is computed as::

        severity = (gone + changed) / max(1, total_baseline_keys)

    New elements found in live but absent from the baseline are flagged as
    :attr:`DriftSignal.NEW_ELEMENT` but do not contribute to severity.
    """
    report = DriftReport(
        provider_id=baseline.provider_id,
        baseline_version=baseline.version,
    )

    baseline_keys = set(baseline.selector_hashes)
    live_keys = set(live_selectors)

    gone = baseline_keys - live_keys
    new_elements = live_keys - baseline_keys
    common = baseline_keys & live_keys

    changed = {
        key
        for key in common
        if _hash_value(live_selectors[key]) != baseline.selector_hashes[key]
    }

    affected = len(gone) + len(changed)

    # Detect significant layout shift: if ≥ 30 % of shared keys changed, flag it
    if common and len(changed) / len(common) >= 0.30:
        report.signals.append(DriftSignal.LAYOUT_SHIFT)
        report.details["layout_shift_ratio"] = f"{len(changed) / len(common):.2f}"

    for key in sorted(gone):
        report.signals.append(DriftSignal.SELECTOR_GONE)
        report.details[f"gone:{key}"] = "not found in live DOM"

    for key in sorted(changed):
        report.signals.append(DriftSignal.SELECTOR_CHANGED)
        report.details[f"changed:{key}"] = "hash mismatch"

    for key in sorted(new_elements):
        report.signals.append(DriftSignal.NEW_ELEMENT)
        report.details[f"new:{key}"] = "not in baseline"

    total = max(1, len(baseline_keys))
    report.severity = min(1.0, affected / total)

    return report


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _hash_value(value: str) -> str:
    """Return a short SHA-256 hex digest of ``value``."""
    return hashlib.sha256(value.encode()).hexdigest()[:16]

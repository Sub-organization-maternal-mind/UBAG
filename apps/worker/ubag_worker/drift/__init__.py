"""DOM signature baseline diffing for adapter drift detection (§13.5/§13.7)."""
from .detector import (
    BaselineSnapshot,
    DriftReport,
    DriftSignal,
    build_baseline,
    detect_drift,
)

__all__ = [
    "BaselineSnapshot",
    "DriftReport",
    "DriftSignal",
    "build_baseline",
    "detect_drift",
]

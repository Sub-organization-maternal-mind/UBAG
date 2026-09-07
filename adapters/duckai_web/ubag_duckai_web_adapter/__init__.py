"""Duck.ai Web manual-session adapter package."""

from .adapter import (
    DuckaiWebAdapter,
    build_duckai_web_events,
    build_duckai_web_live_events,
)

__all__ = [
    "DuckaiWebAdapter",
    "build_duckai_web_events",
    "build_duckai_web_live_events",
]

"""DeepSeek Web manual-session adapter package."""

from .adapter import (
    DeepSeekWebAdapter,
    build_deepseek_web_events,
    build_deepseek_web_live_events,
)

__all__ = [
    "DeepSeekWebAdapter",
    "build_deepseek_web_events",
    "build_deepseek_web_live_events",
]

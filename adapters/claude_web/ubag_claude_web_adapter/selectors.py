"""Claude Web selector configuration.

TODO(drift): re-confirm selectors against https://claude.ai/ and bump
SELECTORS.selector_version when verified.
"""

from __future__ import annotations

from ubag_worker.live.selectors import CLAUDE_WEB as SELECTORS

__all__ = ["SELECTORS"]

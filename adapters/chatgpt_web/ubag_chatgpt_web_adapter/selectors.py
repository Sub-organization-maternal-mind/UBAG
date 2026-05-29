"""ChatGPT Web selector configuration.

Centralizes the provider's selectors and drift baseline. The canonical
definition (with ordered fallbacks and ``baseline_version`` markers) lives in
``ubag_worker.live.selectors``; this module re-exports it so per-adapter
tooling and drift audits have a single import point.

TODO(drift): re-confirm selectors against https://chatgpt.com/ and bump
SELECTORS.selector_version when verified.
"""

from __future__ import annotations

from ubag_worker.live.selectors import CHATGPT_WEB as SELECTORS

__all__ = ["SELECTORS"]

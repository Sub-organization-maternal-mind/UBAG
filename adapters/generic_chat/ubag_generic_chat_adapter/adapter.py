"""Safe-mode Generic Chat adapter scaffold.

This stub intentionally performs no browser automation. It exists so manifests,
registry loading, and worker fail-safe behavior can be validated before a real
manual-session runtime is available.
"""

from __future__ import annotations

from typing import Any, Iterable, List, Mapping


class GenericChatAdapter:
    """Fail-closed placeholder for config-driven chat targets."""

    name = "generic_chat"
    version = "0.0.0"
    status = "stub"

    def iter_events(self, payload: Mapping[str, Any]) -> Iterable[Mapping[str, Any]]:
        raise NotImplementedError(
            "Generic Chat adapter is a safe-mode scaffold only. It requires a "
            "user-owned manual browser session runtime; UBAG will not collect "
            "credentials, scrape cookies, or solve CAPTCHA challenges."
        )

    def run(self, payload: Mapping[str, Any]) -> List[Mapping[str, Any]]:
        return list(self.iter_events(payload))


def build_generic_chat_events(payload: Mapping[str, Any]) -> List[Mapping[str, Any]]:
    return GenericChatAdapter().run(payload)

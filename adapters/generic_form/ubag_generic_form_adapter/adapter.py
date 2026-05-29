"""Safe-mode Generic Form adapter scaffold."""

from __future__ import annotations

from typing import Any, Iterable, List, Mapping


class GenericFormAdapter:
    """Fail-closed placeholder for config-driven form targets."""

    name = "generic_form"
    version = "0.0.0"
    status = "stub"

    def iter_events(self, payload: Mapping[str, Any]) -> Iterable[Mapping[str, Any]]:
        raise NotImplementedError(
            "Generic Form adapter is a safe-mode scaffold only. It requires a "
            "user-owned manual browser session runtime; UBAG will not collect "
            "credentials, scrape cookies, or solve CAPTCHA challenges."
        )

    def run(self, payload: Mapping[str, Any]) -> List[Mapping[str, Any]]:
        return list(self.iter_events(payload))


def build_generic_form_events(payload: Mapping[str, Any]) -> List[Mapping[str, Any]]:
    return GenericFormAdapter().run(payload)

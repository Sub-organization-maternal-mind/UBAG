"""Config-driven engine selection (blueprint §13.10).

Production drives PlaywrightPageDriver directly; this module only parses the
environment into the duck-typed spec the driver's ``_resolve_launch_plan``
reads (``kind.value``, ``is_remote``, ``remote_endpoint``, ``headed``).
"""

from __future__ import annotations

import enum
import os
from dataclasses import dataclass, field
from typing import Mapping, Optional


class EngineKind(enum.Enum):
    """Browser engine family."""

    CHROMIUM = "chromium"
    FIREFOX = "firefox"
    WEBKIT = "webkit"
    BIDI = "bidi"


@dataclass(frozen=True)
class EngineSpec:
    """Declarative engine selection consumed by PlaywrightPageDriver."""

    kind: EngineKind = EngineKind.CHROMIUM
    remote_endpoint: Optional[str] = None
    headed: bool = False
    extra: Mapping[str, object] = field(default_factory=dict)

    @property
    def is_remote(self) -> bool:
        return bool(self.remote_endpoint)


def _env_bool(name: str) -> bool:
    return os.environ.get(name, "").strip().lower() in ("1", "true", "yes", "on")


def _engine_kind_from_env(value: str) -> EngineKind:
    try:
        return EngineKind(value.strip().lower())
    except ValueError as exc:
        raise ValueError(
            "unknown UBAG_BROWSER_ENGINE %r (expected one of: %s)"
            % (value, ", ".join(k.value for k in EngineKind))
        ) from exc


def engine_spec_from_env() -> EngineSpec:
    """Build an :class:`EngineSpec` purely from environment variables.

    Recognized variables:

    * ``UBAG_BROWSER_ENGINE`` - chromium | firefox | webkit | bidi
    * ``UBAG_REMOTE_BROWSER_ENDPOINT`` - remote grid URL (§13.11)
    * ``UBAG_BROWSER_HEADED`` - truthy to launch headed
    """

    kind_env = os.environ.get("UBAG_BROWSER_ENGINE", "").strip()
    kind = _engine_kind_from_env(kind_env) if kind_env else EngineKind.CHROMIUM
    endpoint = os.environ.get("UBAG_REMOTE_BROWSER_ENDPOINT", "").strip() or None

    return EngineSpec(
        kind=kind,
        remote_endpoint=endpoint,
        headed=_env_bool("UBAG_BROWSER_HEADED"),
    )


__all__ = [
    "EngineKind",
    "EngineSpec",
    "engine_spec_from_env",
]

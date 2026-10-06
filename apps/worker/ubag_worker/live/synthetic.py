"""Synthetic chat provider overlay (``UBAG_SYNTHETIC_PROVIDER=1``).

Registers ONE extra live target, ``synthetic_chat``, backed by the local fixture in
``tools/synthetic-provider/server.mjs``. It exists so benchmark scenarios drive a real browser
through :class:`LiveSessionEngine` and the warm daemon; the ``mock`` target bypasses both.

Safe-mode: the target URL must be plain ``http`` on a loopback address, so this can never navigate
a browser to a real provider or any remote origin. A misconfigured URL leaves the target
unregistered (jobs for it fail with "no live selector configuration") rather than raising, because
this runs at import time of :mod:`selectors` and must not take the real providers down.

Deliberately defined outside ``selectors.py``: ``tools/provider-refresh/check-provider-selectors.mjs``
requires every ``ProviderSelectors`` block there to appear in ``adapters/registry.json``, and the
overlay is intentionally not in that registry (it lives in ``adapters/registry.synthetic.json``).
"""

from __future__ import annotations

import ipaddress
import os
import sys
from typing import MutableMapping, Optional
from urllib.parse import urlsplit

SYNTHETIC_PROVIDER_ID = "synthetic_chat"
ENABLE_ENV = "UBAG_SYNTHETIC_PROVIDER"
URL_ENV = "UBAG_SYNTHETIC_PROVIDER_URL"
DEFAULT_URL = "http://127.0.0.1:4799/"
# Bump when tools/synthetic-provider/server.mjs changes a data-synthetic marker.
SELECTOR_VERSION = "2026-10-06-synthetic-fixture-v1"


def synthetic_provider_enabled() -> bool:
    return os.environ.get(ENABLE_ENV, "").strip().lower() in ("1", "true", "yes", "on")


def validated_synthetic_url(raw: Optional[str] = None) -> str:
    """Return the fixture URL, or raise ValueError unless it is http on a loopback address."""
    url = (raw if raw is not None else os.environ.get(URL_ENV, "")).strip() or DEFAULT_URL
    message = "%s must be an http URL on a loopback address (e.g. %s)" % (URL_ENV, DEFAULT_URL)
    try:
        parts = urlsplit(url)
        parts.port  # noqa: B018 - raises ValueError for a non-numeric / out-of-range port
    except ValueError:
        raise ValueError(message)
    host = (parts.hostname or "").lower()
    if parts.scheme != "http" or not host or parts.username or parts.password:
        raise ValueError(message)
    if host != "localhost":
        try:
            loopback = ipaddress.ip_address(host).is_loopback
        except ValueError:
            loopback = False
        if not loopback:
            raise ValueError(message)
    return url


def build_synthetic_selectors(target_url: str):
    # Imported here: selectors.py imports this module at its end, so a module-level import would be circular.
    from .selectors import ProviderSelectors, SelectorGroup

    def group(name: str, *candidates: str) -> SelectorGroup:
        return SelectorGroup(name, candidates, baseline_version=SELECTOR_VERSION)

    return ProviderSelectors(
        provider_id=SYNTHETIC_PROVIDER_ID,
        display_name="Synthetic Chat (local fixture)",
        target_url=target_url,
        selector_version=SELECTOR_VERSION,
        prompt_input=group("prompt_input", "textarea[data-synthetic='prompt']"),
        submit_button=group("submit_button", "button[data-synthetic='send']"),
        response_container=group("response_container", "[data-synthetic='assistant-turn']"),
        authenticated_signal=group("authenticated_signal", "[data-synthetic='composer']"),
        login_signal=group("login_signal", "a[data-synthetic='sign-in']"),
        streaming_indicator=group("streaming_indicator", "button[data-synthetic='stop']"),
        drift_signature_nodes=("main", "[data-synthetic='composer']"),
        file_input=group("file_input", "input[type='file'][data-synthetic='file-input']"),
        new_chat=group("new_chat", "button[data-synthetic='new-chat']"),
    )


def register_synthetic_provider(registry: MutableMapping[str, object]) -> bool:
    """Add ``synthetic_chat`` to ``registry`` when enabled and correctly configured."""
    if not synthetic_provider_enabled():
        return False
    try:
        url = validated_synthetic_url()
    except ValueError as exc:
        sys.stderr.write("[ubag-worker] %s set but synthetic_chat not registered: %s\n" % (ENABLE_ENV, exc))
        return False
    registry[SYNTHETIC_PROVIDER_ID] = build_synthetic_selectors(url)
    return True

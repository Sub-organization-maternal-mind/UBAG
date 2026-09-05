"""Safe-mode secret-material scanner (leaf module).

One scanner, no imports from adapter_registry or engine — this is the trust
boundary every worker path (live engine, registry validation, mock adapter)
shares. Extracted verbatim from adapter_registry; the registry and
live.envelope re-export these names for compatibility.
"""

from __future__ import annotations

import re
from typing import Any, Mapping

_DISALLOWED_SECRET_KEYS = {
    "access_token",
    "api_key",
    "apikey",
    "auth_token",
    "cookie",
    "cookies",
    "credential",
    "credentials",
    "captcha_response",
    "captcha_solution",
    "captcha_token",
    "id_token",
    "mfa_code",
    "novnc_url",
    "password",
    "private_key",
    "refresh_token",
    "secret",
    "session",
    "session_cookie",
    "session_state",
    "storage_state",
    "authorization",
    "bearer",
    "set_cookie",
    "totp",
    "x_api_key",
}
_DISALLOWED_SECRET_SEGMENTS = {
    "authorization",
    "bearer",
    "captcha",
    "cookie",
    "cookies",
    "credential",
    "credentials",
    "mfa",
    "password",
    "secret",
    "token",
    "totp",
}
_DISALLOWED_COMPACT_SECRET_MARKERS = (
    "apikey",
    "privatekey",
    "storagestate",
    "sessionstate",
    "sessiontoken",
    "accesskey",
)

_BEARER_VALUE_PATTERN = re.compile(r"\bbearer\s+[A-Za-z0-9._~+/=-]{12,}", re.IGNORECASE)
_PRIVATE_KEY_VALUE_PATTERN = re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----", re.IGNORECASE | re.DOTALL)
_CAPTCHA_SOLVER_PATTERN = re.compile(
    r"\b(solve|bypass|delegate|outsource)\b.{0,40}\bcaptcha\b|\bcaptcha\b.{0,40}\b(solver|solving|bypass)\b",
    re.IGNORECASE,
)


def contains_disallowed_secret_material(value: Any) -> bool:
    """Recursively reject credential/cookie/token/captcha-solver material.

    Manual-session references (``manual_session``, ``session_id``) and
    secret *references* (``secret_id``/``secret_ref``) are allowed shapes.
    """
    if isinstance(value, Mapping):
        for key, child in value.items():
            normalized_key = _normalize_secret_key(str(key))
            if _is_disallowed_secret_key(normalized_key):
                return True
            if contains_disallowed_secret_material(child):
                return True
    elif isinstance(value, list):
        for child in value:
            if contains_disallowed_secret_material(child):
                return True
    elif isinstance(value, str):
        if _BEARER_VALUE_PATTERN.search(value) or _PRIVATE_KEY_VALUE_PATTERN.search(value) or _CAPTCHA_SOLVER_PATTERN.search(value):
            return True
    return False


# Historical private alias (engine, registry, and tests import this name).
_contains_disallowed_secret_material = contains_disallowed_secret_material


def _normalize_secret_key(value: str) -> str:
    value = re.sub(r"([a-z0-9])([A-Z])", r"\1_\2", value.strip())
    value = re.sub(r"[^A-Za-z0-9]+", "_", value)
    value = re.sub(r"_+", "_", value)
    return value.strip("_").lower()


def _is_disallowed_secret_key(normalized_key: str) -> bool:
    if normalized_key in ("manual_session", "session_id"):
        return False
    if _is_secret_reference_key(normalized_key):
        return False
    if normalized_key in _DISALLOWED_SECRET_KEYS:
        return True
    if any(segment in _DISALLOWED_SECRET_SEGMENTS for segment in normalized_key.split("_")):
        return True
    compact = normalized_key.replace("_", "")
    return any(marker in compact for marker in _DISALLOWED_COMPACT_SECRET_MARKERS)


def _is_secret_reference_key(normalized_key: str) -> bool:
    return normalized_key in ("secret_id", "secret_ref") or normalized_key.endswith(
        ("_secret_id", "_secret_ref")
    )

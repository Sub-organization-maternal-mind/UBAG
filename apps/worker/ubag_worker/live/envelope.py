"""Shared dispatch-envelope normalization for every worker path.

This module is the one place that parses the gateway dispatch envelope:
target, manual context, conversation binding, prompt, session/job/trace ids,
provider config, and attachments. The live engine, the registry stub path,
and the mock adapter all consume it instead of carrying divergent copies
(extracted verbatim from ubag_worker/live/engine.py, whose semantics are
canonical).

The one behavioral addition: errors raise ``EnvelopeError`` (a ValueError
subclass) instead of the engine's ``LiveSessionError``, because the registry
and mock paths historically raised ValueError. The engine re-raises as
LiveSessionError at its boundary so its public behavior is unchanged.
"""

from __future__ import annotations

import json
import os
import re
from datetime import datetime, timedelta
from typing import Any, Dict, List, Mapping, Optional, Tuple

from .events import canonical_json, digest
from .secret_scan import _contains_disallowed_secret_material  # noqa: F401 - re-export

DEFAULT_API_VERSION = "2026-05-22"
DEFAULT_COMMAND_TYPE = "chat.prompt"
DEFAULT_MANUAL_LOGIN_TIMEOUT_S = 300.0
DEFAULT_RESPONSE_TIMEOUT_S = 120.0

# Characters that could let a provider_config value break out of the Playwright
# selector it is interpolated into via ``.format(value=desired)`` in
# ``page_driver``: quotes, a backslash, or a newline. Parentheses, single
# quotes, and spaces are common in real provider UI labels and are safe inside
# the double-quoted :has-text("{value}") context.
PROVIDER_CONFIG_FORBIDDEN_CHARS = ('"', "\\", "\n", "\r")


class EnvelopeError(ValueError):
    """Invalid dispatch envelope (bad shape, forbidden material, bad keys)."""


class NormalizedJob:
    """The canonical parsed form of a dispatch envelope."""

    __slots__ = (
        "api_version",
        "job_id",
        "trace_id",
        "target",
        "command_type",
        "prompt",
        "options",
        "user_data_dir",
        "headless",
        "session_id",
        "account_binding_id",
        "consent_ref",
        "automation_scope",
        "manual_login_timeout_s",
        "response_timeout_s",
        "tenant_id",
        "conversation_id",
        "conversation_key",
        "conversation_thread_ref",
        "conversation_on_missing",
        "audio_artifact_key",
        "audio_local_path",
        "attachments",
        "attachment_local_paths",
        "wait_for_artifacts",
        "provider_config",
        "new_chat_enabled",
        "config_enabled",
    )

    def __init__(self, **kwargs: Any) -> None:
        for key in self.__slots__:
            setattr(self, key, kwargs[key])


def normalize_payload(payload: Mapping[str, Any], provider_id: str) -> NormalizedJob:
    """Parse a raw dispatch envelope Mapping into a NormalizedJob.

    Raises EnvelopeError on invalid shapes or forbidden attachment keys.
    """
    job_payload = payload.get("job", {})
    if not isinstance(job_payload, Mapping):
        job_payload = {}

    input_payload = _mapping_or_empty(job_payload.get("input", payload.get("input", {})))
    options = _mapping_or_empty(job_payload.get("options", payload.get("options", {})))

    api_version = _string_or_default(payload.get("api_version"), DEFAULT_API_VERSION)
    target = _string_or_default(job_payload.get("target", payload.get("target")), provider_id)
    command_type = _string_or_default(
        job_payload.get("command_type", job_payload.get("type")), DEFAULT_COMMAND_TYPE
    )
    prompt = _extract_prompt(input_payload, payload)

    job_id = _derive_job_id(payload, job_payload)
    trace_id = _string_or_default(payload.get("trace_id"), "trace_" + digest(job_id)[:16])

    context = _manual_context(payload)
    session_id = _safe_session_id(context.get("session_id"), job_id, target)

    user_data_dir = _resolve_user_data_dir(options, context, target)
    headless = bool(options.get("headless", False))

    account_binding_id = _clean_text(context.get("account_binding_id"), "unbound")
    tenant_id = _string_or_default(
        payload.get("tenant_id")
        or job_payload.get("tenant_id")
        or context.get("tenant_id"),
        "default",
    )
    conversation_id = _optional_string(
        input_payload.get("conversation_id")
        or options.get("conversation_id")
        or job_payload.get("conversation_id")
    )

    # Conversation-affinity block, injected into the envelope by the gateway only
    # when conversations are enabled and the job carries a conversation_id (see
    # executor.go DispatchConversation). Absent -> today's path exactly.
    conversation_key, conversation_thread_ref, conversation_on_missing = (
        _conversation_binding(payload)
    )

    # Optional audio-transcription inputs. ``audio_artifact_key`` names the job
    # artifact the caller uploaded; ``audio_local_path`` is the locally-materialized
    # file the worker runner/gateway resolved that key to (the engine never fetches
    # from the gateway itself). Text-only jobs leave all three empty.
    audio_artifact_key = _audio_artifact_key(input_payload)
    audio_local_path = _optional_string(
        input_payload.get("audio_local_path") or options.get("audio_local_path")
    )
    # Generalized multi-file attachments. ``attachments`` is the declared manifest
    # (documents/images/audio/video/voice); ``attachment_local_paths`` are the
    # locally-materialized files the gateway resolved those keys to. Text-only
    # jobs leave both empty.
    attachments = _attachments_manifest(input_payload)
    attachment_local_paths = _string_tuple(
        input_payload.get("attachment_local_paths")
        or options.get("attachment_local_paths")
    )
    if attachments and len(attachments) != len(attachment_local_paths):
        raise EnvelopeError(
            "attachments and attachment_local_paths must contain the same number of entries"
        )
    wait_for_artifacts = _string_tuple(options.get("wait_for_artifacts"))

    # Resolve the pre-submit configuration: per-provider UBAG defaults live in the
    # selectors; an env var (UBAG_PROVIDER_CONFIG_<ID>) and the job options layer
    # on top so the always-on settings can change without a code release. The
    # reserved keys "_enabled" / "_new_chat" gate the whole phase.
    provider_config = _resolve_provider_config(provider_id, options)
    if provider_id == "deepseek_web" and attachments:
        # Live DeepSeek (verified 2026-07-24) removes its <input type=file> in
        # Expert mode and explicitly labels that mode as not supporting file
        # uploads. Instant and Vision expose the same multi-file input. Preserve
        # either compatible explicit choice; otherwise choose Instant so the
        # adapter's advertised attachment capability is actually usable.
        deepseek_mode = str(provider_config.get("mode", "")).strip()
        if deepseek_mode not in {"Instant", "Vision"}:
            provider_config["mode"] = "Instant"
    new_chat_enabled = _flag(
        provider_config.get("_new_chat"),
        _env_flag("UBAG_NEW_CHAT_ENABLED", True),
    )
    config_enabled = _flag(
        provider_config.get("_enabled"),
        _env_flag("UBAG_PROVIDER_CONFIG_ENABLED", True),
    )

    return NormalizedJob(
        api_version=api_version,
        job_id=job_id,
        trace_id=trace_id,
        target=target,
        command_type=command_type,
        prompt=prompt,
        options=options,
        user_data_dir=user_data_dir,
        headless=headless,
        session_id=session_id,
        account_binding_id=account_binding_id,
        consent_ref=_clean_text(context.get("consent_ref"), "unspecified"),
        automation_scope=_clean_scope(context.get("automation_scope")),
        manual_login_timeout_s=_float_or_default(
            options.get("manual_login_timeout_s"), DEFAULT_MANUAL_LOGIN_TIMEOUT_S
        ),
        response_timeout_s=_float_or_default(
            options.get("response_timeout_s"), DEFAULT_RESPONSE_TIMEOUT_S
        ),
        tenant_id=tenant_id,
        conversation_id=conversation_id,
        conversation_key=conversation_key,
        conversation_thread_ref=conversation_thread_ref,
        conversation_on_missing=conversation_on_missing,
        audio_artifact_key=audio_artifact_key,
        audio_local_path=audio_local_path,
        attachments=attachments,
        attachment_local_paths=attachment_local_paths,
        wait_for_artifacts=wait_for_artifacts,
        provider_config=provider_config,
        new_chat_enabled=new_chat_enabled,
        config_enabled=config_enabled,
    )


def manual_context(payload: Mapping[str, Any]) -> Mapping[str, Any]:
    """Merge the manual-session context from every historical envelope location."""
    return _manual_context(payload)


def _manual_context(payload: Mapping[str, Any]) -> Mapping[str, Any]:
    job_payload = payload.get("job", {})
    if not isinstance(job_payload, Mapping):
        job_payload = {}
    candidates = [
        payload.get("ownership"),
        payload.get("context"),
        job_payload.get("ownership"),
        job_payload.get("context"),
        job_payload,
        payload,
    ]
    merged: dict = {}
    for candidate in candidates:
        if isinstance(candidate, Mapping):
            nested = candidate.get("manual_session")
            if isinstance(nested, Mapping):
                for key, value in nested.items():
                    merged.setdefault(str(key), value)
            for key, value in candidate.items():
                merged.setdefault(str(key), value)
    return merged


def conversation_binding(payload: Mapping[str, Any]) -> Tuple[Optional[str], Optional[str], str]:
    """Extract the gateway's conversation-affinity block from the envelope.

    Returns ``(key, thread_ref, on_missing)``:

    * ``key`` is the caller-owned conversation key, or ``None`` when the gateway
      injected no block (conversations disabled, or the job carries none).
    * ``thread_ref`` is the bound provider chat URL to resume, or ``None`` for an
      unseen key (first job -> bind after the response).
    * ``on_missing`` is ``"fail"`` (default) or ``"restart"``; any other value is
      normalized to the safe default ``"fail"``.
    """
    return _conversation_binding(payload)


def _conversation_binding(payload: Mapping[str, Any]) -> tuple:
    block = payload.get("conversation")
    if not isinstance(block, Mapping):
        return None, None, "fail"
    key = _optional_string(block.get("key"))
    if key is None:
        return None, None, "fail"
    thread_ref = _optional_string(block.get("thread_ref"))
    on_missing = str(block.get("on_missing") or "fail").strip().lower()
    if on_missing not in ("fail", "restart"):
        on_missing = "fail"
    return key, thread_ref, on_missing


def _resolve_user_data_dir(
    options: Mapping[str, Any], context: Mapping[str, Any], target: str
) -> str:
    for source in (options, context):
        for key in ("user_data_dir", "profile_dir", "profile_path"):
            value = source.get(key)
            if isinstance(value, str) and value.strip():
                return value.strip()
    env_dir = os.environ.get("UBAG_PROFILE_DIR", "").strip()
    if env_dir:
        return os.path.join(env_dir, target)
    return os.path.join("var", "profiles", target, "default")


def _safe_session_id(value: Any, job_id: str, target: str) -> str:
    if isinstance(value, str):
        candidate = value.strip()
        if re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,95}", candidate):
            return candidate
    return "sess_" + digest(job_id + target)[:16]


def _target_from_payload(payload: Any) -> str:
    """Tolerant target extraction shared by the live entrypoints.

    Checks ``payload["job"]["target"]`` first (the standard API envelope
    shape), then ``payload["target"]``, then defaults to ``"mock"``. Never
    raises — the strict (raising) variant lives in adapter_registry.
    """
    if not isinstance(payload, Mapping):
        return "mock"
    job_field = payload.get("job", {})
    if not isinstance(job_field, Mapping):
        job_field = {}
    return str(job_field.get("target", payload.get("target", "mock")))


def _derive_job_id(payload: Mapping[str, Any], job_payload: Mapping[str, Any]) -> str:
    explicit_id = payload.get("job_id", job_payload.get("job_id", job_payload.get("id")))
    if explicit_id is not None:
        return str(explicit_id)
    idempotency_key = payload.get("idempotency_key")
    seed = str(idempotency_key) if idempotency_key is not None else canonical_json(payload)
    return "job_" + digest(seed)[:16]


_BASE_CLOCK = datetime(2026, 1, 1, 0, 0, 0)


def _worker_event(
    api_version: str,
    job_id: str,
    trace_id: str,
    sequence: int,
    event_type: str,
    data: Mapping[str, Any],
) -> Dict[str, Any]:
    """The canonical worker-event envelope shared by every worker path."""
    return {
        "api_version": api_version,
        "event_id": "evt_" + digest("%s:%s" % (job_id, sequence))[:16],
        "job_id": job_id,
        "trace_id": trace_id,
        "type": event_type,
        "sequence": sequence,
        "created_at": (_BASE_CLOCK + timedelta(milliseconds=250 * (sequence - 1))).isoformat(timespec="milliseconds") + "Z",
        "data": dict(data),
    }


def _extract_prompt(input_payload: Mapping[str, Any], payload: Mapping[str, Any]) -> str:
    for key in ("prompt", "text", "message", "content"):
        value = input_payload.get(key)
        if isinstance(value, str) and value:
            return value
    top_level_prompt = payload.get("prompt")
    if isinstance(top_level_prompt, str) and top_level_prompt:
        return top_level_prompt
    if input_payload:
        return canonical_json(input_payload)
    return "empty prompt"


def _mapping_or_empty(value: Any) -> Mapping[str, Any]:
    return value if isinstance(value, Mapping) else {}


def _string_or_default(value: Any, default: str) -> str:
    if value is None:
        return default
    text = str(value)
    return text if text else default


def _clean_text(value: Any, default: str) -> str:
    if isinstance(value, str) and value.strip():
        return value.strip()
    return default


def _optional_string(value: Any) -> Optional[str]:
    if isinstance(value, str) and value.strip():
        return value.strip()
    return None


def _audio_artifact_key(input_payload: Mapping[str, Any]) -> Optional[str]:
    """Extract + validate ``input.audio_artifact_key``.

    Mirrors the gateway's artifact-key rule (a single path segment): rejects keys
    containing ``/``, ``\\``, ``%`` or NUL so a payload can never coerce the worker
    into reading outside the artifact namespace.
    """
    value = input_payload.get("audio_artifact_key")
    if value is None:
        return None
    key = str(value).strip()
    if not key:
        return None
    if any(ch in key for ch in ("/", "\\", "%", "\x00")):
        raise EnvelopeError(
            "audio_artifact_key must be a single path segment without '/', '\\', or '%'"
        )
    return key


def _attachments_manifest(input_payload: Mapping[str, Any]) -> tuple:
    """Extract + validate ``input.attachments``.

    Returns a tuple of ``{"key","filename","content_type","kind"}`` dicts in
    declared order. Each key is validated like ``audio_artifact_key`` (a single
    path segment) so a payload can never coerce the worker outside the artifact
    namespace. Returns an empty tuple for text jobs.
    """
    raw = input_payload.get("attachments")
    if raw is None:
        return ()
    if not isinstance(raw, (list, tuple)):
        raise EnvelopeError("attachments must be an array")
    manifest = []
    seen = set()
    for item in raw:
        if not isinstance(item, Mapping):
            raise EnvelopeError("each attachment must be an object")
        key = str(item.get("key", "")).strip()
        if not key:
            raise EnvelopeError("attachment.key is required")
        if key in (".", "..") or any(ch in key for ch in ("/", "\\", "%", "?", "\x00")):
            raise EnvelopeError(
                "attachment.key must be a unique safe path segment without '/', '\\', '%', or '?'"
            )
        if key in seen:
            raise EnvelopeError("attachment keys must be unique")
        seen.add(key)
        manifest.append(
            {
                "key": key,
                "filename": str(item.get("filename", "")).strip(),
                "content_type": str(item.get("content_type", "")).strip(),
                "kind": str(item.get("kind", "")).strip(),
            }
        )
    return tuple(manifest)


def _string_tuple(value: Any) -> tuple:
    if isinstance(value, (list, tuple)):
        return tuple(str(item).strip() for item in value if str(item).strip())
    if isinstance(value, str) and value.strip():
        return (value.strip(),)
    return ()


def _clean_scope(value: Any) -> List[str]:
    if isinstance(value, list) and value:
        return [str(item) for item in value]
    return ["manual_login", "submit_prompt", "read_response"]


def _float_or_default(value: Any, default: float) -> float:
    try:
        if value is None:
            return default
        return float(value)
    except (TypeError, ValueError):
        return default


def sanitize_provider_config_value(value: Any) -> Any:
    """Reject provider_config values that could break a Playwright selector.

    The gateway already validates ``model_settings`` against the target adapter's
    catalog, but this is defense in depth: every resolved ``desired`` value is
    interpolated into a selector template via ``.format(value=desired)`` in
    ``page_driver``, so a value carrying a quote, backslash, or newline could
    alter the selector's meaning. Booleans (toggle settings) pass through
    untouched; other non-string values are left as-is.
    """
    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        for char in PROVIDER_CONFIG_FORBIDDEN_CHARS:
            if char in value:
                raise EnvelopeError(
                    "provider_config value contains a disallowed selector "
                    "metacharacter: %r" % char
                )
    return value


# Historical private alias (engine and tests import this name).
_sanitize_provider_config_value = sanitize_provider_config_value


def resolve_provider_config(provider_id: str, options: Mapping[str, Any]) -> dict:
    """Merge per-provider config overrides from env + job options.

    Layering (lowest -> highest precedence): the provider's hardcoded defaults
    (applied later by the driver from the selectors), then an env var
    ``UBAG_PROVIDER_CONFIG_<ID>`` holding a JSON object, then the job's
    ``options.provider_config`` object. Reserved keys ``_enabled`` / ``_new_chat``
    gate the phase; other keys override a setting's desired value by its key.
    """
    return _resolve_provider_config(provider_id, options)


def _resolve_provider_config(provider_id: str, options: Mapping[str, Any]) -> dict:
    config: dict = {}
    env_key = "UBAG_PROVIDER_CONFIG_" + re.sub(r"[^A-Z0-9]+", "_", provider_id.upper())
    raw = os.environ.get(env_key, "").strip()
    if raw:
        try:
            parsed = json.loads(raw)
            if isinstance(parsed, Mapping):
                config.update(parsed)
        except (ValueError, TypeError):
            pass
    opt = options.get("provider_config")
    if isinstance(opt, Mapping):
        config.update(opt)
    return {key: sanitize_provider_config_value(val) for key, val in config.items()}


def flag(value: Any, default: bool) -> bool:
    """Coerce an optional JSON/string flag to bool, falling back to ``default``."""
    if value is None:
        return default
    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        return value.strip().lower() not in ("0", "false", "no", "off", "")
    return bool(value)


_flag = flag


def env_flag(name: str, default: bool) -> bool:
    raw = os.environ.get(name, "").strip().lower()
    if not raw:
        return default
    return raw not in ("0", "false", "no", "off")


_env_flag = env_flag


__all__ = [
    "DEFAULT_MANUAL_LOGIN_TIMEOUT_S",
    "DEFAULT_RESPONSE_TIMEOUT_S",
    "EnvelopeError",
    "NormalizedJob",
    "PROVIDER_CONFIG_FORBIDDEN_CHARS",
    "conversation_binding",
    "env_flag",
    "flag",
    "manual_context",
    "normalize_payload",
    "resolve_provider_config",
    "sanitize_provider_config_value",
]

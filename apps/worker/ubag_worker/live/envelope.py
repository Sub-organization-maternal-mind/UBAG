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
from typing import Any, Dict, List, Mapping, Optional, Tuple

from .events import canonical_json, digest
from .events import worker_event as _canonical_worker_event
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

    tenant_id = _string_or_default(
        payload.get("tenant_id")
        or job_payload.get("tenant_id")
        or context.get("tenant_id"),
        "default",
    )
    helper_profile_ref = _helper_profile_ref(payload)
    user_data_dir = _resolve_user_data_dir(
        options, context, target, tenant_id, helper_profile_ref
    )
    headless_raw = options.get("headless", False)
    if not isinstance(headless_raw, bool):
        raise EnvelopeError("options.headless must be a boolean")
    headless = headless_raw

    # Helper mode: the identity is the profile the primary bound, never a
    # caller-supplied context value (the helper spec carries none).
    account_binding_id = helper_profile_ref or _clean_text(
        context.get("account_binding_id"), "unbound"
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
    if audio_local_path:
        # Same exfiltration surface as attachment_local_paths: this path is
        # handed to set_input_files, so it must be a gateway-materialized file.
        audio_local_path = _validate_local_attachment_path(
            audio_local_path, "audio_local_path"
        )
    # Generalized multi-file attachments. ``attachments`` is the declared manifest
    # (documents/images/audio/video/voice); ``attachment_local_paths`` are the
    # locally-materialized files the gateway resolved those keys to. Text-only
    # jobs leave both empty.
    attachments = _attachments_manifest(input_payload)
    attachment_local_paths = _validated_local_path_tuple(
        input_payload.get("attachment_local_paths")
        or options.get("attachment_local_paths"),
        "attachment_local_paths",
    )
    if attachments and len(attachments) != len(attachment_local_paths):
        raise EnvelopeError(
            "attachments and attachment_local_paths must contain the same number of entries"
        )

    # Resolve the pre-submit configuration: per-provider UBAG defaults live in the
    # selectors; an env var (UBAG_PROVIDER_CONFIG_<ID>) and the job options layer
    # on top so the always-on settings can change without a code release. The
    # reserved keys "_enabled" / "_new_chat" gate the whole phase.
    provider_config = _resolve_provider_config(provider_id, options)
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


def _profile_root() -> str:
    """The worker's own browser-profile state directory.

    UBAG_PROFILE_DIR is the operator-configured root (the gateway forwards it
    to every worker subprocess); ``var/profiles`` is the built-in default.
    """
    return os.environ.get("UBAG_PROFILE_DIR", "").strip() or os.path.join(
        "var", "profiles"
    )


def _validate_profile_dir_value(value: str, key: str) -> str:
    """Constrain an explicitly requested profile directory to worker state.

    ``user_data_dir`` / ``profile_dir`` / ``profile_path`` are handed to
    Chromium as the persistent-context directory, so a hostile ``job:create``
    caller must not be able to aim the browser at an arbitrary filesystem
    location (its cookies, its SSH agent socket, ...). The gateway already
    rejects absolute paths and traversal structurally; this is the worker's
    authoritative containment check against its own profile root.
    """
    parts = [part for part in re.split(r"[/\\]+", value) if part not in ("", ".")]
    if ".." in parts:
        raise EnvelopeError("%s must not contain '..'" % key)
    if value.startswith(("/", "\\")) or os.path.isabs(value) or (
        len(value) >= 2 and value[1] == ":"
    ):
        root_abs = os.path.abspath(_profile_root())
        candidate_abs = os.path.abspath(value)
        try:
            inside = os.path.commonpath([root_abs, candidate_abs]) == root_abs
        except ValueError:
            # Different drives (Windows): never inside the profile root.
            inside = False
        if not inside:
            raise EnvelopeError(
                "%s must stay under the worker profile root (%s)" % (key, _profile_root())
            )
    return value


def _profile_options_policy() -> str:
    """UBAG_PROFILE_OPTIONS_POLICY: ``legacy`` (default) or ``namespaced``."""
    value = os.environ.get("UBAG_PROFILE_OPTIONS_POLICY", "").strip().lower()
    return "namespaced" if value == "namespaced" else "legacy"


def _tenant_segment(tenant_id: str) -> str:
    """A filesystem-safe, collision-free directory segment for a tenant id."""
    if re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", tenant_id):
        return tenant_id
    return "t_" + digest(tenant_id)[:16]


# The primary mints profile_ref as "pr_" + base32 (see helperauth.MintProfileRef)
# and the gateway accepts [A-Za-z0-9][A-Za-z0-9._:-]{0,127}. The worker is
# stricter: no ":" (an NTFS alternate-data-stream separator in a directory name).
_HELPER_PROFILE_REF_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}")


def _helper_mode() -> bool:
    """UBAG_HELPER_PLANE: this worker runs on a helper node for the primary.

    Set by the helper agent in the worker's environment, never by a job. It is
    not in the gateway's local-worker env allowlist, so the primary's own
    workers never run in this mode.
    """
    return env_flag("UBAG_HELPER_PLANE", False)


def _helper_profile_ref(payload: Mapping[str, Any]) -> Optional[str]:
    """The opaque profile_ref of a helper attempt, or None outside helper mode.

    Read ONLY from the top level of the payload, which the primary's
    HelperAttemptSpec fills: never from options or context, which a job:create
    caller controls. In helper mode a missing or malformed ref fails closed
    instead of falling back to a caller-selectable profile directory.
    """
    if not _helper_mode():
        return None
    ref = payload.get("profile_ref")
    if not isinstance(ref, str) or not _HELPER_PROFILE_REF_RE.fullmatch(ref):
        raise EnvelopeError(
            "helper mode requires a top-level profile_ref (opaque token, no path characters)"
        )
    return ref


def _resolve_user_data_dir(
    options: Mapping[str, Any],
    context: Mapping[str, Any],
    target: str,
    tenant_id: str = "default",
    helper_profile_ref: Optional[str] = None,
) -> str:
    profile_root = _profile_root()
    if helper_profile_ref is not None:
        # Helper node: the profile directory is derived from the primary-issued
        # profile_ref ALONE. The primary already bound the ref to one tenant,
        # provider and identity, and logins on this host are human-made inside
        # exactly this directory. user_data_dir, profile_dir, profile_path, the
        # tenant id and the target are all ignored.
        return os.path.join(profile_root, "helper", helper_profile_ref)
    if _profile_options_policy() == "namespaced":
        # A job:create caller can otherwise pick ANOTHER tenant's relative
        # profile directory (cookies/sessions). Under the namespaced policy the
        # caller-supplied user_data_dir/profile_dir/profile_path are ignored
        # and the profile lives in a tenant-owned subtree of the profile root.
        # This moves the default location versus legacy: enable it only with a
        # profile migration (see docs/perf-fleet/slices/P1.6.md).
        return os.path.join(profile_root, _tenant_segment(tenant_id), target, "default")
    for source in (options, context):
        for key in ("user_data_dir", "profile_dir", "profile_path"):
            value = source.get(key)
            if isinstance(value, str) and value.strip():
                return _validate_profile_dir_value(value.strip(), key)
    if os.environ.get("UBAG_PROFILE_DIR", "").strip():
        return os.path.join(profile_root, target)
    return os.path.join(profile_root, target, "default")


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


def _worker_event(
    api_version: str,
    job_id: str,
    trace_id: str,
    sequence: int,
    event_type: str,
    data: Mapping[str, Any],
) -> Dict[str, Any]:
    """The canonical worker-event envelope shared by every worker path."""
    return _canonical_worker_event(
        api_version=api_version,
        job_id=job_id,
        trace_id=trace_id,
        sequence=sequence,
        event_type=event_type,
        data=data,
    )


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


# The gateway materializes a job's declared attachments into the system temp
# directory (executor/workerconsumer.go: os.MkdirTemp("", "ubag-attach-*")). The
# bound enforced below is the temp ROOT, not that subdirectory name: the naming
# is an implementation detail of the runner, and pinning it would both encode a
# detail that can change without notice and reject legitimate attachments.
_MATERIALIZED_ATTACHMENT_DIR_PREFIX = "ubag-attach-"


def _validate_local_attachment_path(value: str, field: str) -> str:
    """Fail closed unless ``value`` resolves inside the system temp directory.

    ``attachment_local_paths`` / ``audio_local_path`` reach
    ``locator.set_input_files()``, which UPLOADS the file's bytes to a
    third-party provider. Only the declaration KEY is validated elsewhere - the
    path itself was accepted verbatim, so a job payload naming any readable file
    on the host (an SSH key, /etc/passwd, an env file, a mounted service-account
    token) would have its contents exfiltrated to the model vendor.

    Safety today depends entirely on the Go runner overwriting the field before
    the envelope is dispatched. This is defence in depth for that: accept only
    an absolute path that, AFTER symlink resolution, is the system temp
    directory or something beneath it. realpath() is what makes a symlink
    planted inside an allowed folder useless - it is resolved before the
    containment check, so the link's target is what gets tested.

    Anything else raises EnvelopeError, which fails the job rather than
    uploading the file.
    """
    import os
    import tempfile

    candidate = (value or "").strip()
    if not candidate:
        raise EnvelopeError("%s must not be empty" % field)
    if not os.path.isabs(candidate):
        raise EnvelopeError("%s must be an absolute path" % field)
    try:
        real = os.path.realpath(candidate)
        temp_root = os.path.realpath(tempfile.gettempdir())
    except OSError as exc:  # pragma: no cover - realpath rarely raises
        raise EnvelopeError("%s could not be resolved" % field) from exc
    if real != temp_root and not real.startswith(temp_root + os.sep):
        raise EnvelopeError(
            "%s must resolve inside the gateway's attachment directory (%s)"
            % (field, _MATERIALIZED_ATTACHMENT_DIR_PREFIX + "* under the system temp dir)")
        )
    return candidate


def _validated_local_path_tuple(value: Any, field: str) -> tuple:
    paths = _string_tuple(value)
    return tuple(_validate_local_attachment_path(p, field) for p in paths)


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

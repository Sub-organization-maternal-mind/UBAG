"""Deterministic mock adapter implementation.

The adapter deliberately avoids clocks, randomness, network calls, and external
packages so local worker tests can run the same way on Python 3.9 and 3.12.
"""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass
from datetime import datetime, timedelta
from typing import Any, Dict, Iterable, List, Mapping, Tuple


JsonObject = Dict[str, Any]
_BASE_CLOCK = datetime(2026, 1, 1, 0, 0, 0)
_DISALLOWED_SECRET_KEYS = {
    "access_token",
    "api_key",
    "apikey",
    "auth_token",
    "authorization",
    "bearer",
    "captcha_response",
    "captcha_solution",
    "captcha_token",
    "cookie",
    "cookies",
    "credential",
    "credentials",
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
    "set_cookie",
    "storage_state",
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


class MockAdapterError(ValueError):
    """Raised when the mock adapter receives an invalid job payload."""


@dataclass(frozen=True)
class _NormalizedJob:
    api_version: str
    job_id: str
    trace_id: str
    target: str
    command_type: str
    prompt: str
    tokens: List[str]
    result_text: str
    model_settings: Dict[str, Any]
    conversation_key: str
    thread_ref: str


class MockAdapter:
    """Small deterministic adapter used by the v0 worker implementation."""

    name = "mock"
    version = "0.1.0"

    def iter_events(self, payload: Mapping[str, Any]) -> Iterable[JsonObject]:
        job = _normalize_payload(payload)
        sequence = 1

        yield _event(
            job,
            sequence,
            "queued",
            "queued",
            {"message": "job accepted by mock worker"},
        )
        sequence += 1

        yield _event(
            job,
            sequence,
            "running",
            "running",
            {
                "adapter": self.name,
                "adapter_version": self.version,
                "command_type": job.command_type,
            },
        )
        sequence += 1

        for token_index, token in enumerate(job.tokens):
            yield _event(
                job,
                sequence,
                "token",
                "token_streaming",
                {
                    "token_index": token_index,
                    "delta": {"text": token},
                },
            )
            sequence += 1

        # A conversation key with no bound thread yet binds a fresh, deterministic
        # chat URL. A payload that already carries a thread_ref is a resume and
        # must NOT re-emit thread_bound. No conversation key ⇒ today's path exactly.
        if job.conversation_key and not job.thread_ref:
            # Flat top-level thread_ref: the gateway WorkerConsumer reads
            # data.thread_ref non-recursively (stringFromEventData), and the live
            # engine emits the same flat shape. Nesting it would make the bind
            # persist an empty ref and silently break resume for the mock target.
            yield _event(
                job,
                sequence,
                "conversation.thread_bound",
                "thread_bound",
                {"thread_ref": _conversation_thread_url(job.conversation_key)},
            )
            sequence += 1

        metadata: JsonObject = {
            "adapter": self.name,
            "adapter_version": self.version,
            "token_count": len(job.tokens),
        }
        if job.model_settings:
            metadata["model_settings"] = dict(job.model_settings)

        yield _event(
            job,
            sequence,
            "completed",
            "completed",
            {
                "result": {
                    "type": "text",
                    "text": job.result_text,
                },
                "metadata": metadata,
            },
        )

    def run(self, payload: Mapping[str, Any]) -> List[JsonObject]:
        return list(self.iter_events(payload))


def build_mock_events(payload: Mapping[str, Any]) -> List[JsonObject]:
    """Return the deterministic mock event stream for a job payload."""

    return MockAdapter().run(payload)


def _normalize_payload(payload: Mapping[str, Any]) -> _NormalizedJob:
    if not isinstance(payload, Mapping):
        raise MockAdapterError("job payload must be a JSON object")
    if _contains_disallowed_secret_material(payload):
        raise MockAdapterError(
            "mock adapter payload must not include credentials, cookies, tokens, or secrets"
        )

    job_payload = payload.get("job", {})
    if job_payload is None:
        job_payload = {}
    if not isinstance(job_payload, Mapping):
        raise MockAdapterError("payload.job must be a JSON object when present")

    input_payload = _mapping_or_empty(job_payload.get("input", payload.get("input", {})))
    options = _mapping_or_empty(job_payload.get("options", payload.get("options", {})))

    api_version = _string_or_default(payload.get("api_version"), "v0")
    target = _string_or_default(job_payload.get("target", payload.get("target")), "mock")
    command_type = _string_or_default(
        job_payload.get("command_type", job_payload.get("type")),
        "mock.complete",
    )
    prompt = _extract_prompt(input_payload, payload)
    job_id = _derive_job_id(payload, job_payload)
    trace_id = _string_or_default(payload.get("trace_id"), "trace_" + _digest(job_id)[:16])
    tokens, result_text = _resolve_tokens(job_id, target, command_type, prompt, options)
    model_settings = _extract_model_settings(options)
    conversation_key, thread_ref = _extract_conversation(payload, job_payload)

    return _NormalizedJob(
        api_version=api_version,
        job_id=job_id,
        trace_id=trace_id,
        target=target,
        command_type=command_type,
        prompt=prompt,
        tokens=tokens,
        result_text=result_text,
        model_settings=model_settings,
        conversation_key=conversation_key,
        thread_ref=thread_ref,
    )


def _extract_model_settings(options: Mapping[str, Any]) -> Dict[str, Any]:
    """Read the validated model settings the gateway injects.

    The gateway flattens ``job.model_settings`` into the worker envelope as
    ``options.provider_config`` — a flat map keyed by the adapter's own setting
    keys (e.g. ``model``/``thinking``/``mode``/``deepthink``). Reserved
    ``_``-prefixed worker control keys (``_enabled``/``_new_chat``) never belong
    in the echo, so they are dropped here as defense in depth.
    """

    provider_config = _mapping_or_empty(options.get("provider_config", {}))
    return {
        str(key): value
        for key, value in provider_config.items()
        if not str(key).startswith("_")
    }


def _extract_conversation(
    payload: Mapping[str, Any], job_payload: Mapping[str, Any]
) -> Tuple[str, str]:
    """Resolve the conversation key and any already-bound thread ref.

    The gateway injects a ``conversation: {key, thread_ref, on_missing}`` block
    into the envelope; ``job.conversation_id`` is the public fallback for the
    key. ``thread_ref`` is a provider chat URL only — never credentials.
    """

    conversation_key = ""
    thread_ref = ""
    conversation = payload.get("conversation")
    if isinstance(conversation, Mapping):
        conversation_key = _string_or_default(conversation.get("key"), "")
        thread_ref = _string_or_default(conversation.get("thread_ref"), "")
    if not conversation_key:
        conversation_key = _string_or_default(
            job_payload.get("conversation_id", payload.get("conversation_id")), ""
        )
    return conversation_key, thread_ref


def _conversation_thread_url(conversation_key: str) -> str:
    """Derive a deterministic mock chat URL from a conversation key (sha256)."""

    return "https://mock.local/chat/" + _digest("conversation:" + conversation_key)[:16]


def _event(
    job: _NormalizedJob,
    sequence: int,
    event_type: str,
    status: str,
    body: Mapping[str, Any],
) -> JsonObject:
    data: JsonObject = {
        "target": job.target,
        "status": status,
    }
    data.update(body)

    return {
        "api_version": job.api_version,
        "event_id": "evt_" + _digest("%s:%s" % (job.job_id, sequence))[:16],
        "job_id": job.job_id,
        "trace_id": job.trace_id,
        "type": event_type,
        "sequence": sequence,
        "created_at": _timestamp(sequence - 1),
        "data": data,
    }


def _resolve_tokens(
    job_id: str,
    target: str,
    command_type: str,
    prompt: str,
    options: Mapping[str, Any],
) -> Tuple[List[str], str]:
    configured_tokens = options.get("mock_tokens", options.get("stream_tokens"))
    configured_result = options.get("mock_result", options.get("result_text"))

    if configured_tokens is not None:
        if not isinstance(configured_tokens, list):
            raise MockAdapterError("mock_tokens must be a JSON array of strings")
        tokens = []
        for token in configured_tokens:
            if not isinstance(token, str):
                raise MockAdapterError("mock_tokens must contain only strings")
            tokens.append(token)
        if not tokens:
            tokens = [""]
        result_text = configured_result if isinstance(configured_result, str) else "".join(tokens)
        return tokens, result_text

    if isinstance(configured_result, str):
        result_text = configured_result
    else:
        result_text = "Mock response for %s on %s (%s): %s" % (
            command_type,
            target,
            job_id,
            prompt,
        )

    tokens = _split_for_stream(result_text)
    if not tokens:
        tokens = [""]
    return tokens, "".join(tokens)


def _derive_job_id(payload: Mapping[str, Any], job_payload: Mapping[str, Any]) -> str:
    explicit_id = payload.get("job_id", job_payload.get("job_id", job_payload.get("id")))
    if explicit_id is not None:
        return str(explicit_id)

    idempotency_key = payload.get("idempotency_key")
    seed = str(idempotency_key) if idempotency_key is not None else _canonical_json(payload)
    return "job_" + _digest(seed)[:16]


def _extract_prompt(input_payload: Mapping[str, Any], payload: Mapping[str, Any]) -> str:
    for key in ("prompt", "text", "message", "content"):
        value = input_payload.get(key)
        if isinstance(value, str) and value:
            return value

    top_level_prompt = payload.get("prompt")
    if isinstance(top_level_prompt, str) and top_level_prompt:
        return top_level_prompt

    if input_payload:
        return _canonical_json(input_payload)

    return "empty mock prompt"


def _mapping_or_empty(value: Any) -> Mapping[str, Any]:
    if value is None:
        return {}
    if isinstance(value, Mapping):
        return value
    return {"value": value}


def _contains_disallowed_secret_material(value: Any) -> bool:
    if isinstance(value, Mapping):
        for key, child in value.items():
            normalized_key = _normalize_secret_key(str(key))
            if _is_disallowed_secret_key(normalized_key):
                return True
            if _contains_disallowed_secret_material(child):
                return True
    elif isinstance(value, list):
        for child in value:
            if _contains_disallowed_secret_material(child):
                return True
    elif isinstance(value, str):
        if _BEARER_VALUE_PATTERN.search(value) or _PRIVATE_KEY_VALUE_PATTERN.search(value) or _CAPTCHA_SOLVER_PATTERN.search(value):
            return True
    return False


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


def _string_or_default(value: Any, default: str) -> str:
    if value is None:
        return default
    text = str(value)
    return text if text else default


def _split_for_stream(text: str) -> List[str]:
    if not text:
        return []
    parts = text.split(" ")
    tokens = []
    for index, part in enumerate(parts):
        suffix = " " if index < len(parts) - 1 else ""
        tokens.append(part + suffix)
    return tokens


def _timestamp(sequence: int) -> str:
    return (_BASE_CLOCK + timedelta(milliseconds=250 * sequence)).isoformat(
        timespec="milliseconds"
    ) + "Z"


def _canonical_json(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True)


def _digest(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()

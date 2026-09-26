"""Antigravity SDK adapter with full agent fidelity."""

from __future__ import annotations

import hashlib
import json
import os
import re
from dataclasses import dataclass
from datetime import datetime
from typing import Any, Dict, Iterable, List, Mapping, Optional

from .credential_provider import CredentialProvider
from .events import (
    AgentEventContext,
    ToolCall,
    UsageMetrics,
    make_event,
    make_stop_reason_event,
    make_structured_output_event,
    make_text_delta_event,
    make_tool_call_event,
    make_usage_event,
)
from .sdk_client import AntigravitySDKClient, AntigravitySDKError, SDKConfig, SDKResponse

JsonObject = Dict[str, Any]
_BASE_CLOCK = datetime(2026, 1, 1, 0, 0, 0)

_DISALLOWED_SECRET_KEYS = {
    "access_token", "api_key", "apikey", "auth_token", "authorization",
    "bearer", "captcha_response", "captcha_solution", "captcha_token",
    "cookie", "cookies", "credential", "credentials", "id_token",
    "mfa_code", "novnc_url", "password", "private_key", "refresh_token",
    "secret", "session", "session_cookie", "session_state", "set_cookie",
    "storage_state", "totp", "x_api_key",
}
_DISALLOWED_SECRET_SEGMENTS = {
    "authorization", "bearer", "captcha", "cookie", "cookies",
    "credential", "credentials", "mfa", "password", "secret", "token", "totp",
}
_DISALLOWED_COMPACT_SECRET_MARKERS = (
    "apikey", "privatekey", "storagestate", "sessionstate",
    "sessiontoken", "accesskey",
)

_BEARER_VALUE_PATTERN = re.compile(r"\bbearer\s+[A-Za-z0-9._~+/=-]{12,}", re.IGNORECASE)
_PRIVATE_KEY_VALUE_PATTERN = re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----", re.IGNORECASE | re.DOTALL)
_CAPTCHA_SOLVER_PATTERN = re.compile(
    r"\b(solve|bypass|delegate|outsource)\b.{0,40}\bcaptcha\b|\bcaptcha\b.{0,40}\b(solver|solving|bypass)\b",
    re.IGNORECASE,
)


class AntigravityAdapterError(ValueError):
    pass


@dataclass(frozen=True)
class _NormalizedJob:
    api_version: str
    job_id: str
    trace_id: str
    target: str
    command_type: str
    prompt: str
    model_settings: Dict[str, Any]
    conversation_key: str
    thread_ref: str


class AntigravitySDKAdapter:
    name = "antigravity_sdk"
    version = "0.2.0"

    def __init__(self, credential_provider: Optional[CredentialProvider] = None) -> None:
        self._credentials = credential_provider or CredentialProvider.from_env()

    def iter_events(self, payload: Mapping[str, Any]) -> Iterable[JsonObject]:
        job = _normalize_payload(payload)
        ctx = AgentEventContext(
            request_id=job.job_id,
            provider="antigravity_sdk",
            model=str(job.model_settings.get("model", "gemini-3.8-flash")),
            conversation_id=job.conversation_key,
            trace_id=job.trace_id,
            api_version=job.api_version,
        )

        yield make_event(ctx, 1, "queued", {"status": "queued", "message": "job accepted"})
        yield make_event(ctx, 2, "started", {"status": "running", "adapter": self.name})

        try:
            config = self._build_sdk_config(job)
            response = self._execute(job, config)
        except AntigravitySDKError as exc:
            yield make_event(ctx, 3, "failed", {
                "status": "failed_retryable" if exc.retryable else "failed_terminal",
                "error": str(exc), "error_code": exc.error_code, "retryable": exc.retryable,
            })
            return
        except Exception as exc:
            yield make_event(ctx, 3, "failed", {
                "status": "failed_terminal", "error": str(exc),
                "error_code": "UNEXPECTED_ERROR", "retryable": False,
            })
            return

        sequence = 3
        for token_index, token in enumerate(_split_for_stream(response.text)):
            yield make_text_delta_event(ctx, sequence, token, conversation_id=response.conversation_id)
            sequence += 1

        for tc in response.tool_calls:
            tool_call = ToolCall(
                tool_call_id=tc.get("tool_call_id", ""),
                tool_name=tc.get("tool_name", ""),
                arguments=tc.get("arguments", ""),
                status="completed",
            )
            yield make_tool_call_event(ctx, sequence, tool_call, conversation_id=response.conversation_id)
            sequence += 1

        usage = UsageMetrics(
            input_tokens=response.token_usage.get("input_tokens", 0),
            output_tokens=response.token_usage.get("output_tokens", 0),
            thinking_tokens=response.token_usage.get("thinking_tokens", 0),
            cached_tokens=response.token_usage.get("cached_tokens", 0),
            total_tokens=response.token_usage.get("total_tokens", 0),
        )
        yield make_usage_event(ctx, sequence, usage, conversation_id=response.conversation_id)
        sequence += 1

        if response.structured_output is not None:
            yield make_structured_output_event(ctx, sequence, response.structured_output)
            sequence += 1

        if response.stop_reason:
            yield make_stop_reason_event(ctx, sequence, response.stop_reason)
            sequence += 1

        metadata: JsonObject = {
            "adapter": self.name, "adapter_version": self.version,
            "model": response.model, "effort": response.effort,
            "conversation_id": response.conversation_id,
            "token_usage": response.token_usage,
            "credential_masked": self._credentials.masked_key,
        }
        if job.model_settings:
            metadata["model_settings"] = dict(job.model_settings)

        yield make_event(ctx, sequence, "completed", {
            "status": "completed",
            "result": {"type": "text", "text": response.text},
            "metadata": metadata,
        })

    def run(self, payload: Mapping[str, Any]) -> List[JsonObject]:
        return list(self.iter_events(payload))

    def _build_sdk_config(self, job: _NormalizedJob) -> SDKConfig:
        if not self._credentials.is_configured:
            raise AntigravitySDKError(
                "GEMINI_API_KEY not configured", retryable=False, error_code="NO_CREDENTIALS"
            )
        cred = self._credentials.get_config()
        model = str(job.model_settings.get("model", os.environ.get("UBAG_ANTIGRAVITY_MODEL", "gemini-3.8-flash")))
        effort = str(job.model_settings.get("effort", os.environ.get("UBAG_ANTIGRAVITY_EFFORT", "high")))
        return SDKConfig(
            api_key=cred.api_key, model=model, effort=effort,
            vertex=cred.vertex, project=cred.project, location=cred.location,
            max_tokens=int(os.environ.get("UBAG_ANTIGRAVITY_MAX_TOKENS", "65536")),
            max_model_calls=int(os.environ.get("UBAG_ANTIGRAVITY_MAX_MODEL_CALLS", "20")),
            max_tool_calls=int(os.environ.get("UBAG_ANTIGRAVITY_MAX_TOOL_CALLS", "50")),
            compaction_enabled=os.environ.get("UBAG_ANTIGRAVITY_COMPACTION", "true").lower() not in ("0", "false", "no"),
            tool_output_max_chars=int(os.environ.get("UBAG_ANTIGRAVITY_TOOL_OUTPUT_MAX_CHARS", "10000")),
            retry_max_attempts=int(os.environ.get("UBAG_ANTIGRAVITY_RETRY_MAX", "3")),
        )

    def _execute(self, job: _NormalizedJob, config: SDKConfig) -> SDKResponse:
        return AntigravitySDKClient.run_sync(config, job.prompt)


def build_antigravity_events(payload: Mapping[str, Any]) -> List[JsonObject]:
    return AntigravitySDKAdapter().run(payload)


def _normalize_payload(payload: Mapping[str, Any]) -> _NormalizedJob:
    if not isinstance(payload, Mapping):
        raise AntigravityAdapterError("job payload must be a JSON object")
    if _contains_disallowed_secret_material(payload):
        raise AntigravityAdapterError("payload must not include credentials or secrets")
    job_payload = payload.get("job", {})
    if job_payload is None:
        job_payload = {}
    if not isinstance(job_payload, Mapping):
        raise AntigravityAdapterError("payload.job must be a JSON object")
    input_payload = _mapping_or_empty(job_payload.get("input", payload.get("input", {})))
    options = _mapping_or_empty(job_payload.get("options", payload.get("options", {})))
    return _NormalizedJob(
        api_version=_string_or_default(payload.get("api_version"), "2026-05-22"),
        job_id=_derive_job_id(payload, job_payload),
        trace_id=_string_or_default(payload.get("trace_id"), "trace_" + _digest(_derive_job_id(payload, job_payload))[:16]),
        target=_string_or_default(job_payload.get("target", payload.get("target")), "antigravity_sdk"),
        command_type=_string_or_default(job_payload.get("command_type", job_payload.get("type")), "chat.prompt"),
        prompt=_extract_prompt(input_payload, payload),
        model_settings=_extract_model_settings(options),
        conversation_key=_extract_conversation(payload, job_payload),
        thread_ref="",
    )


def _extract_model_settings(options: Mapping[str, Any]) -> Dict[str, Any]:
    provider_config = _mapping_or_empty(options.get("provider_config", {}))
    return {str(k): v for k, v in provider_config.items() if not str(k).startswith("_")}


def _extract_conversation(payload: Mapping[str, Any], job_payload: Mapping[str, Any]) -> str:
    conversation = payload.get("conversation")
    if isinstance(conversation, Mapping):
        key = conversation.get("key")
        if isinstance(key, str) and key:
            return key
    cid = job_payload.get("conversation_id", payload.get("conversation_id"))
    return str(cid) if cid else ""


def _extract_prompt(input_payload: Mapping[str, Any], payload: Mapping[str, Any]) -> str:
    for key in ("prompt", "text", "message", "content"):
        value = input_payload.get(key)
        if isinstance(value, str) and value:
            return value
    top_level = payload.get("prompt")
    if isinstance(top_level, str) and top_level:
        return top_level
    return _canonical_json(input_payload) if input_payload else "empty prompt"


def _mapping_or_empty(value: Any) -> Mapping[str, Any]:
    return value if isinstance(value, Mapping) else {}


def _contains_disallowed_secret_material(value: Any) -> bool:
    if isinstance(value, Mapping):
        for key, child in value.items():
            if _is_disallowed_secret_key(_normalize_secret_key(str(key))):
                return True
            if _contains_disallowed_secret_material(child):
                return True
    elif isinstance(value, list):
        for child in value:
            if _contains_disallowed_secret_material(child):
                return True
    elif isinstance(value, str):
        if (_BEARER_VALUE_PATTERN.search(value) or _PRIVATE_KEY_VALUE_PATTERN.search(value)
                or _CAPTCHA_SOLVER_PATTERN.search(value)):
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
    if normalized_key in _DISALLOWED_SECRET_KEYS:
        return True
    if any(seg in _DISALLOWED_SECRET_SEGMENTS for seg in normalized_key.split("_")):
        return True
    compact = normalized_key.replace("_", "")
    return any(marker in compact for marker in _DISALLOWED_COMPACT_SECRET_MARKERS)


def _string_or_default(value: Any, default: str) -> str:
    if value is None:
        return default
    text = str(value)
    return text if text else default


def _split_for_stream(text: str) -> List[str]:
    if not text:
        return []
    parts = text.split(" ")
    return [part + (" " if i < len(parts) - 1 else "") for i, part in enumerate(parts)]


def _derive_job_id(payload: Mapping[str, Any], job_payload: Mapping[str, Any]) -> str:
    explicit = payload.get("job_id", job_payload.get("job_id", job_payload.get("id")))
    if explicit is not None:
        return str(explicit)
    idem = payload.get("idempotency_key")
    seed = str(idem) if idem is not None else _canonical_json(payload)
    return "job_" + _digest(seed)[:16]


def _canonical_json(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True)


def _digest(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()

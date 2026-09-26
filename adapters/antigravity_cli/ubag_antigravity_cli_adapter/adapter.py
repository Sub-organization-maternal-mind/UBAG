"""Antigravity CLI adapter using persistent agy stream-json process.

Uses Google account OAuth subscription quota (not API key).
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import os
import re
from dataclasses import dataclass
from datetime import datetime, timedelta
from typing import Any, Dict, Iterable, List, Mapping, Optional

from .process import AgyProcessManager, AgyQuotaError, AgySocketClient, CLIConfig, CLITurnResult

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


class AntigravityCLIAdapterError(ValueError):
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


class AntigravityCLIAdapter:
    name = "antigravity_cli"
    version = "0.1.0"

    def __init__(self, process_manager: Optional[AgyProcessManager] = None) -> None:
        self._pm = process_manager

    def iter_events(self, payload: Mapping[str, Any]) -> Iterable[JsonObject]:
        job = _normalize_payload(payload)
        sequence = 1

        yield _event(job, sequence, "queued", "queued", {"message": "job accepted by antigravity_cli worker"})
        sequence += 1
        yield _event(job, sequence, "started", "running", {"adapter": self.name, "adapter_version": self.version})
        sequence += 1

        try:
            result = self._execute(job)
        except AgyQuotaError:
            yield _event(job, sequence, "failed", "failed", {
                "error": "OAuth account quota exhausted", "error_code": "OAUTH_QUOTA_EXHAUSTED",
                "retryable": False, "adapter": self.name,
            })
            return
        except Exception as exc:
            message = str(exc) if isinstance(exc, AntigravityCLIAdapterError) else "isolated CLI turn failed"
            yield _event(job, sequence, "failed", "failed", {
                "error": message, "error_code": "CLI_ERROR", "retryable": False, "adapter": self.name,
            })
            return

        full_text = result.text
        for token_index, token in enumerate(_split_for_stream(full_text)):
            yield _event(job, sequence, "token", "token_streaming", {
                "token_index": token_index, "delta": {"text": token},
            })
            sequence += 1

        for tc in result.tool_calls:
            yield _event(job, sequence, "tool_call", "tool_call", {
                "tool_call_id": tc.get("tool_call_id", ""),
                "tool_name": tc.get("tool_name", ""),
                "arguments": tc.get("arguments", ""),
                "status": "completed",
            })
            sequence += 1

        yield _event(job, sequence, "usage_update", "usage", {
            "usage": result.token_usage,
        })
        sequence += 1

        if result.conversation_id:
            yield _event(job, sequence, "conversation.thread_bound", "thread_bound", {
                "thread_ref": result.conversation_id,
            })
            sequence += 1

        metadata: JsonObject = {
            "adapter": self.name, "adapter_version": self.version,
            "conversation_id": result.conversation_id,
            "token_usage": result.token_usage,
            "auth_mode": "google_oauth",
        }

        yield _event(job, sequence, "completed", "completed", {
            "result": {"type": "text", "text": full_text},
            "metadata": metadata,
        })

    def run(self, payload: Mapping[str, Any]) -> List[JsonObject]:
        return list(self.iter_events(payload))

    def _execute(self, job: _NormalizedJob) -> CLITurnResult:
        if self._pm is None and not os.environ.get("UBAG_ANTIGRAVITY_ACCOUNT_SOCKET"):
            raise AntigravityCLIAdapterError("isolated account socket is required")
        config = CLIConfig(
            agy_binary=os.environ.get("AGY_BINARY", "agy"),
            model=str(job.model_settings.get("model", os.environ.get("UBAG_ANTIGRAVITY_MODEL", "gemini-3.8-flash"))),
            effort=str(job.model_settings.get("effort", os.environ.get("UBAG_ANTIGRAVITY_EFFORT", "high"))),
        )
        pm = self._pm or AgySocketClient(os.environ["UBAG_ANTIGRAVITY_ACCOUNT_SOCKET"], config)

        async def _run() -> CLITurnResult:
            try:
                text_parts: List[str] = []
                tool_calls: List[Dict[str, Any]] = []
                token_usage: Dict[str, int] = {}
                stop_reason = ""
                conversation_id = ""
                async for event in pm.send_prompt(job.prompt, conversation_id=job.conversation_key or None):
                    evt_type = event.get("event", "")
                    if evt_type == "step_update":
                        delta = event.get("delta", {})
                        if isinstance(delta, dict) and "text" in delta:
                            text_parts.append(str(delta["text"]))
                    elif evt_type == "result":
                        if "text" in event:
                            text_parts = [str(event["text"])]
                        stop_reason = str(event.get("stop_reason", ""))
                        usage = event.get("usage", {})
                        if isinstance(usage, dict):
                            token_usage = {k: int(v) for k, v in usage.items() if isinstance(v, (int, float))}
                        conversation_id = str(event.get("conversation_id", ""))
                        for tc in event.get("tool_calls", []):
                            if isinstance(tc, dict):
                                tool_calls.append(tc)
                return CLITurnResult(
                    text="".join(text_parts),
                    stop_reason=stop_reason,
                    conversation_id=conversation_id,
                    token_usage=token_usage,
                    tool_calls=tool_calls,
                )
            finally:
                if self._pm is None:
                    await pm.stop()

        return asyncio.run(_run())


def build_antigravity_cli_events(payload: Mapping[str, Any]) -> List[JsonObject]:
    return AntigravityCLIAdapter().run(payload)


def _normalize_payload(payload: Mapping[str, Any]) -> _NormalizedJob:
    if not isinstance(payload, Mapping):
        raise AntigravityCLIAdapterError("job payload must be a JSON object")
    if _contains_disallowed_secret_material(payload):
        raise AntigravityCLIAdapterError("payload must not include credentials or secrets")
    job_payload = payload.get("job", {})
    if job_payload is None:
        job_payload = {}
    if not isinstance(job_payload, Mapping):
        raise AntigravityCLIAdapterError("payload.job must be a JSON object")
    input_payload = _mapping_or_empty(job_payload.get("input", payload.get("input", {})))
    options = _mapping_or_empty(job_payload.get("options", payload.get("options", {})))
    job_id = _derive_job_id(payload, job_payload)
    return _NormalizedJob(
        api_version=_string_or_default(payload.get("api_version"), "2026-05-22"),
        job_id=job_id,
        trace_id=_string_or_default(payload.get("trace_id"), "trace_" + _digest(job_id)[:16]),
        target=_string_or_default(job_payload.get("target", payload.get("target")), "antigravity_cli"),
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


def _event(job: _NormalizedJob, sequence: int, event_type: str, status: str, body: Mapping[str, Any]) -> JsonObject:
    data: JsonObject = {"target": job.target, "status": status, "provider": "antigravity_cli"}
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


def _timestamp(sequence: int) -> str:
    return (_BASE_CLOCK + timedelta(milliseconds=250 * sequence)).isoformat(timespec="milliseconds") + "Z"

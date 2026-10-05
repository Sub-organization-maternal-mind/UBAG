"""Voice jobs — run ``voice.activate`` / ``voice.deactivate`` dispatch envelopes.

The gateway dispatches these internal jobs (``job.command_type`` starts with
``voice.``, ``job.target`` is the provider id) with an input of
``{"provider_id", "cdp_endpoint", "action", "context"?}``. They drive
:mod:`ubag_worker.voice.voice_runner` against the browser container's CDP
proxy and report through the same worker-event envelope as every other path.
They NEVER reach the chat engine or the adapter registry: both would type the
input JSON into the provider.

Event sequence: one ``running`` event, then exactly one terminal event
(``completed``, ``blocked`` for a login wall — the live engine's own
representation — or ``failed``). Rejected requests emit the terminal event
alone, before any browser contact.

The CDP endpoint arrives inside a job payload, so it is a trust boundary: its
host must be allowlisted (``UBAG_VOICE_CDP_ALLOWED_HOSTS``, comma list, plus
the host of ``UBAG_REMOTE_BROWSER_ENDPOINT``). With neither set every voice job
is rejected (fail closed), so a forged job cannot aim the worker's browser
attach at an arbitrary host.
"""

from __future__ import annotations

import os
from typing import Any, Callable, Dict, Iterator, Mapping, Optional
from urllib.parse import urlsplit

from ..live.envelope import (
    DEFAULT_API_VERSION,
    _derive_job_id,
    _string_or_default,
)
from ..live.events import canonical_json, digest, worker_event
from .voice_runner import (
    ACTIVATED,
    ATTACH_FAILED,
    DEACTIVATED,
    LOGIN_WALL,
    VOICE_PROVIDERS,
    ContextSelectionError,
    PlaywrightCdpClient,
    VoiceActivationResult,
    activate_voice,
    deactivate_voice,
)

JsonObject = Dict[str, Any]

COMMAND_PREFIX = "voice."
ALLOWED_HOSTS_ENV = "UBAG_VOICE_CDP_ALLOWED_HOSTS"
REMOTE_ENDPOINT_ENV = "UBAG_REMOTE_BROWSER_ENDPOINT"

# Worker-side states that are not VoiceActivationResult states.
INVALID_REQUEST = "invalid_request"
INTERNAL_ERROR = "internal_error"
UNSUPPORTED_RUNNER = "unsupported_runner"

# The gateway caps a worker event's data; stay well under it.
_MAX_DATA_BYTES = 48 * 1024
_MAX_MESSAGE_CHARS = 200


def _job_of(payload: Any) -> Mapping[str, Any]:
    job = payload.get("job") if isinstance(payload, Mapping) else None
    return job if isinstance(job, Mapping) else {}


def is_voice_command(payload: Any) -> bool:
    command_type = _job_of(payload).get("command_type")
    return isinstance(command_type, str) and command_type.startswith(COMMAND_PREFIX)


def _allowed_hosts() -> frozenset:
    hosts = {h.strip().lower() for h in os.environ.get(ALLOWED_HOSTS_ENV, "").split(",")}
    remote = os.environ.get(REMOTE_ENDPOINT_ENV, "").strip()
    if remote:
        try:
            hosts.add((urlsplit(remote).hostname or "").lower())
        except ValueError:
            pass
    hosts.discard("")
    return frozenset(hosts)


def _validate(job: Mapping[str, Any], input_payload: Mapping[str, Any]) -> tuple:
    """Return ``(action, provider_id, endpoint, context_id, error)``; error is None when valid."""
    command_type = str(job.get("command_type") or "")
    action = input_payload.get("action")
    if action not in ("activate", "deactivate") or command_type != COMMAND_PREFIX + action:
        return None, None, None, None, "action must be activate|deactivate and match command_type"
    provider_id = input_payload.get("provider_id")
    if not isinstance(provider_id, str) or provider_id != job.get("target"):
        return None, None, None, None, "provider_id must equal job.target"
    if provider_id not in VOICE_PROVIDERS:
        return None, None, None, None, "provider does not support voice"
    context = input_payload.get("context")
    if context is not None and (
        isinstance(context, bool)
        or not (isinstance(context, int) or (isinstance(context, str) and context.isdigit()))
        or int(context) < 0
    ):
        return None, None, None, None, "context must be a zero-based index"
    endpoint = input_payload.get("cdp_endpoint")
    try:
        parts = urlsplit(endpoint) if isinstance(endpoint, str) else None
        host = (parts.hostname or "").lower() if parts else ""
        if parts:
            parts.port  # noqa: B018 - raises ValueError on a malformed port
    except ValueError:
        parts, host = None, ""
    if parts is None or parts.scheme not in ("http", "https") or not host:
        return None, None, None, None, "cdp_endpoint must be an http(s) URL with a host"
    if parts.username is not None or parts.password is not None:
        return None, None, None, None, "cdp_endpoint must not carry credentials"
    if host not in _allowed_hosts():
        return None, None, None, None, "cdp_endpoint host is not allowlisted"
    return action, provider_id, endpoint, None if context is None else str(context), None


def _describe(exc: BaseException) -> str:
    # Exception text can echo URLs or page content; only the context-choice
    # message (our own wording) is passed through.
    if isinstance(exc, ContextSelectionError):
        return str(exc)[:_MAX_MESSAGE_CHARS]
    return type(exc).__name__


def _bounded(result: JsonObject) -> JsonObject:
    if len(canonical_json(result)) > _MAX_DATA_BYTES:
        result = dict(result, details={"truncated": True})
    return result


def _terminal(provider_id: str, action: str, result: VoiceActivationResult) -> tuple:
    """Map a runner result to ``(event_type, data)``."""
    body = _bounded(result.as_dict())
    data: JsonObject = {
        "target": provider_id,
        "adapter": provider_id,
        "action": action,
        "state": result.state,
        "reason": result.reason,
        "result": body,
    }
    if result.state in (ACTIVATED, DEACTIVATED):
        return "completed", dict(data, status="completed")
    if result.state == LOGIN_WALL:
        # Same type the live engine uses for manual_login_required.
        return "blocked", dict(data, status="blocked", retryable=False)
    return "failed", dict(data, status="failed", retryable=False)


def _failure_result(provider_id: str, state: str, reason: str) -> VoiceActivationResult:
    return VoiceActivationResult(
        provider=provider_id, activated=False, state=state, reason=reason,
    )


def _events(
    payload: Any,
    plan: Callable[[Mapping[str, Any], Mapping[str, Any]], Iterator[tuple]],
) -> Iterator[JsonObject]:
    """Stamp ``(event_type, data)`` pairs with the envelope's identity."""
    payload = payload if isinstance(payload, Mapping) else {}
    job = _job_of(payload)
    job_id = _derive_job_id(payload, job)
    trace_id = _string_or_default(payload.get("trace_id"), "trace_" + digest(job_id)[:16])
    api_version = _string_or_default(payload.get("api_version"), DEFAULT_API_VERSION)
    identity = {
        "tenant_id": _string_or_default(payload.get("tenant_id"), "default"),
        "app_id": _string_or_default(payload.get("app_id"), ""),
    }
    input_payload = job.get("input")
    input_payload = input_payload if isinstance(input_payload, Mapping) else {}

    sequence = 0
    for event_type, data in plan(job, input_payload):
        sequence += 1
        yield worker_event(
            api_version=api_version, job_id=job_id, trace_id=trace_id,
            sequence=sequence, event_type=event_type, data=dict(data, **identity),
        )


def iter_voice_events(
    payload: Any, client_factory: Optional[Callable[..., Any]] = None
) -> Iterator[JsonObject]:
    """Yield worker events for one voice job; never raises, one terminal event."""
    # Resolved at call time so tests (and callers) can substitute the client.
    factory = client_factory or PlaywrightCdpClient

    def plan(job: Mapping[str, Any], input_payload: Mapping[str, Any]) -> Iterator[tuple]:
        action, provider_id, endpoint, context_id, error = _validate(job, input_payload)
        if error is not None:
            target = str(job.get("target") or "")
            yield _terminal(target, str(input_payload.get("action") or ""),
                            _failure_result(target, INVALID_REQUEST, error))
            return
        yield "running", {"status": "running", "target": provider_id, "adapter": provider_id,
                          "action": action, "message": "voice %s started" % action}
        client = None
        try:
            try:
                client = factory(endpoint, context_id)
            except Exception as exc:  # connection / context-choice failure (CLI parity)
                result = _failure_result(
                    provider_id, ATTACH_FAILED, "cdp_attach_failed: " + _describe(exc))
            else:
                run = deactivate_voice if action == "deactivate" else activate_voice
                result = run(client, provider_id)
        except Exception as exc:  # noqa: BLE001 - the stream must always terminate
            result = _failure_result(
                provider_id, INTERNAL_ERROR, "voice_job_failed: " + type(exc).__name__)
        finally:
            if client is not None:
                try:
                    client.close()
                except Exception:  # noqa: BLE001 - teardown must not mask the outcome
                    pass
        yield _terminal(provider_id, action, result)

    return _events(payload, plan)


def refuse_voice_job(payload: Any, state: str, message: str) -> Iterator[JsonObject]:
    """Single terminal ``failed`` event for a voice job a runner cannot execute."""
    target = str(_job_of(payload).get("target") or "")
    result = _failure_result(target, state, message)

    def plan(job: Mapping[str, Any], input_payload: Mapping[str, Any]) -> Iterator[tuple]:
        yield _terminal(target, str(input_payload.get("action") or ""), result)

    return _events(payload, plan)


__all__ = ["is_voice_command", "iter_voice_events", "refuse_voice_job"]

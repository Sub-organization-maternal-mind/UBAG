"""Provider-agnostic live manual-session orchestration engine.

The engine turns a job payload + :class:`ProviderSelectors` + a
:class:`PageDriver` into the canonical JSONL worker event stream:

    queued
    session.opening
    [session.manual_action_required -> (session.authenticated | blocked)]
    session.authenticated
    running
    token* (streamed deltas)
    completed | blocked

It enforces every safe-mode invariant: manual login only, user-owned persistent
profiles only, no credential/cookie/token ingestion, and no CAPTCHA solving.
Selector drift surfaces as ``UBAG-ADAPTER-DRIFT-014`` blocked events.
"""

from __future__ import annotations

import os
import re
from typing import Any, Iterator, List, Mapping, Optional

from .events import JsonObject, canonical_json, digest, worker_event
from .page_driver import (
    AUTHENTICATED,
    DriftDetectedError,
    PageDriver,
    create_default_driver,
)
from .selectors import ProviderSelectors

# Reuse the canonical secret-material guard so the live path rejects exactly the
# same disallowed credential/cookie/token material as the registry & mock paths.
from ..adapter_registry import _contains_disallowed_secret_material  # noqa: E402

_DEFAULT_MANUAL_LOGIN_TIMEOUT_S = 300.0
_DEFAULT_RESPONSE_TIMEOUT_S = 120.0


class LiveSessionError(ValueError):
    """Raised when a live job payload is invalid (e.g. carries secrets)."""


class LiveSessionEngine:
    """Drives a single live manual-session job for one provider."""

    def __init__(
        self,
        selectors: ProviderSelectors,
        *,
        manual_login_timeout_s: float = _DEFAULT_MANUAL_LOGIN_TIMEOUT_S,
        response_timeout_s: float = _DEFAULT_RESPONSE_TIMEOUT_S,
    ) -> None:
        self._selectors = selectors
        self._manual_login_timeout_s = manual_login_timeout_s
        self._response_timeout_s = response_timeout_s

    # -- public API ------------------------------------------------------
    def run(self, payload: Mapping[str, Any], *, driver: Optional[PageDriver] = None) -> List[JsonObject]:
        return list(self.iter_events(payload, driver=driver))

    def iter_events(
        self, payload: Mapping[str, Any], *, driver: Optional[PageDriver] = None
    ) -> Iterator[JsonObject]:
        if not isinstance(payload, Mapping):
            raise LiveSessionError("job payload must be a JSON object")
        if _contains_disallowed_secret_material(payload):
            raise LiveSessionError(
                "adapter %s payload must not include credentials, cookies, tokens, or "
                "secrets; rely on a user-owned manual browser session instead"
                % self._selectors.provider_id
            )

        job = _normalize_payload(payload, self._selectors.provider_id)
        owns_driver = driver is None
        if driver is None:
            driver = create_default_driver(job.options)

        sequence = 1

        def emit(event_type: str, data: Mapping[str, Any]) -> JsonObject:
            nonlocal sequence
            event = worker_event(
                api_version=job.api_version,
                job_id=job.job_id,
                trace_id=job.trace_id,
                sequence=sequence,
                event_type=event_type,
                data=data,
            )
            sequence += 1
            return event

        try:
            yield emit("queued", {
                "status": "queued",
                "target": job.target,
                "adapter": self._selectors.provider_id,
                "message": "job accepted by live manual-session worker",
            })

            driver.open(
                target_url=self._selectors.target_url,
                user_data_dir=job.user_data_dir,
                headless=job.headless,
            )
            yield emit("session.opening", {
                "status": "opening",
                "target": job.target,
                "adapter": self._selectors.provider_id,
                "target_url": self._selectors.target_url,
                "selector_version": self._selectors.selector_version,
                "profile": _profile_label(job.user_data_dir),
                "message": "opened user-owned persistent browser session",
            })

            login_state = driver.detect_login_state(self._selectors)
            if login_state != AUTHENTICATED:
                session_id = job.session_id
                yield emit("session.manual_action_required", {
                    "status": "manual_action_required",
                    "target": job.target,
                    "adapter": self._selectors.provider_id,
                    "session_id": session_id,
                    "novnc_url": _novnc_url(session_id),
                    "account_binding_id": job.account_binding_id,
                    "consent_ref": job.consent_ref,
                    "automation_scope": job.automation_scope,
                    "reason": "manual_login_required",
                    "message": (
                        "Open the live browser session and complete login, CAPTCHA, "
                        "2FA, or consent prompts manually. UBAG will not fill "
                        "credentials or solve challenges."
                    ),
                })

                login_state = driver.await_manual_login(
                    self._selectors, timeout_s=job.manual_login_timeout_s
                )
                if login_state != AUTHENTICATED:
                    yield emit("blocked", {
                        "status": "blocked",
                        "target": job.target,
                        "adapter": self._selectors.provider_id,
                        "session_id": session_id,
                        "reason": "manual_login_required",
                        "retryable": True,
                        "message": (
                            "Provider execution is paused until the user completes "
                            "login in the live browser session."
                        ),
                    })
                    return

            yield emit("session.authenticated", {
                "status": "authenticated",
                "target": job.target,
                "adapter": self._selectors.provider_id,
                "message": "user-owned session is authenticated",
            })

            yield emit("running", {
                "status": "running",
                "target": job.target,
                "adapter": self._selectors.provider_id,
                "command_type": job.command_type,
                "selector_version": self._selectors.selector_version,
            })

            driver.submit_prompt(self._selectors, job.prompt)

            token_index = 0
            for delta in driver.stream_response(
                self._selectors, timeout_s=job.response_timeout_s
            ):
                yield emit("token", {
                    "status": "token_streaming",
                    "target": job.target,
                    "token_index": token_index,
                    "delta": {"text": delta},
                })
                token_index += 1

            result_text = driver.read_final_response(self._selectors)
            dom_signature = driver.dom_signature(self._selectors)

            yield emit("completed", {
                "status": "completed",
                "target": job.target,
                "result": {"type": "text", "text": result_text},
                "metadata": {
                    "adapter": self._selectors.provider_id,
                    "selector_version": self._selectors.selector_version,
                    "token_count": token_index,
                    "dom_signature": dom_signature,
                },
            })

        except DriftDetectedError as exc:
            screenshot = None
            try:
                screenshot = driver.capture_screenshot(
                    "%s-drift-%s" % (self._selectors.provider_id, job.job_id)
                )
            except Exception:  # noqa: BLE001 - artifact capture is best-effort
                screenshot = None
            yield emit("blocked", {
                "status": "blocked",
                "target": job.target,
                "adapter": self._selectors.provider_id,
                "reason": "selector_drift_detected",
                "error_code": exc.error_code,
                "selector_group": exc.group_name,
                "selector_version": exc.selector_version,
                "retryable": False,
                "artifact_screenshot": screenshot,
                "message": (
                    "Selector drift detected; all fallbacks failed. The adapter "
                    "must be re-baselined before this target can run."
                ),
            })
        finally:
            if owns_driver:
                try:
                    driver.close()
                except Exception:  # noqa: BLE001 - never mask the primary error
                    pass


class _NormalizedJob:
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
    )

    def __init__(self, **kwargs: Any) -> None:
        for key in self.__slots__:
            setattr(self, key, kwargs[key])


def _normalize_payload(payload: Mapping[str, Any], provider_id: str) -> _NormalizedJob:
    job_payload = payload.get("job", {})
    if not isinstance(job_payload, Mapping):
        job_payload = {}

    input_payload = _mapping_or_empty(job_payload.get("input", payload.get("input", {})))
    options = _mapping_or_empty(job_payload.get("options", payload.get("options", {})))

    api_version = _string_or_default(payload.get("api_version"), "2026-05-22")
    target = _string_or_default(job_payload.get("target", payload.get("target")), provider_id)
    command_type = _string_or_default(
        job_payload.get("command_type", job_payload.get("type")), "chat.prompt"
    )
    prompt = _extract_prompt(input_payload, payload)

    job_id = _derive_job_id(payload, job_payload)
    trace_id = _string_or_default(payload.get("trace_id"), "trace_" + digest(job_id)[:16])

    context = _manual_context(payload)
    session_id = _safe_session_id(context.get("session_id"), job_id, target)

    user_data_dir = _resolve_user_data_dir(options, context, target)
    headless = bool(options.get("headless", False))

    return _NormalizedJob(
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
        account_binding_id=_clean_text(context.get("account_binding_id"), "unbound"),
        consent_ref=_clean_text(context.get("consent_ref"), "unspecified"),
        automation_scope=_clean_scope(context.get("automation_scope")),
        manual_login_timeout_s=_float_or_default(
            options.get("manual_login_timeout_s"), _DEFAULT_MANUAL_LOGIN_TIMEOUT_S
        ),
        response_timeout_s=_float_or_default(
            options.get("response_timeout_s"), _DEFAULT_RESPONSE_TIMEOUT_S
        ),
    )


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


def _profile_label(user_data_dir: str) -> str:
    # Never leak full filesystem paths in events; expose only the leaf label.
    return os.path.basename(os.path.normpath(user_data_dir)) or "default"


def _novnc_url(session_id: str) -> str:
    return "http://127.0.0.1:7900/session/%s" % session_id


def _safe_session_id(value: Any, job_id: str, target: str) -> str:
    if isinstance(value, str):
        candidate = value.strip()
        if re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,95}", candidate):
            return candidate
    return "sess_" + digest(job_id + target)[:16]


def _derive_job_id(payload: Mapping[str, Any], job_payload: Mapping[str, Any]) -> str:
    explicit_id = payload.get("job_id", job_payload.get("job_id", job_payload.get("id")))
    if explicit_id is not None:
        return str(explicit_id)
    idempotency_key = payload.get("idempotency_key")
    seed = str(idempotency_key) if idempotency_key is not None else canonical_json(payload)
    return "job_" + digest(seed)[:16]


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


__all__ = ["LiveSessionEngine", "LiveSessionError"]

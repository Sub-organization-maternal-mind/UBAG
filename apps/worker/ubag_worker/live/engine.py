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

import json
import os
import re
from typing import TYPE_CHECKING, Any, Iterator, List, Mapping, Optional

# Reuse the canonical secret-material guard so the live path rejects exactly the
# same disallowed credential/cookie/token material as the registry & mock paths.
from ..adapter_registry import _contains_disallowed_secret_material  # noqa: E402
from .events import JsonObject, canonical_json, digest, worker_event
from .page_driver import (
    AUTHENTICATED,
    DriftDetectedError,
    ManualActionRequired,
    PageDriver,
    create_default_driver,
)
from .selectors import ProviderSelectors

if TYPE_CHECKING:  # pragma: no cover - typing only
    from .orchestrator import LiveOrchestrator

_DEFAULT_MANUAL_LOGIN_TIMEOUT_S = 300.0
_DEFAULT_RESPONSE_TIMEOUT_S = 120.0
# Reasoning modes (DeepSeek DeepThink, Gemini Extended thinking) routinely run
# far longer than a plain reply; a short timeout would clip the answer or be
# mistaken for a hang. Used as a floor only when the provider enables reasoning.
_DEFAULT_REASONING_RESPONSE_TIMEOUT_S = 360.0
# Grace window to let a freshly-opened, already-authenticated SPA page render its
# auth markers before the engine concludes the user must log in. Env-overridable.
_DEFAULT_LOGIN_READY_GRACE_S = 12.0
# Extended ceiling used ONLY when no sign-in form is on screen (a heavy account is
# still rendering): keep polling the auth marker this long before surfacing
# manual_action_required. Env-overridable.
_DEFAULT_LOGIN_READY_EXTENDED_S = 45.0

# Telemetry event types appended additively when an orchestrator is wired in.
# The gateway worker-consumer intercepts both BEFORE applying the canonical
# event-stream state machine, so they never poison a job.
_CONCURRENCY_CHANGE_EVENT_TYPE = "concurrency.cap_changed"
_TOPOLOGY_REPORT_EVENT_TYPE = "browser.topology_reported"


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
        orchestrator: "Optional[LiveOrchestrator]" = None,
    ) -> None:
        self._selectors = selectors
        self._manual_login_timeout_s = manual_login_timeout_s
        self._response_timeout_s = response_timeout_s
        # Optional process-level orchestrator (Fleet + ChannelPool + AIMD). When
        # ``None`` (the default) the engine behaves byte-for-byte as before and
        # emits only the canonical event stream — every existing test stays green.
        self._orchestrator = orchestrator

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

        # Orchestration state (only used when an orchestrator was injected). The
        # lease is acquired *after* authentication so a manual-login block never
        # consumes a tab. ``orch_success``/``orch_signal`` drive the AIMD outcome.
        orch_lease = None
        orch_success = True
        orch_signal = None

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
                # A freshly-opened SPA page often has not rendered its authenticated
                # markers within the first detection window. Poll briefly before
                # declaring manual login so a slow cold load is not mis-reported as
                # logged-out (which would emit a spurious manual_action_required for
                # an already-authenticated user and can fail an otherwise-good job).
                # This polls the authenticated marker only — it never simulates a
                # human login, so a genuinely logged-out session still falls through
                # to the manual flow below.
                login_state = driver.wait_until_authenticated(
                    self._selectors, timeout_s=_login_ready_grace_s()
                )
            if login_state != AUTHENTICATED and not driver.login_signal_present(self._selectors):
                # No sign-in form on screen — the session is authenticated but a
                # heavy account is still rendering its markers. Keep polling up to
                # the extended ceiling before ever surfacing manual_action_required,
                # which would poison an already-authenticated job.
                login_state = driver.wait_until_authenticated(
                    self._selectors, timeout_s=_login_ready_extended_s()
                )
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

            # Acquire an orchestration lease (Fleet context + ChannelPool tab)
            # for this job, then report the live browser→context→tab topology.
            if self._orchestrator is not None:
                orch_lease = self._orchestrator.lease(
                    tenant_id=job.tenant_id,
                    provider_id=self._selectors.provider_id,
                    identity_ref=job.account_binding_id,
                    job_id=job.job_id,
                    conversation_id=job.conversation_id,
                )

            yield emit("running", {
                "status": "running",
                "target": job.target,
                "adapter": self._selectors.provider_id,
                "command_type": job.command_type,
                "selector_version": self._selectors.selector_version,
            })

            if self._orchestrator is not None and orch_lease is not None:
                snapshot = self._orchestrator.topology_snapshot(job.tenant_id)
                yield emit(_TOPOLOGY_REPORT_EVENT_TYPE, {
                    "status": "topology_reported",
                    "target": job.target,
                    "adapter": self._selectors.provider_id,
                    "tenant_id": job.tenant_id,
                    "instances": snapshot["instances"],
                    "contexts": snapshot["contexts"],
                    "tabs": snapshot["tabs"],
                })

            # Pre-submit configuration phase: fresh chat + model/mode/reasoning.
            # Every step is idempotent — the driver reads the live UI and only acts
            # when it differs, so this is safe to run on every job ("always, if it
            # is not already set"). A renamed New-chat control degrades to a logged
            # warning; a model/mode/toggle that cannot be confirmed raises
            # DriftDetectedError (handled below) rather than a silent no-op.
            if job.new_chat_enabled and self._selectors.new_chat is not None:
                started_new_chat = driver.start_new_chat(self._selectors)
                yield emit("session.new_chat", {
                    "status": "new_chat" if started_new_chat else "new_chat_skipped",
                    "target": job.target,
                    "adapter": self._selectors.provider_id,
                    "started": started_new_chat,
                    "message": (
                        "started a fresh conversation before configuring"
                        if started_new_chat
                        else "no New-chat control found; continuing in the current chat"
                    ),
                })

            if job.config_enabled and self._selectors.settings:
                applied_settings = driver.ensure_provider_config(
                    self._selectors, overrides=job.provider_config
                )
                yield emit("session.configured", {
                    "status": "configured",
                    "target": job.target,
                    "adapter": self._selectors.provider_id,
                    "settings": list(applied_settings),
                    "message": "enforced provider model/option settings before submit",
                })

            # Audio/file attach phase (optional). When the job carries an audio
            # artifact, attach the locally-materialized file to the provider's
            # upload control BEFORE submitting the prompt. Text-only jobs (no
            # audio_artifact_key) skip this branch entirely, so every existing
            # live job is byte-for-byte unchanged.
            if job.audio_artifact_key:
                attach_supported = (
                    self._selectors.file_input is not None
                    and bool(job.audio_local_path)
                )
                if not attach_supported:
                    yield emit("blocked", {
                        "status": "blocked",
                        "target": job.target,
                        "adapter": self._selectors.provider_id,
                        "reason": "audio_not_supported_by_target",
                        "retryable": False,
                        "message": (
                            "This target cannot attach audio (no file-input selector, "
                            "or the audio artifact was not materialized to a local "
                            "file); refusing to transcribe nothing."
                        ),
                    })
                    return
                # A missing/drifted file input raises DriftDetectedError, caught by
                # the existing drift handler — never a silent hang.
                driver.attach_files(self._selectors, [job.audio_local_path])
                yield emit("file.attached", {
                    "status": "file_attached",
                    "target": job.target,
                    "adapter": self._selectors.provider_id,
                    "artifact_key": job.audio_artifact_key,
                })

            driver.submit_prompt(self._selectors, job.prompt)

            # Reasoning modes need a longer ceiling; use the reasoning floor only
            # when this provider enables a slow thinking mode and config is on.
            stream_timeout_s = job.response_timeout_s
            if self._selectors.reasoning and job.config_enabled:
                stream_timeout_s = max(stream_timeout_s, _reasoning_response_timeout_s())

            token_index = 0
            for delta in driver.stream_response(
                self._selectors, timeout_s=stream_timeout_s
            ):
                yield emit("token", {
                    "status": "token_streaming",
                    "target": job.target,
                    "token_index": token_index,
                    "delta": {"text": delta},
                })
                token_index += 1

            return_mode = str((job.options or {}).get("return_mode") or "final")
            result_text = driver.read_final_response(
                self._selectors, return_mode=return_mode
            )
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
            # Selector drift is an adverse runtime signal — feed AIMD so the
            # ceiling backs off for the next job of this provider+identity.
            orch_success = False
            orch_signal = _drift_negative_signal()
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
        except ManualActionRequired as exc:
            orch_success = False
            orch_signal = _manual_action_negative_signal()
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
                "reason": exc.reason,
                "message": str(exc),
            })
            yield emit("blocked", {
                "status": "blocked",
                "target": job.target,
                "adapter": self._selectors.provider_id,
                "session_id": session_id,
                "reason": exc.reason,
                "retryable": True,
                "message": str(exc),
            })
        finally:
            # Release the orchestration lease and surface any AIMD cap change as
            # a trailing ``concurrency.cap_changed`` telemetry event. Computed
            # before release so ``in_flight`` reflects this job's busy tab.
            if self._orchestrator is not None and orch_lease is not None:
                state = self._orchestrator.concurrency_state(orch_lease)
                change = self._orchestrator.record_outcome(
                    orch_lease, success=orch_success, signal=orch_signal
                )
                if change is not None:
                    from ..orchestration.telemetry import concurrency_change_data

                    yield emit(
                        _CONCURRENCY_CHANGE_EVENT_TYPE,
                        concurrency_change_data(
                            target=job.target,
                            identity_ref=job.account_binding_id,
                            change=change,
                            minimum=state.minimum,
                            maximum=state.maximum,
                            in_flight=state.in_flight,
                        ),
                    )
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
        "tenant_id",
        "conversation_id",
        "audio_artifact_key",
        "audio_local_path",
        "wait_for_artifacts",
        "provider_config",
        "new_chat_enabled",
        "config_enabled",
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

    account_binding_id = _clean_text(context.get("account_binding_id"), "unbound")
    tenant_id = _string_or_default(
        context.get("tenant_id")
        or job_payload.get("tenant_id")
        or payload.get("tenant_id"),
        "default",
    )
    conversation_id = _optional_string(
        input_payload.get("conversation_id")
        or options.get("conversation_id")
        or job_payload.get("conversation_id")
    )

    # Optional audio-transcription inputs. ``audio_artifact_key`` names the job
    # artifact the caller uploaded; ``audio_local_path`` is the locally-materialized
    # file the worker runner/gateway resolved that key to (the engine never fetches
    # from the gateway itself). Text-only jobs leave all three empty.
    audio_artifact_key = _audio_artifact_key(input_payload)
    audio_local_path = _optional_string(
        input_payload.get("audio_local_path") or options.get("audio_local_path")
    )
    wait_for_artifacts = _string_tuple(options.get("wait_for_artifacts"))

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
        account_binding_id=account_binding_id,
        consent_ref=_clean_text(context.get("consent_ref"), "unspecified"),
        automation_scope=_clean_scope(context.get("automation_scope")),
        manual_login_timeout_s=_float_or_default(
            options.get("manual_login_timeout_s"), _DEFAULT_MANUAL_LOGIN_TIMEOUT_S
        ),
        response_timeout_s=_float_or_default(
            options.get("response_timeout_s"), _DEFAULT_RESPONSE_TIMEOUT_S
        ),
        tenant_id=tenant_id,
        conversation_id=conversation_id,
        audio_artifact_key=audio_artifact_key,
        audio_local_path=audio_local_path,
        wait_for_artifacts=wait_for_artifacts,
        provider_config=provider_config,
        new_chat_enabled=new_chat_enabled,
        config_enabled=config_enabled,
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
    # The base is operator-configurable for deployments (e.g. the loopback port
    # the live-browser viewer publishes), but it MUST stay loopback: the gateway
    # only forwards a noVNC URL to operators when it is a loopback http URL with
    # a port and a /session/ path (see isSafeLoopbackNoVNCURL). Anything else is
    # rejected, so we fall back to the safe default rather than emit a URL the
    # gateway would redact.
    base = os.environ.get("UBAG_NOVNC_BASE_URL", "http://127.0.0.1:7900").strip()
    base = base.rstrip("/")
    if not _is_loopback_novnc_base(base):
        base = "http://127.0.0.1:7900"
    return "%s/session/%s" % (base, session_id)


def _is_loopback_novnc_base(base: str) -> bool:
    match = re.fullmatch(r"http://([^/:@?#]+):(\d{1,5})", base)
    if not match:
        return False
    host = match.group(1).lower()
    port = int(match.group(2))
    if port < 1 or port > 65535:
        return False
    if host in ("localhost", "127.0.0.1", "::1", "[::1]"):
        return True
    return host.startswith("127.")


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
        raise LiveSessionError(
            "audio_artifact_key must be a single path segment without '/', '\\', or '%'"
        )
    return key


def _string_tuple(value: Any) -> tuple:
    if isinstance(value, (list, tuple)):
        return tuple(str(item).strip() for item in value if str(item).strip())
    if isinstance(value, str) and value.strip():
        return (value.strip(),)
    return ()


def _drift_negative_signal():
    """Map selector drift to an AIMD negative signal (lazy import)."""

    from ..orchestration.aimd import NegativeSignal

    return NegativeSignal.ERROR_RATE_SPIKE


def _manual_action_negative_signal():
    """Map human-required provider UI blocks to an AIMD negative signal."""

    from ..orchestration.aimd import NegativeSignal

    return NegativeSignal.ERROR_RATE_SPIKE


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


def _resolve_provider_config(
    provider_id: str, options: Mapping[str, Any]
) -> dict:
    """Merge per-provider config overrides from env + job options.

    Layering (lowest -> highest precedence): the provider's hardcoded defaults
    (applied later by the driver from the selectors), then an env var
    ``UBAG_PROVIDER_CONFIG_<ID>`` holding a JSON object, then the job's
    ``options.provider_config`` object. Reserved keys ``_enabled`` / ``_new_chat``
    gate the phase; other keys override a setting's desired value by its key.
    """

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
    return config


def _flag(value: Any, default: bool) -> bool:
    """Coerce an optional JSON/string flag to bool, falling back to ``default``."""

    if value is None:
        return default
    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        return value.strip().lower() not in ("0", "false", "no", "off", "")
    return bool(value)


def _env_flag(name: str, default: bool) -> bool:
    raw = os.environ.get(name, "").strip().lower()
    if not raw:
        return default
    return raw not in ("0", "false", "no", "off")


def _reasoning_response_timeout_s() -> float:
    """Response-timeout floor for reasoning modes (env-overridable)."""

    return _float_or_default(
        os.environ.get("UBAG_REASONING_RESPONSE_TIMEOUT_S"),
        _DEFAULT_REASONING_RESPONSE_TIMEOUT_S,
    )


def _login_ready_grace_s() -> float:
    """Grace window (s) to await auth markers on a cold page (env-overridable)."""

    return _float_or_default(
        os.environ.get("UBAG_LOGIN_READY_GRACE_S"),
        _DEFAULT_LOGIN_READY_GRACE_S,
    )


def _login_ready_extended_s() -> float:
    """Extended auth-marker poll (s) when no sign-in form is present."""

    return _float_or_default(
        os.environ.get("UBAG_LOGIN_READY_EXTENDED_S"),
        _DEFAULT_LOGIN_READY_EXTENDED_S,
    )


__all__ = ["LiveSessionEngine", "LiveSessionError"]

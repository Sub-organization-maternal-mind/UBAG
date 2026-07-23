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
from typing import TYPE_CHECKING, Any, Callable, Iterator, List, Mapping, Optional

# Reuse the canonical secret-material guard so the live path rejects exactly the
# same disallowed credential/cookie/token material as the registry & mock paths.
from ..adapter_registry import _contains_disallowed_secret_material  # noqa: E402
from .events import (
    CONVERSATION_THREAD_BOUND_EVENT_TYPE,
    CONVERSATION_THREAD_BROKEN_EVENT_TYPE,
    CONVERSATION_THREAD_REBOUND_EVENT_TYPE,
    JsonObject,
    canonical_json,
    digest,
    worker_event,
)
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
# How many times to attempt the buffered interaction (config -> submit -> read)
# before giving up, so a transient browser/CDP hiccup self-heals within the job.
_DEFAULT_INTERACTION_ATTEMPTS = 3

# Telemetry event types appended additively when an orchestrator is wired in.
# The gateway worker-consumer intercepts both BEFORE applying the canonical
# event-stream state machine, so they never poison a job.
_CONCURRENCY_CHANGE_EVENT_TYPE = "concurrency.cap_changed"
_TOPOLOGY_REPORT_EVENT_TYPE = "browser.topology_reported"

# The single field a conversation.* event payload carries: the provider chat URL.
# The gateway WorkerConsumer reads ONLY this field and forces every identity field
# (tenant, app, target, conversation key) from the trusted job record — so a
# thread event must carry the URL and nothing else (no cookies, storage state,
# session ids, or noVNC URLs). It mirrors the ``thread_ref`` field the gateway
# sends down in the dispatch envelope.
_CONVERSATION_THREAD_REF_FIELD = "thread_ref"


class LiveSessionError(ValueError):
    """Raised when a live job payload is invalid (e.g. carries secrets)."""


class ConversationThreadNotFoundError(LiveSessionError):
    """A bound provider chat thread could not be resumed and ``on_missing=fail``.

    Carries the stable target error code and the pre-built (redacted, URL-only)
    ``conversation.thread_broken`` telemetry event so :meth:`iter_events` can emit
    it before the error propagates and the gateway marks the binding broken.

    Subclasses :class:`LiveSessionError` so the interaction retry loop treats it as
    a deterministic outcome (no browser retry) rather than a transient hiccup.
    """

    error_code = "UBAG-TARGET-CONVERSATION-NOT-FOUND-001"

    def __init__(self, conversation_key: str, broken_event: Any) -> None:
        super().__init__(
            "bound provider chat thread for conversation %r could not be resumed "
            "(%s)" % (conversation_key, self.error_code)
        )
        self.conversation_key = conversation_key
        self.broken_event = broken_event


class LiveSessionEngine:
    """Drives a single live manual-session job for one provider."""

    def __init__(
        self,
        selectors: ProviderSelectors,
        *,
        manual_login_timeout_s: float = _DEFAULT_MANUAL_LOGIN_TIMEOUT_S,
        response_timeout_s: float = _DEFAULT_RESPONSE_TIMEOUT_S,
        orchestrator: "Optional[LiveOrchestrator]" = None,
        chat_sink: "Optional[Callable[..., Any]]" = None,
    ) -> None:
        self._selectors = selectors
        self._manual_login_timeout_s = manual_login_timeout_s
        self._response_timeout_s = response_timeout_s
        # Optional sink recording every chat UBAG creates, so the chat reaper can
        # only ever delete OUR chats and never the human's (see chat_ledger.py —
        # deletion on these providers is permanent). ``None`` (the default) keeps
        # the engine side-effect-free for tests and for anyone not running the
        # reaper; the event stream is identical either way, so no gateway or
        # contract change is needed to carry this.
        self._chat_sink = chat_sink
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

            # Pre-submit interaction (fresh chat -> model/option config -> optional
            # audio attach -> submit -> stream -> read), buffered so a transient
            # browser/CDP hiccup retries the WHOLE interaction from a clean page
            # instead of crashing the worker (which the gateway would surface as a
            # hard failure). Deterministic outcomes — selector drift, manual action,
            # an audio-unsupported target — are NOT retried.
            interaction = None
            attempts = _interaction_attempts()
            for attempt in range(1, attempts + 1):
                try:
                    interaction = self._run_interaction(driver, job)
                    break
                except (DriftDetectedError, ManualActionRequired, LiveSessionError):
                    raise
                except Exception:  # noqa: BLE001 - transient browser/CDP hiccup
                    if attempt >= attempts:
                        raise
                    driver.reset(self._selectors.target_url)

            blocked = interaction.get("blocked") if interaction else None
            if blocked is not None:
                yield emit("blocked", {
                    "status": "blocked",
                    "target": job.target,
                    "adapter": self._selectors.provider_id,
                    "reason": blocked["reason"],
                    "retryable": blocked.get("retryable", False),
                    "message": blocked["message"],
                })
                return

            for event_type, data in (interaction["events"] if interaction else []):
                yield emit(event_type, data)

        except ConversationThreadNotFoundError as exc:
            # The bound provider chat vanished and the caller chose "fail" (the
            # default). Emit the redacted thread_broken telemetry (URL only) so the
            # gateway marks the binding broken, then re-raise so the job fails with
            # the stable UBAG-TARGET-CONVERSATION-NOT-FOUND-001 code. Not an AIMD
            # provider-health signal — it is a caller/state error, so the ceiling
            # is left untouched (orch_success stays True).
            event_type, data = exc.broken_event
            yield emit(event_type, data)
            raise
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

    def _run_interaction(self, driver: PageDriver, job: "_NormalizedJob") -> JsonObject:
        """Run the buffered pre-submit + submit + read interaction.

        Returns ``{"events": [(type, data), ...], "blocked": {...} | None}``.
        Buffering (rather than yielding) lets :meth:`iter_events` retry the whole
        interaction on a transient browser/CDP hiccup without double-emitting
        events. Tokens are collected and replayed in order; for the final-result
        use case (Fix / Cross-Check) this is functionally identical to streaming.
        """
        events: List = []

        # Conversation affinity. Runs BEFORE start_new_chat. When the gateway
        # injected no conversation block (conversations disabled, or the job
        # carries none), ``conversation_key`` is None and every branch below is
        # skipped, so the path stays byte-identical to the pre-feature behavior.
        resumed = False
        bind_after_response = False  # emit thread_bound with the captured chat URL
        rebind_after_response = False  # emit thread_rebound with the captured URL
        if job.conversation_key is not None:
            thread_ref = job.conversation_thread_ref
            if thread_ref:
                # A bound thread exists: resume it so the end user keeps context.
                resumed = driver.resume_thread(self._selectors, thread_ref)
                if not resumed:
                    if job.conversation_on_missing == "restart":
                        # Opt-in self-healing: fall through to a fresh chat and
                        # rebind the key to it after the response.
                        rebind_after_response = True
                    else:
                        # Default posture: mark the binding broken and fail loudly
                        # with a stable code. thread_broken carries ONLY the (now
                        # dead) URL; the gateway forces every identity field from
                        # the trusted job record.
                        broken_event = (CONVERSATION_THREAD_BROKEN_EVENT_TYPE, {
                            _CONVERSATION_THREAD_REF_FIELD: thread_ref,
                        })
                        raise ConversationThreadNotFoundError(
                            job.conversation_key, broken_event
                        )
            else:
                # First job for an unseen key: open a new chat below, then bind the
                # key to the assigned chat URL after the response.
                bind_after_response = True

        # Skip start_new_chat when we resumed a bound thread — starting a new chat
        # would discard exactly the context we just navigated back to.
        if not resumed and job.new_chat_enabled and self._selectors.new_chat is not None:
            started_new_chat = driver.start_new_chat(self._selectors)
            events.append(("session.new_chat", {
                "status": "new_chat" if started_new_chat else "new_chat_skipped",
                "target": job.target,
                "adapter": self._selectors.provider_id,
                "started": started_new_chat,
                "message": (
                    "started a fresh conversation before configuring"
                    if started_new_chat
                    else "no New-chat control found; continuing in the current chat"
                ),
            }))

        if job.config_enabled and self._selectors.settings:
            applied_settings = driver.ensure_provider_config(
                self._selectors, overrides=job.provider_config
            )
            events.append(("session.configured", {
                "status": "configured",
                "target": job.target,
                "adapter": self._selectors.provider_id,
                "settings": list(applied_settings),
                "message": "enforced provider model/option settings before submit",
            }))

        attach_paths = list(job.attachment_local_paths)
        # Back-compat: an older gateway materializes a single dictation-audio job
        # to audio_local_path only — treat that as one attachment.
        if not attach_paths and job.audio_local_path:
            attach_paths = [job.audio_local_path]
        if job.attachments or job.audio_artifact_key:
            attach_supported = (
                self._selectors.file_input is not None and bool(attach_paths)
            )
            if not attach_supported:
                return {"events": events, "blocked": {
                    "reason": "attachment_not_supported_by_target",
                    "retryable": False,
                    "message": (
                        "This target cannot attach files (no file-input selector, "
                        "or the attachments were not materialized to local files); "
                        "refusing to submit without the requested attachments."
                    ),
                }}
            # A missing/drifted file input raises DriftDetectedError, caught by the
            # existing drift handler — never a silent hang. attach_files takes the
            # full list, so all files land in one operation.
            driver.attach_files(self._selectors, attach_paths)
            attached_keys = [item["key"] for item in job.attachments]
            if not attached_keys and job.audio_artifact_key:
                attached_keys = [job.audio_artifact_key]
            events.append(("file.attached", {
                "status": "file_attached",
                "target": job.target,
                "adapter": self._selectors.provider_id,
                "artifact_keys": attached_keys,
                "count": len(attach_paths),
            }))

        driver.submit_prompt(self._selectors, job.prompt)

        # Reasoning modes need a longer ceiling; use the reasoning floor only when
        # this provider enables a slow thinking mode and config is on.
        stream_timeout_s = job.response_timeout_s
        if self._selectors.reasoning and job.config_enabled:
            stream_timeout_s = max(stream_timeout_s, _reasoning_response_timeout_s())

        token_index = 0
        for delta in driver.stream_response(self._selectors, timeout_s=stream_timeout_s):
            events.append(("token", {
                "status": "token_streaming",
                "target": job.target,
                "token_index": token_index,
                "delta": {"text": delta},
            }))
            token_index += 1

        return_mode = str((job.options or {}).get("return_mode") or "final")
        result_text = driver.read_final_response(self._selectors, return_mode=return_mode)
        dom_signature = driver.dom_signature(self._selectors)

        events.append(("completed", {
            "status": "completed",
            "target": job.target,
            "result": {"type": "text", "text": result_text},
            "metadata": {
                "adapter": self._selectors.provider_id,
                "selector_version": self._selectors.selector_version,
                "token_count": token_index,
                "dom_signature": dom_signature,
            },
        }))

        # Bind / rebind the conversation key AFTER the response, once the provider
        # has assigned/settled the canonical chat URL. Best-effort like
        # start_new_chat: with no bindable URL (base driver) nothing is emitted, so
        # the store is never handed an empty ref. The event carries ONLY the URL.
        #
        # The URL is now also captured for UNBOUND jobs, purely to record it in the
        # chat ledger: the reaper may only ever delete chats UBAG is recorded as
        # having created, so a chat we never record can never be cleaned up (and,
        # far more importantly, a chat the HUMAN created can never be mistaken for
        # ours). Resumed threads are skipped — we did not create that chat on this
        # run, and it is bound anyway.
        chat_url = ""
        if bind_after_response or rebind_after_response or (
            self._chat_sink is not None and not resumed
        ):
            chat_url = driver.current_thread_url(self._selectors)
        if chat_url and (bind_after_response or rebind_after_response):
            event_type = (
                CONVERSATION_THREAD_REBOUND_EVENT_TYPE
                if rebind_after_response
                else CONVERSATION_THREAD_BOUND_EVENT_TYPE
            )
            events.append((event_type, {
                _CONVERSATION_THREAD_REF_FIELD: chat_url,
            }))
        if chat_url and self._chat_sink is not None and not resumed:
            # Best-effort: a ledger failure must never fail a job that already
            # produced a good answer (worst case: one chat is never reaped).
            try:
                self._chat_sink(
                    url=chat_url,
                    target=self._selectors.provider_id,
                    conversation_key=job.conversation_key,
                )
            except Exception:  # noqa: BLE001
                pass

        return {"events": events, "blocked": None}


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
    # locally-materialized files the gateway resolved those keys to (the engine
    # never fetches from the gateway itself). Text-only jobs leave both empty.
    attachments = _attachments_manifest(input_payload)
    attachment_local_paths = _string_tuple(
        input_payload.get("attachment_local_paths")
        or options.get("attachment_local_paths")
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


def _conversation_binding(payload: Mapping[str, Any]) -> tuple:
    """Extract the gateway's conversation-affinity block from the envelope.

    Returns ``(key, thread_ref, on_missing)``:

    * ``key`` is the caller-owned conversation key, or ``None`` when the gateway
      injected no block (conversations disabled, or the job carries none). ``None``
      means "conversation affinity is off for this job" — the today path exactly.
    * ``thread_ref`` is the bound provider chat URL to resume, or ``None`` for an
      unseen key (first job -> bind after the response).
    * ``on_missing`` is ``"fail"`` (default) or ``"restart"``; any other value is
      normalized to the safe default ``"fail"``.
    """

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
        raise LiveSessionError("attachments must be an array")
    manifest = []
    for item in raw:
        if not isinstance(item, Mapping):
            raise LiveSessionError("each attachment must be an object")
        key = str(item.get("key", "")).strip()
        if not key:
            raise LiveSessionError("attachment.key is required")
        if any(ch in key for ch in ("/", "\\", "%", "\x00")):
            raise LiveSessionError(
                "attachment.key must be a single path segment without '/', '\\', or '%'"
            )
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


# Characters that could let a provider_config value break out of the Playwright
# selector it is interpolated into via ``.format(value=desired)`` in
# ``page_driver``: quotes, a closing paren, a backslash, or a newline.
# Every resolved value is interpolated into a Playwright selector as
# ``:has-text("{value}")`` (double-quoted). Only characters that break OUT of
# that double-quoted string are dangerous: the double quote itself, a backslash
# (CSS escape), and newlines. Parentheses, single quotes, and spaces are common
# in real provider UI labels (e.g. ``2.5 Flash (Preview)``) and are safe inside
# the quotes, so rejecting them would break legitimate operator env overrides on
# the no-model-settings path.
_PROVIDER_CONFIG_FORBIDDEN_CHARS = ('"', "\\", "\n", "\r")


def _sanitize_provider_config_value(value: Any) -> Any:
    """Reject provider_config values that could break a Playwright selector.

    The gateway already validates ``model_settings`` against the target adapter's
    catalog, but this is defense in depth: every resolved ``desired`` value is
    interpolated into a selector template via ``.format(value=desired)`` in
    ``page_driver``, so a value carrying a quote, closing paren, backslash, or
    newline could alter the selector's meaning. Booleans (toggle settings) pass
    through untouched; other non-string values are left as-is.
    """

    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        for char in _PROVIDER_CONFIG_FORBIDDEN_CHARS:
            if char in value:
                raise ValueError(
                    "provider_config value contains a disallowed selector "
                    "metacharacter: %r" % char
                )
    return value


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
    return {key: _sanitize_provider_config_value(val) for key, val in config.items()}


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


def _interaction_attempts() -> int:
    """How many times to attempt the buffered interaction (env-overridable)."""

    raw = os.environ.get("UBAG_INTERACTION_ATTEMPTS", "").strip()
    if raw.isdigit() and int(raw) >= 1:
        return int(raw)
    return _DEFAULT_INTERACTION_ATTEMPTS


__all__ = [
    "ConversationThreadNotFoundError",
    "LiveSessionEngine",
    "LiveSessionError",
]

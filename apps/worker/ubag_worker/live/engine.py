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
from typing import TYPE_CHECKING, Any, Callable, Iterator, List, Mapping, Optional

from .envelope import (
    DEFAULT_MANUAL_LOGIN_TIMEOUT_S as _DEFAULT_MANUAL_LOGIN_TIMEOUT_S,
)
from .envelope import (
    DEFAULT_RESPONSE_TIMEOUT_S as _DEFAULT_RESPONSE_TIMEOUT_S,
)
from .envelope import (
    EnvelopeError,
    _float_or_default,  # noqa: F401 - re-export (historical import path)
    _resolve_provider_config,  # noqa: F401 - re-export (historical import path)
    _sanitize_provider_config_value,  # noqa: F401 - re-export (historical import path)
)
from .envelope import (
    NormalizedJob as _NormalizedJob,
)
from .envelope import (
    env_flag as _env_flag,  # noqa: F401 - re-export (historical import path)
)
from .envelope import (
    flag as _flag,  # noqa: F401 - re-export (historical import path)
)
from .envelope import (
    normalize_payload as _normalize_envelope,
)
from .events import (
    CONVERSATION_THREAD_BOUND_EVENT_TYPE,
    CONVERSATION_THREAD_BROKEN_EVENT_TYPE,
    CONVERSATION_THREAD_REBOUND_EVENT_TYPE,
    JsonObject,
    worker_event,
)
from .page_driver import (
    AUTHENTICATED,
    DriftDetectedError,
    ManualActionRequired,
    PageDriver,
    create_default_driver,
)

# Reuse the canonical secret-material guard (leaf module) so the live path
# rejects exactly the same disallowed credential/cookie/token material as the
# registry & mock paths.
from .secret_scan import _contains_disallowed_secret_material  # noqa: E402
from .selectors import ProviderSelectors

if TYPE_CHECKING:  # pragma: no cover - typing only
    from .orchestrator import LiveOrchestrator

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

_ATTACHMENT_CONTENT_TYPES = {
    "document": frozenset(
        ("application/pdf", "text/plain", "text/markdown", "text/csv", "application/json")
    ),
    "image": frozenset(("image/png", "image/jpeg", "image/gif", "image/webp")),
    "audio": frozenset(
        ("audio/webm", "audio/wav", "audio/x-wav", "audio/mpeg", "audio/mp4", "audio/ogg")
    ),
    "voice": frozenset(
        ("audio/webm", "audio/wav", "audio/x-wav", "audio/mpeg", "audio/mp4", "audio/ogg")
    ),
    "video": frozenset(("video/mp4", "video/webm")),
}

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
        orch_ctx = None
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
            # for this job. The context manager owns the release protocol;
            # the engine only sets the AIMD outcome fields on the lease.
            if self._orchestrator is not None:
                orch_ctx = self._orchestrator.leased(
                    tenant_id=job.tenant_id,
                    provider_id=self._selectors.provider_id,
                    identity_ref=job.account_binding_id,
                    job_id=job.job_id,
                    conversation_id=job.conversation_id,
                )
                orch_lease = orch_ctx.__enter__()

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
            # Release the orchestration lease via the context manager (which
            # performs record_outcome) and surface any AIMD cap change as a
            # trailing ``concurrency.cap_changed`` telemetry event.
            if self._orchestrator is not None and orch_lease is not None:
                orch_lease.outcome_success = orch_success
                orch_lease.outcome_signal = orch_signal
                try:
                    orch_ctx.__exit__(None, None, None)
                except Exception:  # noqa: BLE001 - release must never mask the job outcome
                    pass
                change = orch_lease.cap_change
                state = orch_lease.cap_state
                if change is not None and state is not None:
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
        elif self._selectors.settings:
            # Config disabled for this job (_enabled=false or operator gate
            # off): skip the picker entirely and record WHY, so a drifted
            # model menu can never fail a job the operator chose to run
            # unconfigured. The prompt still submits in the account's current
            # mode — best-effort, never blocked.
            events.append(("session.configured", {
                "status": "skipped_config_disabled",
                "target": job.target,
                "adapter": self._selectors.provider_id,
                "settings": [],
                "message": "provider settings skipped: config disabled for this job",
            }))

        attach_paths = list(job.attachment_local_paths)
        # Back-compat: an older gateway materializes a single dictation-audio job
        # to audio_local_path only — treat that as one attachment.
        if not attach_paths and job.audio_local_path:
            attach_paths = [job.audio_local_path]
        if job.attachments or job.audio_artifact_key:
            rejected = next(
                (
                    item
                    for item in job.attachments
                    if item["kind"] not in _ATTACHMENT_CONTENT_TYPES
                    or item["content_type"]
                    not in _ATTACHMENT_CONTENT_TYPES.get(item["kind"], ())
                ),
                None,
            )
            if rejected is not None:
                return {"events": events, "blocked": {
                    "reason": "attachment_type_rejected",
                    "retryable": False,
                    "message": (
                        "Attachment %r has an invalid kind/content-type pair; "
                        "refusing to attach untrusted metadata."
                    ) % rejected["key"],
                }}
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
            attached_kinds = [item["kind"] for item in job.attachments]
            if not attached_kinds and job.audio_artifact_key:
                attached_kinds = ["audio"]
            events.append(("file.attached", {
                "status": "file_attached",
                "target": job.target,
                "adapter": self._selectors.provider_id,
                "artifact_keys": attached_keys,
                "attachment_kinds": attached_kinds,
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

def _normalize_payload(payload: Mapping[str, Any], provider_id: str) -> _NormalizedJob:
    # Shared envelope normalization (live/envelope.py) with the engine's
    # historical error type preserved at this boundary.
    try:
        return _normalize_envelope(payload, provider_id)
    except EnvelopeError as exc:
        raise LiveSessionError(str(exc)) from exc


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


def _drift_negative_signal():
    """Map selector drift to an AIMD negative signal (lazy import)."""

    from ..orchestration.aimd import NegativeSignal

    return NegativeSignal.ERROR_RATE_SPIKE


def _manual_action_negative_signal():
    """Map human-required provider UI blocks to an AIMD negative signal."""

    from ..orchestration.aimd import NegativeSignal

    return NegativeSignal.ERROR_RATE_SPIKE


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

"""Per-provider selector configuration with drift-detection metadata.

Selectors are centralized here (one :class:`ProviderSelectors` per provider) so
that selector drift can be patched in a single, auditable place. Every group
keeps an ordered list of fallback selectors plus a ``baseline_version`` marker
used by the engine's drift-detection hook.

NOTE: The selector strings below are best-effort, public-DOM CSS/ARIA guesses.
They WILL drift as providers ship UI changes. Each group is annotated with a
``TODO(drift)`` marker and a ``baseline_version`` so operators know when a
baseline was last confirmed. The engine surfaces ``UBAG-ADAPTER-DRIFT-014`` when
all fallbacks fail.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import List, Optional, Sequence


@dataclass(frozen=True)
class SelectorGroup:
    """An ordered set of fallback selectors for a single UI affordance.

    The first selector is the preferred one; subsequent entries are tried in
    order before the engine declares drift.
    """

    name: str
    candidates: Sequence[str]
    baseline_version: str = "unverified"

    def __post_init__(self) -> None:  # pragma: no cover - trivial validation
        if not self.candidates:
            raise ValueError("selector group %s must define at least one candidate" % self.name)

    @property
    def primary(self) -> str:
        return self.candidates[0]

    def as_list(self) -> List[str]:
        return list(self.candidates)


@dataclass(frozen=True)
class ProviderSetting:
    """One idempotent UI setting enforced before a prompt is submitted.

    UBAG reads the current UI state and only acts when it differs from the
    desired value, so it is safe to run on every job (the operator's "always,
    if it is not already set" requirement). Two kinds are supported:

    * ``toggle`` — an on/off control (e.g. DeepSeek's *DeepThink*). ``on_when``
      lists selectors that match ONLY when the control is currently ON;
      ``toggle_click`` flips it.
    * ``choice`` — pick a labelled option (e.g. DeepSeek mode *Expert*, the
      Gemini model, the Gemini *Thinking level*). ``satisfied_when`` and
      ``apply_click`` are templates with a ``{value}`` placeholder substituted
      with the desired label; ``open_steps`` clicks open any menu first (each
      step is its own ordered fallback list).

    Selectors are plain strings (CSS / Playwright text engines); within a list
    the first visible one wins and the rest are drift fallbacks. NO credentials,
    NO storage state — pure UI configuration, same safe-mode posture as the rest
    of this module. ``desired`` may be overridden per job / per env var without a
    code change (see the engine's provider_config resolution).
    """

    key: str
    kind: str  # "toggle" | "choice"
    desired: object  # bool for toggle, str label for choice
    open_steps: Sequence[Sequence[str]] = ()
    # toggle
    on_when: Sequence[str] = ()
    toggle_click: Sequence[str] = ()
    # choice (templates; "{value}" -> the desired label)
    satisfied_when: str = ""
    apply_click: str = ""
    # When False, a setting that cannot be confirmed warns instead of blocking
    # the job (keeps a renamed control from killing an otherwise-good answer).
    required: bool = True


@dataclass(frozen=True)
class ChatDeleteFlow:
    """The PERMANENT delete flow for one exact chat, used only by the reaper.

    Every selector addresses a chat by its provider conversation id via a
    ``{conv_id}`` placeholder — never by title/age/position. That is the whole
    safety property: the reaper reads an id out of the chat ledger (a chat UBAG
    recorded itself as creating) and can physically not express "delete the
    oldest chat" or "delete anything matching this title", which on these real,
    human-owned accounts would destroy the operator's own work with no undo.

    ``open_options`` is dispatched via element.click() rather than a synthetic
    mouse click: the sidebar rows sit under overlays that intercept a positional
    click, which would silently click the WRONG element.
    """

    #: Row options button for one conversation. Templated with {conv_id}.
    open_options: str
    #: The "Delete" item inside that row's options menu.
    delete_item: str
    #: The confirm button in the "Delete chat?" dialog.
    confirm: str
    #: Matches only while the chat still exists; used to VERIFY the delete landed
    #: instead of trusting the click. Templated with {conv_id}.
    still_present: str
    #: Matches once the chat LIST itself has rendered. Load-bearing: without it,
    #: "row not found" on a still-loading sidebar is indistinguishable from
    #: "already deleted", and reporting the latter marks the ledger done so the
    #: chat is never retried (observed exactly that — a reaped-looking chat was
    #: still alive). Absence of this ⇒ refuse to conclude anything.
    list_ready: str = ""
    baseline_version: str = "unverified"


@dataclass(frozen=True)
class ProviderSelectors:
    """Provider-specific selector + navigation configuration.

    Attributes
    ----------
    provider_id:
        Canonical adapter id (e.g. ``chatgpt_web``).
    target_url:
        Chat UI entry URL the engine navigates to.
    selector_version:
        Bumped whenever the selector baseline is re-confirmed against the live
        site. Surfaced in ``completed`` metadata and drift events.
    login_signal / authenticated_signal:
        Groups used to decide whether the user-owned session is logged in.
    prompt_input / submit_button / response_container / streaming_indicator:
        Core interaction groups.
    """

    provider_id: str
    display_name: str
    target_url: str
    selector_version: str
    prompt_input: SelectorGroup
    submit_button: SelectorGroup
    response_container: SelectorGroup
    authenticated_signal: SelectorGroup
    login_signal: SelectorGroup
    streaming_indicator: SelectorGroup
    # Structural nodes sampled for the DOM drift signature (no text captured).
    drift_signature_nodes: Sequence[str] = field(default_factory=tuple)
    # Optional explicit "final answer" container. For reasoning models (e.g. DeepSeek
    # R1) the live DOM renders the chain-of-thought and the final answer as SEPARATE
    # nodes; response_container alone (.first) can latch the thinking pane. When set,
    # read_final_response uses this as the authoritative source for return_mode="final".
    final_answer_container: Optional[SelectorGroup] = None
    # Optional hidden <input type=file> for targets that support uploading a file
    # (e.g. dictation audio) into the chat before submitting the prompt. When None
    # the target does not support file attachment and the engine emits
    # ``audio_not_supported_by_target`` rather than silently transcribing nothing.
    # Deliberately NOT part of all_groups(): an absent attach control must never
    # fail the mandatory drift baseline for unrelated, text-only targets.
    file_input: Optional[SelectorGroup] = None
    # Optional ordered click-path that reveals the hidden <input type=file> for
    # providers that INJECT it on demand instead of rendering it at rest. Each step
    # is clicked in order; the FINAL step opens the native OS file chooser, which
    # the Playwright driver intercepts (expect_file_chooser) to set the files —
    # never a real file dialog. Verified live 2026-07-23 for Gemini
    # ("Upload & tools" -> "Upload files"); ChatGPT and DeepSeek render the input
    # at rest and leave this empty. Like file_input, NOT part of all_groups().
    file_attach_trigger: Sequence[SelectorGroup] = field(default_factory=tuple)
    # Optional "New chat / new conversation" affordance. When set, the engine
    # clicks it before configuring + submitting so each job starts a fresh
    # conversation (no context bleed between unrelated Fix requests). Best-effort:
    # a missing/renamed control warns rather than failing the job.
    new_chat: Optional[SelectorGroup] = None
    # Ordered, idempotent UI settings enforced before submit (model pickers, mode
    # pills, reasoning toggles). Empty = submit in whatever mode is current.
    settings: Sequence[ProviderSetting] = field(default_factory=tuple)
    # Optional PERMANENT chat deletion flow, used only by the chat reaper and only
    # ever against a chat id read back from the chat ledger (a chat UBAG recorded
    # itself as creating). None = this provider cannot be reaped, which is the
    # safe default: an unverified delete selector on a real account risks the
    # human's own chats, and provider deletion has no undo.
    #
    # Templates take "{conv_id}" (NOT a free-text title) so a delete can only ever
    # address one exact conversation. Deliberately NOT part of all_groups(): a
    # provider without a verified delete flow must never fail the drift baseline.
    delete_chat: Optional[ChatDeleteFlow] = None
    # True when any enforced setting turns on a slow "reasoning" mode (DeepThink,
    # Extended thinking); the engine lengthens the response timeout accordingly so
    # a long think is not mistaken for a hang.
    reasoning: bool = False

    def all_groups(self) -> List[SelectorGroup]:
        return [
            self.prompt_input,
            self.submit_button,
            self.response_container,
            self.authenticated_signal,
            self.login_signal,
            self.streaming_indicator,
        ]


# ---------------------------------------------------------------------------
# Provider definitions
#
# TODO(drift): re-confirm every selector group against the live site and bump
# ``selector_version`` + each group's ``baseline_version`` when verified.
# ---------------------------------------------------------------------------

CHATGPT_WEB = ProviderSelectors(
    provider_id="chatgpt_web",
    display_name="ChatGPT Web",
    target_url="https://chatgpt.com/",
    selector_version="2026-07-17-model-pinned",
    prompt_input=SelectorGroup(
        "prompt_input",
        (
            "#prompt-textarea",
            "textarea[data-id='root']",
            "div[contenteditable='true'][data-virtualkeyboard='true']",
            "textarea[placeholder*='Message']",
        ),
    ),
    submit_button=SelectorGroup(
        "submit_button",
        (
            "button[data-testid='send-button']",
            "button[aria-label*='Send']",
            "button[type='submit']",
        ),
    ),
    response_container=SelectorGroup(
        "response_container",
        (
            "div[data-message-author-role='assistant']",
            "div.markdown.prose",
            "[data-testid^='conversation-turn'] .markdown",
        ),
    ),
    authenticated_signal=SelectorGroup(
        "authenticated_signal",
        (
            "#prompt-textarea",
            "nav[aria-label='Chat history']",
            "button[data-testid='profile-button']",
        ),
    ),
    login_signal=SelectorGroup(
        "login_signal",
        (
            "button[data-testid='login-button']",
            "a[href*='auth/login']",
            "text=Log in",
        ),
    ),
    streaming_indicator=SelectorGroup(
        "streaming_indicator",
        (
            "button[data-testid='stop-button']",
            "button[aria-label*='Stop']",
            ".result-streaming",
        ),
    ),
    drift_signature_nodes=("main", "form", "#prompt-textarea"),
    file_input=SelectorGroup(
        "file_input",
        (
            # set_input_files targets the (often hidden) <input type=file>, not the
            # visible "Attach" button. Order: most specific input first.
            "input[type='file'][multiple]",
            "input[type='file']",
            "input[accept*='audio']",
        ),
    ),
    new_chat=SelectorGroup(
        "new_chat",
        (
            "a[data-testid='create-new-chat-button']",
            "a[href='/']:has-text('New chat')",
            "button[aria-label*='New chat']",
        ),
    ),
    # Verified 2026-07-17 by deleting a UBAG-created throwaway chat on the live
    # account. The row options button carries the conversation id directly
    # (data-conversation-options-trigger="<uuid>"), which is what makes an exact,
    # id-addressed delete possible — no title or position matching. The menu then
    # exposes stable testids (delete-chat-menu-item ->
    # delete-conversation-confirm-button, dialog "Delete chat? This will delete
    # <title>."). still_present re-checks the same id afterwards so the reaper
    # verifies the deletion instead of trusting the click.
    delete_chat=ChatDeleteFlow(
        open_options="button[data-conversation-options-trigger='{conv_id}']",
        delete_item="[data-testid='delete-chat-menu-item']",
        confirm="[data-testid='delete-conversation-confirm-button']",
        still_present="button[data-conversation-options-trigger='{conv_id}']",
        # Any conversation link means the sidebar has rendered, so an absent row
        # genuinely means "gone" rather than "not loaded yet".
        list_ready="a[href^='/c/']",
        baseline_version="2026-07-17-verified",
    ),
    # Operator default (always-on), superseding the 2026-06-29 "leave the account
    # default" decision: pin GPT-5.6 Sol + Medium intelligence on every job.
    #
    # Verified 2026-07-17 against live chatgpt.com (DOM re-baselined; the old
    # data-testid='model-switcher-dropdown-button' no longer exists). Both
    # controls live behind ONE composer pill whose label is the current
    # intelligence level ("Medium"). Clicking it opens a menu containing:
    #   * the intelligence levels as [role=menuitemradio] — Instant 5.5 / Medium /
    #     High / Pro (Pro renders cursor-not-allowed on this account), and
    #   * a nested [role=menuitem][aria-haspopup=menu] opener whose label is the
    #     CURRENT model ("GPT-5.6 Sol"); clicking it (hover is not required —
    #     _open_control clicks) reveals the models, also [role=menuitemradio]:
    #     GPT-5.6 Sol / GPT-5.5 / GPT-5.4 / GPT-5.3 / o3.
    # Selected state on both = aria-checked='true'.
    #
    # Why role=menuitemradio (not :has-text alone): the submenu OPENER carries the
    # same "GPT-5.6 Sol" text as the model row, but is role=menuitem — matching on
    # menuitemradio disambiguates. Verified on the live DOM: with the pill menu
    # open, :has-text("Medium") matches exactly 1 row and no model label contains
    # "Medium", so the two settings cannot cross-match.
    #
    # Order matters: model is enforced BEFORE thinking, because switching model can
    # reset the intelligence level (settings are applied in declaration order).
    settings=(
        ProviderSetting(
            key="model",
            kind="choice",
            desired="GPT-5.6 Sol",
            open_steps=(
                (
                    "button.__composer-pill[aria-haspopup='menu']",
                    "button[class*='composer-pill'][aria-haspopup='menu']",
                ),
                ("[role='menuitem'][aria-haspopup='menu']",),
            ),
            satisfied_when="[role='menuitemradio'][aria-checked='true']:has-text(\"{value}\")",
            apply_click="[role='menuitemradio']:has-text(\"{value}\")",
        ),
        ProviderSetting(
            key="thinking",
            kind="choice",
            desired="Medium",
            open_steps=(
                (
                    "button.__composer-pill[aria-haspopup='menu']",
                    "button[class*='composer-pill'][aria-haspopup='menu']",
                ),
            ),
            satisfied_when="[role='menuitemradio'][aria-checked='true']:has-text(\"{value}\")",
            apply_click="[role='menuitemradio']:has-text(\"{value}\")",
        ),
    ),
    # Medium intelligence thinks before answering, so give the reader the longer
    # reasoning timeout rather than mistaking a think for a hang.
    reasoning=True,
)

CLAUDE_WEB = ProviderSelectors(
    provider_id="claude_web",
    display_name="Claude Web",
    target_url="https://claude.ai/",
    selector_version="2026-05-22-baseline-unverified",
    prompt_input=SelectorGroup(
        "prompt_input",
        (
            "div[contenteditable='true'].ProseMirror",
            "div[contenteditable='true'][role='textbox']",
            "fieldset div[contenteditable='true']",
        ),
    ),
    submit_button=SelectorGroup(
        "submit_button",
        (
            "button[aria-label='Send message']",
            "button[aria-label*='Send']",
            "button[type='submit']",
        ),
    ),
    response_container=SelectorGroup(
        "response_container",
        (
            "div[data-testid='assistant-message']",
            "div.font-claude-message",
            "[data-is-streaming] .prose",
        ),
    ),
    authenticated_signal=SelectorGroup(
        "authenticated_signal",
        (
            "div[contenteditable='true'].ProseMirror",
            "nav[aria-label='Conversations']",
            "button[data-testid='user-menu-button']",
        ),
    ),
    login_signal=SelectorGroup(
        "login_signal",
        (
            "button[data-testid='login']",
            "a[href*='login']",
            "text=Continue with Google",
        ),
    ),
    streaming_indicator=SelectorGroup(
        "streaming_indicator",
        (
            "button[aria-label='Stop response']",
            "[data-is-streaming='true']",
            "button[aria-label*='Stop']",
        ),
    ),
    drift_signature_nodes=("main", "fieldset", "div.ProseMirror"),
    file_input=SelectorGroup(
        "file_input",
        (
            "input[data-testid='file-upload']",
            "input[type='file']",
            "input[accept*='audio']",
        ),
    ),
)

DEEPSEEK_WEB = ProviderSelectors(
    provider_id="deepseek_web",
    display_name="DeepSeek Web",
    target_url="https://chat.deepseek.com/",
    selector_version="2026-06-29-controls-verified",
    prompt_input=SelectorGroup(
        "prompt_input",
        (
            "textarea#chat-input",
            "textarea[placeholder*='Message']",
            "div[contenteditable='true']",
        ),
    ),
    submit_button=SelectorGroup(
        "submit_button",
        (
            "div[role='button'][aria-disabled='false']",
            "div[role='button'].ds-button--primary.ds-button--circle:not([class*='disabled'])",
            "div[role='button'].ds-button--primary.ds-button--circle",
            "button[type='submit']",
            "button[aria-label*='Send']",
        ),
    ),
    response_container=SelectorGroup(
        "response_container",
        (
            # Verified 2026-06-22 against live chat.deepseek.com (R1 reasoning ON): the
            # reply renders TWO div.ds-markdown nodes - the chain-of-thought inside
            # div.ds-think-content, then the final answer in
            # div.ds-markdown.ds-assistant-message-main-content. The old bare
            # "div.ds-markdown" matched the thinking pane first (.first) and leaked the
            # reasoner's chain-of-thought as the result. Target the answer node, with a
            # structural xpath fallback that excludes anything inside a *think* pane.
            "div.ds-markdown.ds-assistant-message-main-content",
            "xpath=//div[contains(concat(' ', normalize-space(@class), ' '), ' ds-markdown ')][not(ancestor::*[contains(@class, 'ds-think')])]",
            "div[class*='message'][class*='assistant']",
            "div.markdown-body",
            "div.ds-markdown",
        ),
    ),
    final_answer_container=SelectorGroup(
        "final_answer_container",
        (
            "div.ds-markdown.ds-assistant-message-main-content",
            "xpath=//div[contains(concat(' ', normalize-space(@class), ' '), ' ds-markdown ')][not(ancestor::*[contains(@class, 'ds-think')])]",
        ),
    ),
    authenticated_signal=SelectorGroup(
        "authenticated_signal",
        (
            "textarea#chat-input",
            "textarea[placeholder*='Message']",
            "div[class*='sidebar']",
            "img[alt*='avatar']",
        ),
    ),
    login_signal=SelectorGroup(
        "login_signal",
        (
            "button[class*='login']",
            "text=Log in",
            "input[type='password']",
        ),
    ),
    streaming_indicator=SelectorGroup(
        "streaming_indicator",
        (
            "div[class*='stop']",
            ".result-streaming",
            "div[aria-label*='Stop']",
        ),
    ),
    drift_signature_nodes=("main", "textarea#chat-input"),
    file_input=SelectorGroup(
        "file_input",
        (
            "input[type='file']",
            "input[accept*='audio']",
        ),
    ),
    # Verified 2026-06-29 against live chat.deepseek.com: the composer sidebar
    # exposes a "New chat" control whose label is a <span>New chat</span> inside a
    # tabindex=0 wrapper; clicking the span bubbles to the wrapper.
    new_chat=SelectorGroup(
        "new_chat",
        (
            "div[tabindex='0']:has(> span:text-is('New chat'))",
            "div:has(> span:text-is('New chat'))",
            "span:text-is('New chat')",
        ),
    ),
    # Operator default (always-on): Expert mode + DeepThink reasoning. Verified
    # 2026-06-29: mode pills render as role=radio (active => aria-checked='true');
    # DeepThink is a div.ds-toggle-button whose ON state adds
    # 'ds-toggle-button--selected' (blue). Idempotent: only clicked when not set.
    settings=(
        ProviderSetting(
            key="mode",
            kind="choice",
            desired="Expert",
            satisfied_when="[role='radio'][aria-checked='true']:has-text(\"{value}\")",
            apply_click="[role='radio']:has-text(\"{value}\")",
        ),
        ProviderSetting(
            key="deepthink",
            kind="toggle",
            desired=True,
            on_when=(
                "div.ds-toggle-button--selected:has(span:text-is('DeepThink'))",
                ".ds-toggle-button--selected:has-text('DeepThink')",
            ),
            toggle_click=(
                "span:text-is('DeepThink')",
                "div.ds-toggle-button:has(span:text-is('DeepThink'))",
            ),
        ),
    ),
    reasoning=True,
)

GEMINI_WEB = ProviderSelectors(
    provider_id="gemini_web",
    display_name="Gemini Web",
    target_url="https://gemini.google.com/app",
    selector_version="2026-07-23-gemini-3.6-standard",
    # Re-baselined 2026-07-15 against live gemini.google.com/app. Gemini's
    # composer is a Quill editor whose <rich-textarea> holds TWO contenteditable
    # divs: the real composer (div.ql-editor, ~439x24) and an invisible
    # 'div.ql-clipboard' paste shim (0x1). The old lead selector
    # 'rich-textarea div[contenteditable=true]' matched BOTH, and _first_visible
    # takes .first — which resolved to the never-visible clipboard shim and burned
    # its full timeout before falling through, so a job whose composer had not yet
    # settled (i.e. straight after ensure_provider_config, with no wait) reported
    # prompt_input drift even though the composer was present and visible. Every
    # candidate below matches the real composer ONLY (verified count=1).
    prompt_input=SelectorGroup(
        "prompt_input",
        (
            "div.ql-editor[contenteditable='true']",
            "rich-textarea div.ql-editor[contenteditable='true']",
            "div[contenteditable='true'][aria-label*='Enter a prompt']",
            "div[contenteditable='true'][data-placeholder='Ask Gemini']",
            "textarea[aria-label*='prompt']",
        ),
    ),
    submit_button=SelectorGroup(
        "submit_button",
        (
            "button[aria-label*='Send message']",
            "button.send-button",
            "button[mattooltip*='Send']",
        ),
    ),
    response_container=SelectorGroup(
        "response_container",
        (
            # Verified 2026-06-22 against live gemini.google.com/app: a model reply
            # renders inside a <model-response> custom element; <message-content>
            # and .markdown hold the answer text. The previous
            # *.model-response-text* / data-response-index selectors had drifted and
            # matched zero nodes, so the worker hung waiting for a response that — by
            # its selectors — never appeared, hitting the 2-minute hard timeout.
            "message-content",
            "div.markdown",
            ".markdown",
            "model-response",
            # legacy fallbacks (pre-2026-06 Gemini DOM):
            "message-content.model-response-text",
            "div.model-response-text",
            "div[data-response-index]",
        ),
    ),
    authenticated_signal=SelectorGroup(
        "authenticated_signal",
        (
            "rich-textarea",
            "a[aria-label*='Google Account']",
            "img.gb_P",
        ),
    ),
    login_signal=SelectorGroup(
        "login_signal",
        (
            "a[href*='accounts.google.com']",
            "text=Sign in",
            "input[type='email']",
        ),
    ),
    streaming_indicator=SelectorGroup(
        "streaming_indicator",
        (
            "button[aria-label*='Stop']",
            "div.blinking-cursor",
            "progress-bar",
        ),
    ),
    drift_signature_nodes=("main", "rich-textarea"),
    file_input=SelectorGroup(
        "file_input",
        (
            # Verified live 2026-07-23: Gemini renders NO <input type=file> at rest.
            # It is injected only when "Upload & tools" -> "Upload files" fires the
            # native file chooser (see file_attach_trigger). This group is the
            # target the intercepted chooser resolves to.
            "input[type='file']",
            "input[name='Filedata']",
            "input[accept*='audio']",
        ),
    ),
    file_attach_trigger=(
        # Step 1: open the composer's upload menu ("Upload & tools").
        SelectorGroup(
            "upload_menu_button",
            (
                "button[aria-label*='Upload' i]",
                "button[mattooltip*='Upload' i]",
            ),
        ),
        # Step 2: the "Upload files" menu item — clicking it opens the native file
        # chooser the Playwright driver intercepts. Verified live 2026-07-23.
        SelectorGroup(
            "upload_files_item",
            (
                "[role='menuitem'][aria-label*='Upload files' i]",
                "button[aria-label*='Upload files' i]",
                "[aria-label*='Upload files' i]",
            ),
        ),
    ),
    # Verified 2026-06-29 against live gemini.google.com/app.
    new_chat=SelectorGroup(
        "new_chat",
        (
            "a[aria-label='New chat']",
            ".side-nav-sparkle-button",
            "[data-test-id='new-chat-button']",
        ),
    ),
    # Operator default (always-on): "3.6 Flash" with Standard thinking.
    #
    # Re-baselined 2026-07-17 against live gemini.google.com. Google FLATTENED the
    # mode picker: the nested "Thinking level" gem-menu-item (whose submenu offered
    # Standard / Extended) is GONE, and "Extended thinking" is now a sibling entry
    # in the single menu opened by data-test-id='bard-mode-menu-button':
    #     3.5 Flash-Lite | 3.6 Flash | 3.1 Pro | Extended thinking
    # "Standard" no longer exists as a label at all.
    #
    # Crucially the model and Extended thinking are NOT mutually exclusive —
    # verified live on 2026-07-23: clicking "3.6 Flash" can leave Extended
    # selected too. Standard has no menu label; it is the state where the
    # independent Extended toggle is OFF.
    #
    # Both settings are idempotent. The model is a labelled choice; thinking is
    # represented as a toggle whose desired state is False, so a persisted
    # Extended selection is clicked exactly once to return to Standard.
    settings=(
        ProviderSetting(
            key="model",
            kind="choice",
            desired="3.6 Flash",
            open_steps=(
                (
                    "button[data-test-id='bard-mode-menu-button']",
                    "button[aria-label*='mode picker']",
                    "button.input-area-switch",
                ),
            ),
            satisfied_when="gem-menu-item.selected:has-text(\"{value}\")",
            apply_click="gem-menu-item:has-text(\"{value}\")",
        ),
        ProviderSetting(
            key="thinking",
            kind="toggle",
            desired=False,
            open_steps=(
                (
                    "button[data-test-id='bard-mode-menu-button']",
                    "button[aria-label*='mode picker']",
                    "button.input-area-switch",
                ),
            ),
            on_when=("gem-menu-item.selected:has-text('Extended thinking')",),
            toggle_click=("gem-menu-item:has-text('Extended thinking')",),
        ),
    ),
    reasoning=False,
)

MISTRAL_LECHAT = ProviderSelectors(
    provider_id="mistral_lechat",
    display_name="Mistral Le Chat",
    target_url="https://chat.mistral.ai/chat",
    selector_version="2026-05-22-baseline-unverified",
    prompt_input=SelectorGroup(
        "prompt_input",
        (
            "textarea[name='message']",
            "textarea[placeholder*='Ask']",
            "div[contenteditable='true']",
        ),
    ),
    submit_button=SelectorGroup(
        "submit_button",
        (
            "button[type='submit']",
            "button[aria-label*='Send']",
            "button:has(svg[data-icon='send'])",
        ),
    ),
    response_container=SelectorGroup(
        "response_container",
        (
            "div[data-message-author='assistant']",
            "div.prose",
            "div[class*='assistant'] div.markdown",
        ),
    ),
    authenticated_signal=SelectorGroup(
        "authenticated_signal",
        (
            "textarea[name='message']",
            "nav[aria-label*='chat']",
            "button[aria-label*='account']",
        ),
    ),
    login_signal=SelectorGroup(
        "login_signal",
        (
            "a[href*='login']",
            "text=Sign in",
            "input[type='email']",
        ),
    ),
    streaming_indicator=SelectorGroup(
        "streaming_indicator",
        (
            "button[aria-label*='Stop']",
            ".animate-pulse",
            "div[data-streaming='true']",
        ),
    ),
    drift_signature_nodes=("main", "textarea[name='message']"),
    file_input=SelectorGroup(
        "file_input",
        (
            "input[type='file']",
            "input[accept*='audio']",
        ),
    ),
)

PERPLEXITY_WEB = ProviderSelectors(
    provider_id="perplexity_web",
    display_name="Perplexity Web",
    target_url="https://www.perplexity.ai/",
    selector_version="2026-05-22-baseline-unverified",
    prompt_input=SelectorGroup(
        "prompt_input",
        (
            "textarea[placeholder*='Ask anything']",
            "div[contenteditable='true']",
            "textarea[autofocus]",
        ),
    ),
    submit_button=SelectorGroup(
        "submit_button",
        (
            "button[aria-label='Submit']",
            "button[aria-label*='Submit']",
            "button[type='submit']",
        ),
    ),
    response_container=SelectorGroup(
        "response_container",
        (
            "div[class*='prose']",
            "div[id^='markdown-content']",
            "div.answer",
        ),
    ),
    authenticated_signal=SelectorGroup(
        "authenticated_signal",
        (
            "textarea[placeholder*='Ask anything']",
            "button[aria-label*='Account']",
            "a[href*='/settings']",
        ),
    ),
    login_signal=SelectorGroup(
        "login_signal",
        (
            "button:has-text('Sign in')",
            "a[href*='login']",
            "text=Continue with",
        ),
    ),
    streaming_indicator=SelectorGroup(
        "streaming_indicator",
        (
            "button[aria-label*='Stop']",
            "div[class*='animate']",
            ".loading-dots",
        ),
    ),
    drift_signature_nodes=("main", "textarea"),
    file_input=SelectorGroup(
        "file_input",
        (
            "input[type='file']",
            "input[accept*='audio']",
        ),
    ),
)


def live_web_template(
    provider_id: str,
    display_name: str,
    target_url: str,
    *,
    selector_version: str = "unverified-template",
    prompt_input: Optional[Sequence[str]] = None,
    submit_button: Optional[Sequence[str]] = None,
    response_container: Optional[Sequence[str]] = None,
    authenticated_signal: Optional[Sequence[str]] = None,
    login_signal: Optional[Sequence[str]] = None,
    streaming_indicator: Optional[Sequence[str]] = None,
    drift_signature_nodes: Sequence[str] = ("main",),
) -> ProviderSelectors:
    """Scaffold a :class:`ProviderSelectors` for a new live web provider.

    This is the supported entry point for onboarding a new ToS-safe live
    manual-session target. Pass a canonical ``provider_id``, a human-readable
    ``display_name``, and the chat UI ``target_url``; override only the selector
    groups you have confirmed against the live DOM. Every group defaults to a
    conservative, framework-agnostic placeholder so the engine can drive the
    manual-login flow before the selectors are tuned.

    The result is a pure configuration object: it carries NO automation logic,
    NO credentials, and NO storage state. Login, CAPTCHA, 2FA, and consent are
    always handled by the human in a user-owned browser session. Bump
    ``selector_version`` once a baseline is confirmed against the live site.
    """

    if not provider_id or not provider_id.strip():
        raise ValueError("provider_id is required")
    if not target_url.startswith("https://"):
        raise ValueError("target_url must be an https:// URL")

    def group(name: str, candidates: Optional[Sequence[str]], fallback: Sequence[str]) -> SelectorGroup:
        return SelectorGroup(name, tuple(candidates) if candidates else tuple(fallback))

    return ProviderSelectors(
        provider_id=provider_id,
        display_name=display_name,
        target_url=target_url,
        selector_version=selector_version,
        # TODO(drift): replace these generic placeholders with confirmed,
        # provider-specific selectors and bump selector_version.
        prompt_input=group(
            "prompt_input",
            prompt_input,
            (
                "textarea",
                "div[contenteditable='true']",
                "textarea[placeholder*='Message']",
            ),
        ),
        submit_button=group(
            "submit_button",
            submit_button,
            (
                "button[type='submit']",
                "button[aria-label*='Send']",
                "button[aria-label*='Submit']",
            ),
        ),
        response_container=group(
            "response_container",
            response_container,
            (
                "div[class*='assistant']",
                "div.markdown",
                "div.prose",
            ),
        ),
        authenticated_signal=group(
            "authenticated_signal",
            authenticated_signal,
            (
                "textarea",
                "div[contenteditable='true']",
                "nav",
            ),
        ),
        login_signal=group(
            "login_signal",
            login_signal,
            (
                "a[href*='login']",
                "text=Sign in",
                "input[type='password']",
            ),
        ),
        streaming_indicator=group(
            "streaming_indicator",
            streaming_indicator,
            (
                "button[aria-label*='Stop']",
                ".result-streaming",
                "div[data-streaming='true']",
            ),
        ),
        drift_signature_nodes=tuple(drift_signature_nodes),
    )


# Copy-and-tune starting point for a brand-new live provider. Registered so it
# is discoverable end-to-end, but intentionally points at a neutral target with
# placeholder selectors until a real provider is wired in.
GENERIC_LIVE_WEB = live_web_template(
    provider_id="generic_live_web",
    display_name="Generic Live Web (template)",
    target_url="https://example.com/",
    selector_version="2026-05-22-template-unverified",
)


PROVIDER_SELECTORS = {
    selectors.provider_id: selectors
    for selectors in (
        CHATGPT_WEB,
        CLAUDE_WEB,
        DEEPSEEK_WEB,
        GEMINI_WEB,
        MISTRAL_LECHAT,
        PERPLEXITY_WEB,
        GENERIC_LIVE_WEB,
    )
}


def get_provider_selectors(provider_id: str) -> ProviderSelectors:
    try:
        return PROVIDER_SELECTORS[provider_id]
    except KeyError as exc:  # pragma: no cover - defensive
        raise KeyError("no selector configuration for provider %s" % provider_id) from exc


__all__ = [
    "CHATGPT_WEB",
    "CLAUDE_WEB",
    "DEEPSEEK_WEB",
    "GEMINI_WEB",
    "GENERIC_LIVE_WEB",
    "MISTRAL_LECHAT",
    "PERPLEXITY_WEB",
    "PROVIDER_SELECTORS",
    "ProviderSelectors",
    "ProviderSetting",
    "SelectorGroup",
    "get_provider_selectors",
    "live_web_template",
]

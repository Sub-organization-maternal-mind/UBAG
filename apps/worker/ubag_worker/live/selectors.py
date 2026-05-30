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
    selector_version="2026-05-22-baseline-unverified",
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
)

DEEPSEEK_WEB = ProviderSelectors(
    provider_id="deepseek_web",
    display_name="DeepSeek Web",
    target_url="https://chat.deepseek.com/",
    selector_version="2026-05-22-baseline-unverified",
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
            "button[type='submit']",
            "button[aria-label*='Send']",
        ),
    ),
    response_container=SelectorGroup(
        "response_container",
        (
            "div.ds-markdown",
            "div[class*='message'][class*='assistant']",
            "div.markdown-body",
        ),
    ),
    authenticated_signal=SelectorGroup(
        "authenticated_signal",
        (
            "textarea#chat-input",
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
)

GEMINI_WEB = ProviderSelectors(
    provider_id="gemini_web",
    display_name="Gemini Web",
    target_url="https://gemini.google.com/app",
    selector_version="2026-05-22-baseline-unverified",
    prompt_input=SelectorGroup(
        "prompt_input",
        (
            "rich-textarea div[contenteditable='true']",
            "div.ql-editor[contenteditable='true']",
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
    "SelectorGroup",
    "get_provider_selectors",
    "live_web_template",
]

"""Format detection — second stage of the normalization pipeline (§16)."""

from __future__ import annotations

import enum
import json
import re


class FormatKind(str, enum.Enum):
    MARKDOWN = "markdown"
    PLAIN = "plain"
    HTML = "html"
    CODE = "code"
    JSON = "json"
    MIXED = "mixed"


# Markdown signals that are NOT code fences (to avoid conflating code with markdown).
_MARKDOWN_SIGNALS = re.compile(
    r"(^#{1,6}\s|\*\*|__|\[.+\]\(.+\)|^[-*]\s|^>\s|`[^`]{1,}[^`]`)",
    re.MULTILINE,
)
_HTML_TAG_RE = re.compile(r"<[a-zA-Z][^>]*>")
_CODE_FENCE_RE = re.compile(r"^```", re.MULTILINE)


def detect_format(text: str) -> FormatKind:
    """Return the dominant format of ``text``.

    Detection priority: JSON → HTML → Code (fenced) → Markdown → Plain.
    Returns ``MIXED`` when multiple strong signals are present.
    """
    stripped = text.strip()
    if not stripped:
        return FormatKind.PLAIN

    # JSON
    if stripped.startswith("{") or stripped.startswith("["):
        try:
            json.loads(stripped)
            return FormatKind.JSON
        except json.JSONDecodeError:
            pass

    has_html = bool(_HTML_TAG_RE.search(stripped))
    has_code_fence = bool(_CODE_FENCE_RE.search(stripped))
    md_hits = len(_MARKDOWN_SIGNALS.findall(stripped))
    has_markdown = md_hits >= 2

    signal_count = sum([has_html, has_markdown, has_code_fence])
    if signal_count >= 2:
        return FormatKind.MIXED
    if has_html:
        return FormatKind.HTML
    if has_code_fence:
        return FormatKind.CODE
    if has_markdown:
        return FormatKind.MARKDOWN
    return FormatKind.PLAIN

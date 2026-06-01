"""Text cleanup utilities — first stage of the normalization pipeline (§16)."""

from __future__ import annotations

import re
from typing import Sequence

# Boilerplate phrases commonly prepended/appended by providers.
_BOILERPLATE_PATTERNS = [
    re.compile(r"^As an AI( language model)?,?\s*", re.IGNORECASE | re.MULTILINE),
    re.compile(r"^I('m| am) Claude,?\s*", re.IGNORECASE | re.MULTILINE),
    re.compile(r"^(Sure|Certainly|Of course)[!,.]?\s+", re.IGNORECASE),
    re.compile(r"\n{3,}", re.MULTILINE),  # triple+ blank lines
]

_HTML_TAG_RE = re.compile(r"<[^>]+>")
_MULTI_SPACE_RE = re.compile(r"[ \t]+")
_LEADING_TRAILING_BLANK_LINES_RE = re.compile(r"^\n+|\n+$")


def strip_boilerplate(text: str) -> str:
    """Remove common AI-assistant boilerplate openers from ``text``."""
    for pattern in _BOILERPLATE_PATTERNS:
        text = pattern.sub("", text)
    return text.strip()


def normalize_whitespace(text: str) -> str:
    """Collapse runs of spaces/tabs to single space; trim leading/trailing blank lines."""
    lines = text.splitlines()
    cleaned = [_MULTI_SPACE_RE.sub(" ", line).rstrip() for line in lines]
    result = "\n".join(cleaned)
    return _LEADING_TRAILING_BLANK_LINES_RE.sub("", result)


def remove_html_tags(text: str) -> str:
    """Strip HTML tags from ``text``, leaving only the tag content."""
    return _HTML_TAG_RE.sub("", text)

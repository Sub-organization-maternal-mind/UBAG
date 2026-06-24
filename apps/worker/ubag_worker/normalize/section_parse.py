"""Section parsing — third stage of the normalization pipeline (§16)."""

from __future__ import annotations

import re
from dataclasses import dataclass
from typing import List, Optional

from .format_detect import FormatKind

_MD_HEADING_RE = re.compile(r"^(#{1,6})\s+(.+)$", re.MULTILINE)


@dataclass
class Section:
    """A parsed content section with an optional heading."""

    heading: Optional[str]
    body: str
    level: int = 0  # heading level 1-6; 0 for body-only sections


def parse_sections(text: str, fmt: Optional[FormatKind] = None) -> List[Section]:
    """Split ``text`` into sections using headings (Markdown) or paragraphs (plain/other).

    * Markdown: splits on ``#`` headings.
    * Plain / other: splits on double-newline paragraph boundaries.
    * Returns at least one ``Section`` even for empty text.
    """
    if not text.strip():
        return [Section(heading=None, body="")]

    if fmt in (FormatKind.MARKDOWN, FormatKind.MIXED, None):
        sections = _parse_markdown_sections(text)
        if sections:
            return sections

    # Paragraph split for plain/html/code/json
    paragraphs = re.split(r"\n\s*\n", text.strip())
    return [Section(heading=None, body=p.strip()) for p in paragraphs if p.strip()]


def _parse_markdown_sections(text: str) -> List[Section]:
    """Split text at markdown headings."""
    headings = list(_MD_HEADING_RE.finditer(text))
    if not headings:
        return []

    sections: List[Section] = []

    # Content before the first heading (if any)
    preamble = text[: headings[0].start()].strip()
    if preamble:
        sections.append(Section(heading=None, body=preamble))

    for i, match in enumerate(headings):
        level = len(match.group(1))
        heading_text = match.group(2).strip()
        body_start = match.end()
        body_end = headings[i + 1].start() if i + 1 < len(headings) else len(text)
        body = text[body_start:body_end].strip()
        sections.append(Section(heading=heading_text, body=body, level=level))

    return sections

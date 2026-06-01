"""Normalization pipeline — compose all stages into a single callable (§16)."""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional

from .format_detect import FormatKind, detect_format
from .guards import check_forbidden_phrases, check_length
from .section_parse import Section, parse_sections
from .text_cleanup import normalize_whitespace, remove_html_tags, strip_boilerplate


@dataclass
class NormalizeResult:
    """Result produced by the pipeline."""

    text: str
    """Cleaned, normalized text."""

    format: FormatKind = FormatKind.PLAIN
    """Detected dominant format."""

    sections: List[Section] = field(default_factory=list)
    """Parsed content sections."""

    guards_failed: Dict[str, Any] = field(default_factory=dict)
    """Guards that failed. Keys are guard names, values are details."""

    metadata: Dict[str, Any] = field(default_factory=dict)
    """Pipeline metadata (original length, stages applied, etc.)."""


@dataclass
class NormalizePipeline:
    """Configurable output normalization pipeline.

    Stages are applied in order:
    1. ``strip_boilerplate``
    2. ``remove_html_tags`` (when ``strip_html`` is True)
    3. ``normalize_whitespace``
    4. ``detect_format``
    5. ``parse_sections``
    6. Length guard
    7. Forbidden-phrase guard
    """

    strip_html: bool = False
    """Remove HTML tags before processing."""

    min_chars: int = 10
    """Minimum acceptable response length."""

    max_chars: int = 50_000
    """Maximum acceptable response length."""

    forbidden_phrases: List[str] = field(default_factory=list)
    """Phrases that must not appear in the output."""

    def normalize(self, text: str) -> NormalizeResult:
        """Run all pipeline stages on ``text`` and return a :class:`NormalizeResult`."""
        original_len = len(text)

        # Stage 1: strip boilerplate
        cleaned = strip_boilerplate(text)

        # Stage 2: optional HTML stripping
        if self.strip_html:
            cleaned = remove_html_tags(cleaned)

        # Stage 3: whitespace normalisation
        cleaned = normalize_whitespace(cleaned)

        # Stage 4: format detection
        fmt = detect_format(cleaned)

        # Stage 5: section parsing
        sections = parse_sections(cleaned, fmt)

        # Stage 6: length guard
        guards_failed: Dict[str, Any] = {}
        if not check_length(cleaned, self.min_chars, self.max_chars):
            guards_failed["length"] = {
                "actual": len(cleaned),
                "min": self.min_chars,
                "max": self.max_chars,
            }

        # Stage 7: forbidden-phrase guard
        matched = check_forbidden_phrases(cleaned, self.forbidden_phrases)
        if matched:
            guards_failed["forbidden_phrases"] = matched

        metadata = {
            "original_length": original_len,
            "cleaned_length": len(cleaned),
            "section_count": len(sections),
        }

        return NormalizeResult(
            text=cleaned,
            format=fmt,
            sections=sections,
            guards_failed=guards_failed,
            metadata=metadata,
        )

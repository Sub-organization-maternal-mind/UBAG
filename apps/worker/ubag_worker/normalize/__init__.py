"""UBAG output normalization pipeline (§16)."""

from .format_detect import FormatKind, detect_format
from .guards import check_forbidden_phrases, check_length
from .pipeline import NormalizePipeline, NormalizeResult
from .section_parse import Section, parse_sections
from .text_cleanup import normalize_whitespace, remove_html_tags, strip_boilerplate

__all__ = [
    "FormatKind",
    "NormalizePipeline",
    "NormalizeResult",
    "Section",
    "check_forbidden_phrases",
    "check_length",
    "detect_format",
    "normalize_whitespace",
    "parse_sections",
    "remove_html_tags",
    "strip_boilerplate",
]

"""Tests for the output normalization pipeline."""
import pytest

from ubag_worker.normalize import (
    FormatKind,
    NormalizePipeline,
    NormalizeResult,
    Section,
    check_forbidden_phrases,
    check_length,
    detect_format,
    normalize_whitespace,
    parse_sections,
    remove_html_tags,
    strip_boilerplate,
)


class TestTextCleanup:
    def test_strip_boilerplate_sure(self):
        text = "Sure! Here is the answer."
        assert not strip_boilerplate(text).startswith("Sure")

    def test_strip_boilerplate_as_an_ai(self):
        text = "As an AI language model, I cannot provide..."
        assert "As an AI" not in strip_boilerplate(text)

    def test_normalize_whitespace_collapses_spaces(self):
        assert normalize_whitespace("a  b   c") == "a b c"

    def test_normalize_whitespace_trims_blank_lines(self):
        result = normalize_whitespace("\n\nhello\n\n")
        assert result == "hello"

    def test_remove_html_tags(self):
        result = remove_html_tags("<p>Hello <b>world</b></p>")
        assert result == "Hello world"

    def test_remove_html_no_tags(self):
        assert remove_html_tags("plain text") == "plain text"


class TestFormatDetect:
    def test_plain(self):
        assert detect_format("Just some plain text here.") == FormatKind.PLAIN

    def test_markdown_headings(self):
        md = "# Heading\n\nSome **bold** text.\n\n- item one\n- item two"
        assert detect_format(md) == FormatKind.MARKDOWN

    def test_html(self):
        html = "<html><body><p>Content</p></body></html>"
        assert detect_format(html) == FormatKind.HTML

    def test_json(self):
        assert detect_format('{"key": "value", "num": 42}') == FormatKind.JSON

    def test_code_fence(self):
        code = "```python\nprint('hello')\n```"
        assert detect_format(code) == FormatKind.CODE

    def test_empty_returns_plain(self):
        assert detect_format("") == FormatKind.PLAIN


class TestSectionParse:
    def test_markdown_sections(self):
        md = "# First\n\nBody one.\n\n## Second\n\nBody two."
        sections = parse_sections(md, FormatKind.MARKDOWN)
        assert len(sections) == 2
        assert sections[0].heading == "First"
        assert "Body one" in sections[0].body
        assert sections[1].heading == "Second"
        assert sections[1].level == 2

    def test_plain_paragraph_split(self):
        text = "Para one.\n\nPara two.\n\nPara three."
        sections = parse_sections(text, FormatKind.PLAIN)
        assert len(sections) == 3
        assert all(s.heading is None for s in sections)

    def test_empty_text_returns_one_section(self):
        sections = parse_sections("")
        assert len(sections) == 1
        assert sections[0].body == ""

    def test_no_headings_returns_paragraph_sections(self):
        text = "Just some text without headings."
        sections = parse_sections(text, FormatKind.MARKDOWN)
        assert len(sections) >= 1


class TestGuards:
    def test_check_length_pass(self):
        assert check_length("hello world", min_chars=5, max_chars=100)

    def test_check_length_too_short(self):
        assert not check_length("hi", min_chars=5)

    def test_check_length_too_long(self):
        assert not check_length("x" * 1001, max_chars=1000)

    def test_forbidden_phrases_found(self):
        matched = check_forbidden_phrases("Do not click here", ["click here", "buy now"])
        assert "click here" in matched
        assert "buy now" not in matched

    def test_forbidden_phrases_none_found(self):
        assert check_forbidden_phrases("safe content", ["forbidden"]) == []

    def test_forbidden_phrases_case_insensitive(self):
        assert check_forbidden_phrases("CLICK HERE", ["click here"]) == ["click here"]


class TestNormalizePipeline:
    def test_simple_normalize(self):
        pipeline = NormalizePipeline()
        result = pipeline.normalize("Sure! Here is the answer to your question.")
        assert isinstance(result, NormalizeResult)
        assert "Sure" not in result.text
        assert result.format in list(FormatKind)
        assert len(result.sections) >= 1

    def test_length_guard_triggers(self):
        pipeline = NormalizePipeline(min_chars=100)
        result = pipeline.normalize("short text")
        assert "length" in result.guards_failed

    def test_forbidden_phrase_guard_triggers(self):
        pipeline = NormalizePipeline(forbidden_phrases=["forbidden phrase"])
        result = pipeline.normalize("This contains a forbidden phrase that must be caught.")
        assert "forbidden_phrases" in result.guards_failed

    def test_html_stripping(self):
        pipeline = NormalizePipeline(strip_html=True)
        result = pipeline.normalize("<p>Hello <b>world</b></p>")
        assert "<p>" not in result.text
        assert "Hello" in result.text

    def test_metadata_populated(self):
        pipeline = NormalizePipeline()
        result = pipeline.normalize("Test text with content.")
        assert "original_length" in result.metadata
        assert "section_count" in result.metadata

    def test_markdown_sections_detected(self):
        md = "# Section A\n\nContent A.\n\n# Section B\n\nContent B."
        pipeline = NormalizePipeline()
        result = pipeline.normalize(md)
        assert result.format == FormatKind.MARKDOWN
        assert len(result.sections) >= 2

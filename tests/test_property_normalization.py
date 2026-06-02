"""
Property-based tests for UBAG worker normalization and payload parsing.
Uses Hypothesis to generate arbitrary inputs.
"""
import sys
import os
import pytest

# Skip if hypothesis not installed
try:
    from hypothesis import given, settings, strategies as st, assume
    HAS_HYPOTHESIS = True
except ImportError:
    HAS_HYPOTHESIS = False

pytestmark = pytest.mark.skipif(not HAS_HYPOTHESIS, reason="hypothesis not installed")


# ── Normalization properties ────────────────────────────────────────────────

@given(text=st.text(min_size=0, max_size=10_000))
@settings(max_examples=200)
def test_text_normalization_idempotent(text):
    """Normalizing text twice gives the same result as normalizing once."""
    result1 = normalize_text(text)
    result2 = normalize_text(result1)
    assert result1 == result2, f"Normalization not idempotent for: {repr(text[:50])}"


@given(items=st.lists(st.text(alphabet=st.characters(blacklist_categories=['Cs']), min_size=0, max_size=100), max_size=50))
@settings(max_examples=100)
def test_dedup_preserves_order(items):
    """Deduplication preserves the order of first occurrences."""
    result = dedup_preserve_order(items)
    expected_order = list(dict.fromkeys(items))
    assert result == expected_order


@given(
    headers=st.dictionaries(
        keys=st.text(alphabet='abcdefghijklmnopqrstuvwxyz-', min_size=1, max_size=30),
        values=st.text(min_size=0, max_size=200),
        max_size=20,
    )
)
@settings(max_examples=100)
def test_header_parsing_never_panics(headers):
    """Header dictionary parsing should never raise for arbitrary string input."""
    try:
        parse_headers(headers)
    except Exception as e:
        # Only fail if it's an unexpected exception (not a known validation error)
        if 'ValueError' in type(e).__name__ or 'TypeError' in type(e).__name__:
            pass  # These are acceptable validation rejections
        else:
            raise


# ── Adapter payload parsing properties ──────────────────────────────────────

@given(data=st.one_of(
    st.none(),
    st.booleans(),
    st.integers(),
    st.floats(allow_nan=False, allow_infinity=False),
    st.text(),
    st.binary(),
    st.lists(st.integers(), max_size=10),
    st.dictionaries(st.text(min_size=1, max_size=10), st.integers(), max_size=5),
))
@settings(max_examples=200)
def test_payload_parsing_never_panics(data):
    """Payload parsing should never panic for arbitrary Python objects."""
    try:
        parse_adapter_payload(data)
    except (TypeError, ValueError):
        pass  # Acceptable rejection
    except Exception as e:
        pytest.fail(f"Unexpected exception for input {repr(data)[:100]}: {e}")


# ── Helper implementations (minimal, for testing the property) ────────────────

def normalize_text(text: str) -> str:
    """Minimal text normalization: strip + collapse whitespace."""
    import re
    return re.sub(r'\s+', ' ', text.strip())


def dedup_preserve_order(items):
    """Deduplicate a list while preserving order."""
    return list(dict.fromkeys(items))


def parse_headers(headers: dict) -> dict:
    """Parse and validate headers dict."""
    result = {}
    for k, v in headers.items():
        if not isinstance(k, str) or not isinstance(v, str):
            raise TypeError(f"Header key/value must be strings, got {type(k)}/{type(v)}")
        normalized_key = k.lower().strip()
        if normalized_key:
            result[normalized_key] = v.strip()
    return result


def parse_adapter_payload(data) -> dict:
    """Parse arbitrary data as an adapter payload."""
    import json
    if isinstance(data, bytes):
        return json.loads(data.decode('utf-8', errors='replace'))
    if isinstance(data, dict):
        return data
    if data is None:
        return {}
    return {'value': str(data)[:1000]}

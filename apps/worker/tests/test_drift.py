"""Tests for the DOM drift detector."""
import pytest

from ubag_worker.drift.detector import (
    DriftSignal,
    build_baseline,
    detect_drift,
)


class TestBuildBaseline:
    def test_creates_baseline_with_hashed_values(self):
        baseline = build_baseline(
            "chatgpt_web", "0.3.0",
            {"prompt_input": "1 textarea", "submit_btn": "1 button"},
            captured_at="2026-01-01T00:00:00Z",
        )
        assert baseline.provider_id == "chatgpt_web"
        assert baseline.version == "0.3.0"
        assert "prompt_input" in baseline.selector_hashes
        # Hashes are 16-char hex
        for h in baseline.selector_hashes.values():
            assert len(h) == 16

    def test_same_value_produces_same_hash(self):
        b1 = build_baseline("p", "1", {"k": "v"})
        b2 = build_baseline("p", "1", {"k": "v"})
        assert b1.selector_hashes == b2.selector_hashes

    def test_different_values_produce_different_hashes(self):
        b1 = build_baseline("p", "1", {"k": "v1"})
        b2 = build_baseline("p", "1", {"k": "v2"})
        assert b1.selector_hashes["k"] != b2.selector_hashes["k"]


class TestNoDrift:
    def test_identical_selectors_produces_no_drift(self):
        selectors = {"prompt": "1 textarea", "submit": "1 button", "output": "1 div"}
        baseline = build_baseline("chatgpt_web", "1.0", selectors)
        report = detect_drift(selectors, baseline)
        assert not report.has_drift
        assert report.severity == 0.0
        assert not report.is_critical


class TestSelectorGone:
    def test_missing_selector_flagged(self):
        baseline = build_baseline("claude_web", "1.0", {"prompt": "textarea", "submit": "button"})
        live = {"prompt": "textarea"}  # submit gone
        report = detect_drift(live, baseline)
        assert DriftSignal.SELECTOR_GONE in report.signals
        assert report.has_drift
        assert report.severity == pytest.approx(0.5, abs=0.01)

    def test_all_selectors_gone_max_severity(self):
        baseline = build_baseline("p", "1", {"a": "x", "b": "y"})
        report = detect_drift({}, baseline)
        assert report.severity == 1.0
        assert report.is_critical


class TestSelectorChanged:
    def test_changed_value_flagged(self):
        baseline = build_baseline("deepseek_web", "1.0", {"submit": "1 button"})
        live = {"submit": "2 buttons"}  # text changed
        report = detect_drift(live, baseline)
        assert DriftSignal.SELECTOR_CHANGED in report.signals
        assert report.has_drift

    def test_layout_shift_when_many_changed(self):
        # Change 4 out of 4 selectors (100% > 30% threshold)
        selectors = {f"sel{i}": f"v{i}" for i in range(4)}
        baseline = build_baseline("p", "1", selectors)
        live = {f"sel{i}": f"changed{i}" for i in range(4)}
        report = detect_drift(live, baseline)
        assert DriftSignal.LAYOUT_SHIFT in report.signals


class TestNewElement:
    def test_new_selector_flagged_but_not_in_severity(self):
        baseline = build_baseline("p", "1", {"a": "x"})
        live = {"a": "x", "b": "y (new)"}
        report = detect_drift(live, baseline)
        assert DriftSignal.NEW_ELEMENT in report.signals
        # 'a' is unchanged, 'b' is new → no severity contribution
        assert report.severity == 0.0
        assert not report.is_critical


class TestReportProperties:
    def test_has_drift_false_when_clean(self):
        baseline = build_baseline("p", "1", {"k": "v"})
        report = detect_drift({"k": "v"}, baseline)
        assert not report.has_drift

    def test_provider_and_version_in_report(self):
        baseline = build_baseline("gemini_web", "2.1", {"a": "x"})
        report = detect_drift({"a": "x"}, baseline)
        assert report.provider_id == "gemini_web"
        assert report.baseline_version == "2.1"

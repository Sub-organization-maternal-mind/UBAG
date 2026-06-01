"""Tests for chaos experiment schema validation."""
from __future__ import annotations

import json
import os
import sys
import tempfile

# Ensure harness package is importable when running from repo root
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", ".."))

import pytest
from chaos.harness.schema import validate_experiment, load_and_validate


def _valid_experiment() -> dict:
    return {
        "title": "Test experiment",
        "description": "A test chaos experiment",
        "steady-states": {
            "before": {"probes": []},
            "after": {"probes": []},
        },
        "method": [{"type": "action", "name": "inject-fault"}],
    }


# ---------------------------------------------------------------------------
# validate_experiment
# ---------------------------------------------------------------------------

def test_valid_experiment_no_errors():
    errors = validate_experiment(_valid_experiment())
    assert errors == []


def test_missing_method():
    exp = _valid_experiment()
    del exp["method"]
    errors = validate_experiment(exp)
    assert any("missing required field: 'method'" in e for e in errors)


def test_missing_steady_states_before():
    exp = _valid_experiment()
    del exp["steady-states"]["before"]
    errors = validate_experiment(exp)
    assert any("missing 'before'" in e for e in errors)


def test_method_empty_list():
    exp = _valid_experiment()
    exp["method"] = []
    errors = validate_experiment(exp)
    assert any("non-empty" in e for e in errors)


def test_method_not_a_list():
    exp = _valid_experiment()
    exp["method"] = "not-a-list"
    errors = validate_experiment(exp)
    assert any("must be a list" in e for e in errors)


# ---------------------------------------------------------------------------
# load_and_validate
# ---------------------------------------------------------------------------

def test_load_and_validate_valid_file():
    exp = _valid_experiment()
    with tempfile.NamedTemporaryFile(
        mode="w", suffix=".json", delete=False, encoding="utf-8"
    ) as f:
        json.dump(exp, f)
        path = f.name
    try:
        loaded, errors = load_and_validate(path)
        assert errors == []
        assert loaded["title"] == exp["title"]
    finally:
        os.unlink(path)


def test_load_and_validate_malformed_experiment():
    # Missing required fields
    exp = {"title": "incomplete"}
    with tempfile.NamedTemporaryFile(
        mode="w", suffix=".json", delete=False, encoding="utf-8"
    ) as f:
        json.dump(exp, f)
        path = f.name
    try:
        _, errors = load_and_validate(path)
        assert len(errors) > 0
    finally:
        os.unlink(path)

"""Chaos experiment schema validation."""
from __future__ import annotations

import json
from typing import Any


# Minimal schema for a chaos-toolkit experiment
EXPERIMENT_REQUIRED_FIELDS = {"title", "description", "steady-states", "method"}


def validate_experiment(experiment: dict[str, Any]) -> list[str]:
    """Validate an experiment dict against the minimal chaos-toolkit schema.

    Returns a list of error strings (empty = valid).
    """
    errors = []
    for field in EXPERIMENT_REQUIRED_FIELDS:
        if field not in experiment:
            errors.append(f"missing required field: {field!r}")

    # steady-states must have before and after
    ss = experiment.get("steady-states")
    if isinstance(ss, dict):
        for phase in ("before", "after"):
            if phase not in ss:
                errors.append(f"steady-states missing {phase!r} phase")
    elif ss is not None:
        errors.append("steady-states must be a dict")

    # method must be a non-empty list
    method = experiment.get("method")
    if method is not None and not isinstance(method, list):
        errors.append("method must be a list")
    elif isinstance(method, list) and len(method) == 0:
        errors.append("method must be non-empty")

    return errors


def load_and_validate(path: str) -> tuple[dict[str, Any], list[str]]:
    """Load a JSON experiment file and validate it. Returns (experiment, errors)."""
    with open(path, encoding="utf-8") as f:
        experiment = json.load(f)
    errors = validate_experiment(experiment)
    return experiment, errors

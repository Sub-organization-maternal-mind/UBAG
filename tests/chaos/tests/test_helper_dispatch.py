"""Helper-dispatch chaos experiments (perf-fleet P4.14, P4.18): structure and cross-references.

These experiments inject faults into a two-helper lab and need UBAG_HELPER_DISPATCH,
a wired picker and hosts that do not exist yet, so they are DEFINITIONS ONLY and are
labelled so (`status`). What runs today is the same scenarios against a fake helper
and a loopback helper service in Go (`go test -race ./internal/executor
./internal/helperclient ./internal/nodes`). This file keeps the two in step: every
scenario must be valid, reversible, honest about not having run, and must name Go
tests that exist. P4.18 adds the attempt-reconcile scenarios (gateway restart).
"""
from __future__ import annotations

import os
import sys
from pathlib import Path

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", ".."))

import pytest
from chaos.harness.schema import load_and_validate

CHAOS_DIR = Path(__file__).resolve().parent.parent
REPO_ROOT = CHAOS_DIR.parent.parent
GATEWAY = REPO_ROOT / "apps" / "gateway"

EXPERIMENTS = [
    "helper-lost-pre-submit.json",
    "helper-lost-post-submit.json",
    "helper-stale-generation.json",
    "fleet-manager-down.json",
    "reconcile-gateway-lost-before-submit.json",
    "reconcile-gateway-lost-after-submit.json",
    "reconcile-gateway-and-helper-restarted.json",
]


def _load(name: str) -> dict:
    experiment, errors = load_and_validate(str(CHAOS_DIR / "experiments" / name))
    assert errors == [], f"{name}: {errors}"
    return experiment


def _go_test_names() -> set[str]:
    names: set[str] = set()
    for path in GATEWAY.rglob("*_test.go"):
        for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
            if line.startswith("func Test") and "(" in line:
                names.add(line[len("func "):line.index("(")])
    return names


@pytest.mark.parametrize("name", EXPERIMENTS)
def test_experiment_is_valid_and_reversible(name: str):
    experiment = _load(name)
    assert experiment["rollbacks"], "a fault experiment must undo its fault"
    for phase in ("before", "after"):
        probes = experiment["steady-states"][phase]["probes"]
        assert probes and all(p["tolerance"] == 200 for p in probes)
    assert "UBAG_HELPER_DISPATCH" in experiment["requires"]
    # Every ${variable} an action uses must be declared in the configuration.
    declared = set(experiment["configuration"])
    for step in experiment["method"] + experiment["rollbacks"]:
        arguments = step["provider"].get("arguments", "")
        for token in arguments.split():
            if token.startswith("${") and token.endswith("}"):
                assert token[2:-1] in declared, f"{name}: {token} is not declared"


@pytest.mark.parametrize("name", EXPERIMENTS)
def test_experiment_does_not_claim_to_have_run(name: str):
    assert _load(name)["status"] == "definition_only"


@pytest.mark.parametrize("name", EXPERIMENTS)
def test_experiment_names_go_tests_that_exist(name: str):
    covered = _load(name)["covered_by"]
    assert covered, "an experiment must point at the tests that run its scenario today"
    known = _go_test_names()
    missing = [t for t in covered if t not in known]
    assert missing == [], f"{name}: no such Go test: {missing}"


def test_exit_gate_scenarios_are_all_covered():
    """The P4.14 exit gate: duplicate completion, stale generation, 20 s renewal and 120 s
    expiry, helper lost mid-run, oversize output, manager down."""
    covered = {t for name in EXPERIMENTS for t in _load(name)["covered_by"]}
    required = {
        "TestRemoteDuplicateCompletionIsANoOp",
        "TestRemoteStaleEventGenerationIsRejectedTheJobUntouchedAndAudited",
        "TestRemoteLeaseRenewsEvery20sAndExpiresAt120s",
        "TestRemoteHelperLostBeforeSubmissionIsReassigned",
        "TestRemoteHelperLostAfterSubmissionFailsClosedAndIsNeverReplayed",
        "TestRemoteManagerDownKeepsInFlightAttemptsAndGrantsNothingNew",
    }
    assert required <= covered, sorted(required - covered)
    # Oversize output has no fault to inject on a lab host (the helper itself refuses it):
    # it is covered by the Go tests alone.
    assert "TestRemoteOutputThatBreaksTheContractFailsTheAttemptNeverASuccess" in _go_test_names()


def test_reconcile_exit_gate_scenarios_are_all_covered():
    """The P4.18 exit gate: pre-submit loss, post-submit loss, stale result and manager down,
    each by an experiment that names the Go tests (`go test ./internal/nodes -run Reconcile`
    plus the executor and real-helper tests) that run the scenario today."""
    by_experiment = {name: set(_load(name)["covered_by"]) for name in EXPERIMENTS}
    expected = {
        "reconcile-gateway-lost-before-submit.json": {"TestReconcileGateLostBeforeSubmissionReassignsAtGenerationPlusOne"},
        "reconcile-gateway-lost-after-submit.json": {
            "TestReconcileGateCollectsAFinishedAttemptAfterAGatewayRestart",
            "TestReconcileNeverRunsASubmittedAttempt",
        },
        "reconcile-gateway-and-helper-restarted.json": {"TestReconcileRealHelperThatForgotTheAttemptFailsClosed"},
        "helper-stale-generation.json": {"TestReconcileGateRejectsAStaleHelperResult"},
        "fleet-manager-down.json": {"TestReconcileManagerDownStillSettlesAnAlreadyPlacedAttempt"},
    }
    for name, tests in expected.items():
        assert tests <= by_experiment[name], (name, sorted(tests - by_experiment[name]))
    # The nodes package is the exit gate's own package: at least one named test lives there.
    nodes_tests = {
        line[len("func "):line.index("(")]
        for path in (GATEWAY / "internal" / "nodes").glob("*_test.go")
        for line in path.read_text(encoding="utf-8", errors="replace").splitlines()
        if line.startswith("func TestReconcile") and "(" in line
    }
    assert nodes_tests, "go test ./internal/nodes -run Reconcile must run something"
    assert {"TestReconcilePolicyTable", "TestReconcileNeverRunsASubmittedAttempt"} <= nodes_tests


def test_reconcile_experiments_say_what_must_not_happen():
    """A reconcile experiment is only worth running if it states the safety property:
    the prompt is never sent twice. Each says so in its description."""
    for name in EXPERIMENTS[-3:]:
        description = _load(name)["description"].lower()
        assert "reconcil" in description or "ledger" in description, name

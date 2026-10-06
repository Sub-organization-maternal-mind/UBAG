"""Helper isolation acceptance (perf-fleet P7.3): the Python side.

What runs by default (stdlib + pytest, no Docker, no fault injection):

  * the env-dump scanner, checked against the helper's OWN forbidden-environment
    lists (parsed from internal/helper/config.go so the two cannot drift);
  * a consistency check that the Go isolation tests the plan names exist, climb
    the 1/2/5/10/20 workload ladder, and that the workflow that runs them against
    two containerised helpers can only be dispatched by hand.

What needs two helper containers (skipped otherwise; the manually dispatched
workflow .github/workflows/helper-isolation.yml provides them):

  UBAG_CHAOS_HELPER_CONTAINERS=name_a,name_b  scan every process environment inside
                                              each running helper container;
  UBAG_CHAOS_HELPER_IMAGE=<image>             a helper started with a primary secret
  UBAG_CHAOS_HELPER_BIN=<path to ubag-helper> in its environment must refuse to start
                                              and must not echo the value.

The Go side (cross-tenant events, duplicate results, killed or partitioned helpers
and stale commits at 1/2/5/10/20 workloads, plus the worker-process env dump) is in
apps/gateway/internal/helperclient and apps/gateway/internal/helper/runner.
"""
from __future__ import annotations

import os
import re
import shutil
import subprocess
import sys
from pathlib import Path
from typing import List

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", ".."))

import pytest

CHAOS_DIR = Path(__file__).resolve().parent.parent
REPO_ROOT = CHAOS_DIR.parent.parent
GATEWAY = REPO_ROOT / "apps" / "gateway"
HELPER_CONFIG = GATEWAY / "internal" / "helper" / "config.go"
WORKFLOW = REPO_ROOT / ".github" / "workflows" / "helper-isolation.yml"

LADDER = "1, 2, 5, 10, 20"

REQUIRED_GO_TESTS = {
    "helperclient/isolation_test.go": [
        "TestIsolationNoCrossTenantLeakAndNoDuplicateResultsUnderLoad",
        "TestIsolationHealedPartitionCommitsEveryEventExactlyOnce",
        "TestIsolationLostHelperNeverProducesAStaleCommit",
    ],
    "helperclient/isolation_containers_test.go": [
        "TestIsolationWriteFixtures",
        "TestIsolationOnContainerisedHelpers",
    ],
    "helper/runner/isolation_test.go": [
        "TestIsolationWorkerProcessesCarryNoPrimarySecrets",
    ],
}


# ---- the scanner ---------------------------------------------------------------


def _go_string_list(name: str) -> List[str]:
    """The string literals of `name = []string{...}` in internal/helper/config.go."""
    text = HELPER_CONFIG.read_text(encoding="utf-8")
    match = re.search(name + r"\s*=\s*\[\]string\{(.*?)\n?\t?\}", text, re.S)
    assert match, f"{name} not found in {HELPER_CONFIG.name}"
    return re.findall(r'"([^"]+)"', match.group(1))


FORBIDDEN_NAMES = _go_string_list("forbiddenEnv")
FORBIDDEN_PREFIXES = _go_string_list("forbiddenEnvPrefixes")
FORBIDDEN_SUFFIXES = _go_string_list("forbiddenEnvSuffixes")


def forbidden(key: str) -> bool:
    """Same rule as helper.CheckEnvIsolation: UBAG_HELPER_* is the helper's own."""
    if key.startswith("UBAG_HELPER_"):
        return False
    return (
        key in FORBIDDEN_NAMES
        or any(key.startswith(p) for p in FORBIDDEN_PREFIXES)
        or (key.startswith("UBAG_") and any(key.endswith(s) for s in FORBIDDEN_SUFFIXES))
    )


def scan_environ(blob: bytes) -> List[str]:
    """Names (never values) of primary-only variables with a non-empty value in a
    NUL-separated environment dump (the format of /proc/<pid>/environ). Dumps of
    several processes may simply be concatenated."""
    bad = set()
    for entry in re.split(rb"[\x00\n]", blob):
        key, sep, value = entry.partition(b"=")
        if sep and value and forbidden(key.decode("utf-8", "replace")):
            bad.add(key.decode("utf-8", "replace"))
    return sorted(bad)


def test_the_helpers_forbidden_lists_were_parsed():
    assert "UBAG_APP_SECRET" in FORBIDDEN_NAMES and "PGPASSWORD" in FORBIDDEN_NAMES
    assert "UBAG_NATS_" in FORBIDDEN_PREFIXES
    assert "_TOKEN" in FORBIDDEN_SUFFIXES


@pytest.mark.parametrize("key", FORBIDDEN_NAMES + [p + "X" for p in FORBIDDEN_PREFIXES] + ["UBAG_ANY" + s for s in FORBIDDEN_SUFFIXES])
def test_scanner_flags_every_forbidden_class(key: str):
    assert scan_environ(f"PATH=/usr/bin\x00{key}=s3cret\x00".encode()) == [key]


def test_scanner_names_variables_never_values():
    assert "s3cret" not in " ".join(scan_environ(b"UBAG_APP_SECRET=s3cret\x00"))


def test_scanner_ignores_the_helpers_own_and_allowlisted_configuration():
    ok = (
        b"PATH=/usr/bin\x00HOME=/var/lib/ubag-helper/home\x00UBAG_HELPER_NODE_ID=iso-a\x00"
        b"UBAG_HELPER_TLS_KEY_FILE=/etc/ubag-helper/tls/node.key\x00UBAG_HELPER_PRIMARY_URI_SAN=spiffe://ubag/primary/p\x00"
        b"UBAG_BROWSER_ENGINE=chromium\x00UBAG_HELPER_PLANE=1\x00UBAG_APP_SECRET=\x00"
    )
    assert scan_environ(ok) == []


def test_scanner_reads_concatenated_process_dumps():
    dump = b"PATH=/bin\x00\nUBAG_NATS_URL=nats://x\x00\nUBAG_HELPER_NODE_ID=a\x00\nDATABASE_URL=postgres://x\x00"
    assert scan_environ(dump) == ["DATABASE_URL", "UBAG_NATS_URL"]


# ---- consistency with the Go suites and the workflow -----------------------------


def _go_funcs(rel: str) -> str:
    return (GATEWAY / "internal" / rel).read_text(encoding="utf-8")


@pytest.mark.parametrize("rel,names", sorted(REQUIRED_GO_TESTS.items()))
def test_the_go_isolation_tests_exist(rel: str, names: List[str]):
    text = _go_funcs(rel)
    missing = [n for n in names if not re.search(r"^func " + n + r"\(", text, re.M)]
    assert missing == [], f"{rel}: {missing}"


@pytest.mark.parametrize("rel", ["helperclient/isolation_test.go", "helper/runner/isolation_test.go"])
def test_the_go_ladder_is_1_2_5_10_20(rel: str):
    assert "return []int{" + LADDER + "}" in _go_funcs(rel)


def test_the_workflow_can_only_be_dispatched_by_hand():
    text = WORKFLOW.read_text(encoding="utf-8")
    triggers = text.split("\non:", 1)[1].split("\npermissions:", 1)[0]
    assert "workflow_dispatch" in triggers
    for forbidden_trigger in ("push:", "pull_request", "schedule:", "workflow_run", "pull_request_target"):
        assert forbidden_trigger not in triggers, forbidden_trigger
    assert re.search(r"^permissions:\s*\n\s+contents:\s*read\s*$", text, re.M), "the token must be read-only"
    for name in ("TestIsolationWriteFixtures", "TestIsolationOnContainerisedHelpers"):
        assert name in text, f"the workflow does not run {name}"
    assert "test_helper_isolation.py" in text


def test_the_workflow_starts_exactly_two_helper_containers():
    text = WORKFLOW.read_text(encoding="utf-8")
    assert len(re.findall(r"docker run -d", text)) == 2
    assert "UBAG_CHAOS_HELPER_CONTAINERS" in text


# ---- two containerised helpers (workflow) -----------------------------------------


def _docker(*args: str, stdin: bytes = b"") -> subprocess.CompletedProcess:
    return subprocess.run(["docker", *args], input=stdin, capture_output=True, timeout=120)


def _containers() -> List[str]:
    return [c for c in os.environ.get("UBAG_CHAOS_HELPER_CONTAINERS", "").split(",") if c.strip()]


needs_containers = pytest.mark.skipif(
    len(_containers()) < 2 or shutil.which("docker") is None,
    reason="UBAG_CHAOS_HELPER_CONTAINERS must name two running helper containers (and docker must be on PATH)",
)


def _all_process_environments(container: str) -> bytes:
    """Every readable /proc/<pid>/environ of the container (the helper and its workers)."""
    script = 'for f in /proc/[0-9]*/environ; do cat "$f" 2>/dev/null; printf "\\n"; done'
    done = _docker("exec", container, "sh", "-c", script)
    assert done.returncode == 0, done.stderr.decode("utf-8", "replace")
    return done.stdout


def _pid1_environment(container: str) -> bytes:
    done = _docker("exec", container, "sh", "-c", "cat /proc/1/environ")
    assert done.returncode == 0, done.stderr.decode("utf-8", "replace")
    return done.stdout


@needs_containers
@pytest.mark.parametrize("index", [0, 1])
def test_a_helper_container_holds_no_primary_secret_in_any_process(index: int):
    container = _containers()[index]
    dump = _all_process_environments(container)
    # The scan is not vacuous: the helper's own configuration is in the dump.
    assert b"UBAG_HELPER_NODE_ID=" in dump, "no helper process environment was readable"
    assert scan_environ(dump) == [], f"{container} carries primary-only configuration"


@needs_containers
def test_the_two_helpers_are_distinct_nodes_with_their_own_profiles():
    ids = []
    for container in _containers()[:2]:
        match = re.search(rb"UBAG_HELPER_NODE_ID=([^\x00\n]+)", _pid1_environment(container))
        assert match, container
        ids.append(match.group(1))
    assert ids[0] != ids[1], "two helpers must not share a node identity"
    sources = [set(_docker("inspect", "-f", "{{range .Mounts}}{{.Source}} {{end}}", c).stdout.split()) for c in _containers()[:2]]
    assert not (sources[0] & sources[1]), "the helpers share a bind mount or volume"


def _helper_command() -> List[str]:
    image = os.environ.get("UBAG_CHAOS_HELPER_IMAGE")
    if image and shutil.which("docker"):
        return ["docker", "run", "--rm", "-e", "UBAG_APP_SECRET=decoy-primary-secret", image]
    binary = os.environ.get("UBAG_CHAOS_HELPER_BIN")
    return [binary] if binary else []


@pytest.mark.skipif(not _helper_command(), reason="UBAG_CHAOS_HELPER_IMAGE (docker) or UBAG_CHAOS_HELPER_BIN is not set")
def test_a_helper_started_with_a_primary_secret_refuses_and_never_echoes_it():
    command = _helper_command()
    env = {k: v for k, v in os.environ.items() if not forbidden(k)}
    env["UBAG_APP_SECRET"] = "decoy-primary-secret"
    done = subprocess.run(command, capture_output=True, timeout=60, env=env)
    output = (done.stdout + done.stderr).decode("utf-8", "replace")
    assert done.returncode != 0, "the helper started with a primary secret in its environment"
    assert "primary-only configuration" in output and "UBAG_APP_SECRET" in output
    assert "decoy-primary-secret" not in output, "the helper echoed the secret value"

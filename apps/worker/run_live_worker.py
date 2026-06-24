"""Entrypoint for the UBAG live browser-automation worker.

The Go worker-consumer invokes this script as:

    python /app/apps/worker/run_live_worker.py --input -

Routing logic
-------------
- If ``payload["job"]["target"]`` (or ``payload["target"]`` as fallback) is found
  in ``PROVIDER_SELECTORS`` (e.g. ``"chatgpt_web"``) the job is driven through
  :class:`LiveSessionEngine`, which internally calls ``create_default_driver()``
  → ``PlaywrightPageDriver`` → CDP attach via ``UBAG_REMOTE_BROWSER_ENDPOINT``.
- Otherwise (e.g. ``target == "mock"`` or any unknown target) the job is
  routed through ``runner.emit_jsonl()`` which dispatches via the adapter
  registry → mock adapter.

This is a single-job script (not a long-running daemon).
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import List, Optional

# ---------------------------------------------------------------------------
# sys.path bootstrap — must happen before any ubag_worker imports
# ---------------------------------------------------------------------------

_WORKER_DIR = Path(__file__).resolve().parent
if str(_WORKER_DIR) not in sys.path:
    sys.path.insert(0, str(_WORKER_DIR))

# The adapter_registry auto-discovers adapters via
#   _ADAPTERS_ROOT = Path(__file__).resolve().parents[3] / "adapters"
# which resolves relative to adapter_registry.py, so no additional path
# entry is needed for the adapters package.

from ubag_worker.live.engine import LiveSessionEngine  # noqa: E402
from ubag_worker.live.selectors import PROVIDER_SELECTORS  # noqa: E402
from ubag_worker.runner import emit_jsonl, load_payload_from_text  # noqa: E402
from ubag_worker.runtime.shutdown import GracefulDrainer, install_shutdown_handler  # noqa: E402

# ---------------------------------------------------------------------------
# Graceful shutdown — mirrors run_mock_worker.py pattern
# ---------------------------------------------------------------------------

class _LiveOrchestrator:
    """Minimal stub satisfying OrchestratorProtocol for the single-job live worker.

    The live worker drives one job per process invocation; there are no
    long-lived in-flight jobs tracked at the process level, so drain completes
    immediately.
    """

    def all_inflight_job_ids(self) -> list:
        return []

    def set_accepting(self, accepting: bool) -> None:
        pass


_drainer = GracefulDrainer(orchestrator=_LiveOrchestrator(), grace_window=5.0)
install_shutdown_handler(_drainer)


# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="ubag-live-worker",
        description="Emit UBAG live worker events as JSONL.",
    )
    source = parser.add_mutually_exclusive_group()
    source.add_argument(
        "--payload",
        help="Inline JSON job payload.",
    )
    source.add_argument(
        "--input",
        "-i",
        help="Path to a JSON job payload file. Use '-' to read stdin.",
    )
    parser.add_argument(
        "--output",
        "-o",
        help="Path to write JSONL events. Defaults to stdout.",
    )
    return parser


def _read_payload_text(
    inline_payload: Optional[str],
    input_path: Optional[str],
    parser: argparse.ArgumentParser,
) -> str:
    if inline_payload is not None:
        return inline_payload

    if input_path is None:
        if sys.stdin.isatty():
            parser.error("provide --payload, --input, or pipe JSON to stdin")
        return sys.stdin.read()

    if input_path == "-":
        return sys.stdin.read()

    return Path(input_path).read_text(encoding="utf-8")


# ---------------------------------------------------------------------------
# JSONL emission helpers
# ---------------------------------------------------------------------------

def _dump_event(event: object) -> str:
    return json.dumps(event, sort_keys=True, separators=(",", ":"), ensure_ascii=True)


def _target_from_payload(payload: object) -> str:
    """Extract the target adapter ID from a job payload.

    Checks ``payload["job"]["target"]`` first (the standard API envelope shape),
    then falls back to ``payload["target"]``, then defaults to ``"mock"``.
    Mirrors adapter_registry._target_from_payload.
    """
    if not isinstance(payload, dict):
        return "mock"
    job_field = payload.get("job", {})
    if not isinstance(job_field, dict):
        job_field = {}
    return str(job_field.get("target", payload.get("target", "mock")))


def _emit_live_jsonl(payload: object, stream) -> int:
    """Drive a live session and emit each event as a JSONL line."""
    target = _target_from_payload(payload)
    if target not in PROVIDER_SELECTORS:
        raise ValueError("no live selector configuration for target %r" % target)
    selectors = PROVIDER_SELECTORS[target]
    engine = LiveSessionEngine(selectors)
    count = 0
    for event in engine.iter_events(payload):
        stream.write(_dump_event(event))
        stream.write("\n")
        stream.flush()
        count += 1
    return count


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main(argv: Optional[List[str]] = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)

    try:
        payload_text = _read_payload_text(args.payload, args.input, parser)
        payload = load_payload_from_text(payload_text)

        target = _target_from_payload(payload)
        is_live = target in PROVIDER_SELECTORS

        if args.output:
            output_path = Path(args.output)
            with output_path.open("w", encoding="utf-8", newline="\n") as output:
                if is_live:
                    _emit_live_jsonl(payload, output)
                else:
                    emit_jsonl(payload, output)
        else:
            if is_live:
                _emit_live_jsonl(payload, sys.stdout)
            else:
                emit_jsonl(payload, sys.stdout)

    except Exception as exc:  # pragma: no cover - exercised through CLI behavior
        print("ubag-live-worker: %s" % exc, file=sys.stderr)
        return 2

    return 0


if __name__ == "__main__":
    raise SystemExit(main())

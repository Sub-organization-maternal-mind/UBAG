"""Command line entrypoint for the deterministic mock worker."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path
from typing import List, Optional

from .runner import emit_jsonl, load_payload_from_text


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="ubag-mock-worker",
        description="Emit deterministic UBAG mock worker events as JSONL.",
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


def main(argv: Optional[List[str]] = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    try:
        payload_text = _read_payload_text(args.payload, args.input, parser)
        payload = load_payload_from_text(payload_text)
        if args.output:
            output_path = Path(args.output)
            with output_path.open("w", encoding="utf-8", newline="\n") as output:
                emit_jsonl(payload, output)
        else:
            emit_jsonl(payload, sys.stdout)
    except Exception as exc:  # pragma: no cover - exercised through CLI behavior
        print("ubag-mock-worker: %s" % exc, file=sys.stderr)
        return 2

    return 0


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


if __name__ == "__main__":
    raise SystemExit(main())

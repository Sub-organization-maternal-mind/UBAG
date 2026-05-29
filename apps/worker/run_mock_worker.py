"""Convenience script for running the UBAG mock worker from the repo root."""

from __future__ import annotations

import sys
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[2]
for import_path in (REPO_ROOT / "apps" / "worker", REPO_ROOT / "adapters" / "mock"):
    sys.path.insert(0, str(import_path))

from ubag_worker.cli import main  # noqa: E402


if __name__ == "__main__":
    raise SystemExit(main())

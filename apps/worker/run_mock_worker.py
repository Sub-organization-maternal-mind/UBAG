"""Convenience script for running the UBAG mock worker from the repo root."""

from __future__ import annotations

import sys
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[2]
for import_path in (REPO_ROOT / "apps" / "worker", REPO_ROOT / "adapters" / "mock"):
    sys.path.insert(0, str(import_path))

from ubag_worker.cli import main  # noqa: E402
from ubag_worker.runtime.shutdown import GracefulDrainer, install_shutdown_handler  # noqa: E402


class _MockOrchestrator:
    """Minimal stub satisfying OrchestratorProtocol for the mock worker.
    No real tabs are active in the mock worker path, so all_inflight_job_ids
    always returns empty — drain completes immediately.
    """

    def concurrency_state(self, tenant_id: str = "") -> object:
        class _State:
            inflight = 0
        return _State()

    def all_inflight_job_ids(self) -> list:
        return []  # mock worker has no real in-flight jobs

    def set_accepting(self, accepting: bool) -> None:
        pass  # no-op for mock worker


_drainer = GracefulDrainer(orchestrator=_MockOrchestrator(), grace_window=5.0)
install_shutdown_handler(_drainer)


if __name__ == "__main__":
    raise SystemExit(main())

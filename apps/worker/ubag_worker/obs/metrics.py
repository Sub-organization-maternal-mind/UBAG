"""Worker metrics collector for the UBAG worker (§18 / Task 2.4).

Maintains in-process counters for ubag_worker_* metrics and can write them
to a Prometheus textfile-compatible output (for node_exporter textfile
collector, Pushgateway, or direct scraping).

No external dependencies required — pure stdlib.
"""
from __future__ import annotations

import os
import threading
from typing import Dict


class WorkerMetrics:
    """Thread-safe in-process counters for ubag_worker_* contract metrics."""

    def __init__(
        self,
        worker_pool: str = "local",
        adapter_family: str = "mock",
    ) -> None:
        self._lock = threading.Lock()
        self._pool = worker_pool
        self._family = adapter_family

        # Counters — monotonically increasing.
        self._jobs_processed: Dict[str, int] = {"success": 0, "failure": 0}
        self._result_ingestions: Dict[str, int] = {"success": 0, "failure": 0}
        self._artifact_captures: Dict[str, int] = {"success": 0, "failure": 0}

        # Duration sums (seconds).
        self._job_duration_sum: Dict[str, float] = {"success": 0.0, "failure": 0.0}
        self._ingestion_duration_sum: Dict[str, float] = {"success": 0.0}

    # ── Mutation helpers ──────────────────────────────────────────────────────

    def record_job(self, outcome: str, duration_seconds: float = 0.0) -> None:
        """Increment the job-processed counter and accumulate duration."""
        key = outcome if outcome in ("success", "failure") else "failure"
        with self._lock:
            self._jobs_processed[key] = self._jobs_processed.get(key, 0) + 1
            self._job_duration_sum[key] = self._job_duration_sum.get(key, 0.0) + duration_seconds

    def record_ingestion(self, outcome: str, duration_seconds: float = 0.0) -> None:
        """Increment the result-ingestion counter."""
        key = outcome if outcome in ("success", "failure") else "failure"
        with self._lock:
            self._result_ingestions[key] = self._result_ingestions.get(key, 0) + 1
            if key == "success":
                self._ingestion_duration_sum["success"] = (
                    self._ingestion_duration_sum.get("success", 0.0) + duration_seconds
                )

    def record_artifact(self, outcome: str = "success") -> None:
        """Increment the artifact-capture counter."""
        key = outcome if outcome in ("success", "failure") else "failure"
        with self._lock:
            self._artifact_captures[key] = self._artifact_captures.get(key, 0) + 1

    # ── Snapshot ──────────────────────────────────────────────────────────────

    def snapshot(self) -> Dict[str, object]:
        """Return a point-in-time snapshot of all counters."""
        with self._lock:
            return {
                "jobs_processed": dict(self._jobs_processed),
                "job_duration_sum": dict(self._job_duration_sum),
                "result_ingestions": dict(self._result_ingestions),
                "ingestion_duration_sum": dict(self._ingestion_duration_sum),
                "artifact_captures": dict(self._artifact_captures),
            }

    # ── Prometheus text format ────────────────────────────────────────────────

    def to_prometheus_text(self) -> str:
        """Serialise the counters as Prometheus text-format lines."""
        snap = self.snapshot()
        pool = self._pool
        family = self._family
        lines: list[str] = []

        def metric(name: str, labels: Dict[str, str], value: object) -> None:
            lbl = ",".join(f'{k}="{v}"' for k, v in labels.items())
            lines.append(f"{name}{{{lbl}}} {value}")

        for outcome, count in snap["jobs_processed"].items():
            metric(
                "ubag_worker_jobs_processed_total",
                {"worker_pool": pool, "adapter_family": family, "outcome": outcome},
                count,
            )
        for outcome, s in snap["job_duration_sum"].items():
            count = snap["jobs_processed"].get(outcome, 0)
            metric(
                "ubag_worker_job_duration_seconds_count",
                {"worker_pool": pool, "adapter_family": family, "outcome": outcome},
                count,
            )
            metric(
                "ubag_worker_job_duration_seconds_sum",
                {"worker_pool": pool, "adapter_family": family, "outcome": outcome},
                f"{s:.6f}",
            )

        for outcome, count in snap["result_ingestions"].items():
            error_class = "none" if outcome == "success" else "worker_execution"
            metric(
                "ubag_worker_result_ingestions_total",
                {
                    "worker_pool": pool,
                    "adapter_family": family,
                    "outcome": outcome,
                    "error_class": error_class,
                },
                count,
            )
        for outcome, s in snap["ingestion_duration_sum"].items():
            count = snap["result_ingestions"].get(outcome, 0)
            metric(
                "ubag_worker_result_ingestion_duration_seconds_count",
                {"worker_pool": pool, "adapter_family": family, "outcome": outcome},
                count,
            )
            metric(
                "ubag_worker_result_ingestion_duration_seconds_sum",
                {"worker_pool": pool, "adapter_family": family, "outcome": outcome},
                f"{s:.6f}",
            )

        for outcome, count in snap["artifact_captures"].items():
            metric(
                "ubag_artifact_captures_total",
                {"artifact_type": "file", "outcome": outcome},
                count,
            )

        return "\n".join(lines) + "\n"

    def write_textfile(self, path: str) -> None:
        """Write the Prometheus text-format snapshot to a file atomically."""
        tmp = path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            f.write(self.to_prometheus_text())
        os.replace(tmp, path)


# Module-level default collector.
_DEFAULT = WorkerMetrics()


def default_metrics() -> WorkerMetrics:
    """Return the process-wide default WorkerMetrics instance."""
    return _DEFAULT

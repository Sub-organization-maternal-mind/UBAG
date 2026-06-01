"""Session recording (§13.8) — captures HAR entries, DOM snapshots, console
logs, and screenshots during a live browser job.

All data is kept in memory as plain Python dicts and dataclasses.  Persistence
to MinIO / object storage is handled by the caller (not this module) to keep
the recording subsystem dependency-free and easily testable.

Security: recordings must never capture raw passwords, cookies, or
authorization headers.  ``HarEntry`` deliberately omits ``Cookie``,
``Set-Cookie``, and ``Authorization`` headers.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional
import datetime


# ---------------------------------------------------------------------------
# Data types
# ---------------------------------------------------------------------------

# Headers that must never be stored in recordings.
_REDACTED_HEADERS = frozenset(
    {"cookie", "set-cookie", "authorization", "proxy-authorization"}
)


@dataclass
class HarEntry:
    """A single HTTP request/response pair captured during the session."""

    url: str
    method: str = "GET"
    status: int = 0
    request_headers: Dict[str, str] = field(default_factory=dict)
    response_headers: Dict[str, str] = field(default_factory=dict)
    body_size: int = -1
    duration_ms: float = 0.0
    timestamp: str = ""

    def sanitized_request_headers(self) -> Dict[str, str]:
        """Return request headers with sensitive values redacted."""
        return {
            k: ("[redacted]" if k.lower() in _REDACTED_HEADERS else v)
            for k, v in self.request_headers.items()
        }

    def sanitized_response_headers(self) -> Dict[str, str]:
        """Return response headers with sensitive values redacted."""
        return {
            k: ("[redacted]" if k.lower() in _REDACTED_HEADERS else v)
            for k, v in self.response_headers.items()
        }


@dataclass
class DomSnapshot:
    """A point-in-time DOM fingerprint (no raw HTML to keep size manageable)."""

    url: str
    title: str = ""
    selector_counts: Dict[str, int] = field(default_factory=dict)
    """Map of CSS selector → element count (structural only, no text)."""
    timestamp: str = ""


@dataclass
class ConsoleLog:
    """A single browser console log entry."""

    level: str  # "log" | "warn" | "error" | "info"
    text: str
    timestamp: str = ""


@dataclass
class RecordingSession:
    """Accumulates all recording data for a single job run."""

    job_id: str
    har_entries: List[HarEntry] = field(default_factory=list)
    dom_snapshots: List[DomSnapshot] = field(default_factory=list)
    console_logs: List[ConsoleLog] = field(default_factory=list)
    screenshots: List[str] = field(default_factory=list)
    """Opaque references (paths or object-store keys) to screenshot files."""
    started_at: str = ""
    ended_at: str = ""


# ---------------------------------------------------------------------------
# Recording helpers
# ---------------------------------------------------------------------------


def new_session(job_id: str) -> RecordingSession:
    """Create a new recording session, stamped with the current UTC time."""
    return RecordingSession(
        job_id=job_id,
        started_at=_now_iso(),
    )


def finish_session(session: RecordingSession) -> RecordingSession:
    """Mark ``session`` as complete by setting ``ended_at``."""
    session.ended_at = _now_iso()
    return session


def record_har_entry(session: RecordingSession, entry: HarEntry) -> None:
    """Append ``entry`` to ``session.har_entries``."""
    session.har_entries.append(entry)


def record_dom_snapshot(
    session: RecordingSession,
    url: str,
    title: str = "",
    selector_counts: Optional[Dict[str, int]] = None,
) -> DomSnapshot:
    """Build and append a :class:`DomSnapshot` to ``session.dom_snapshots``.

    Returns the newly created snapshot.
    """
    snapshot = DomSnapshot(
        url=url,
        title=title,
        selector_counts=selector_counts or {},
        timestamp=_now_iso(),
    )
    session.dom_snapshots.append(snapshot)
    return snapshot


def record_console_log(
    session: RecordingSession, level: str, text: str
) -> ConsoleLog:
    """Append a console log entry to ``session``."""
    log = ConsoleLog(level=level, text=text, timestamp=_now_iso())
    session.console_logs.append(log)
    return log


def record_screenshot(session: RecordingSession, ref: str) -> None:
    """Append a screenshot reference (path or object-store key) to ``session``."""
    session.screenshots.append(ref)


def to_summary(session: RecordingSession) -> Dict[str, Any]:
    """Serialize ``session`` to a JSON-compatible summary dict.

    Sensitive headers are redacted; raw payloads are excluded.
    """
    return {
        "job_id": session.job_id,
        "started_at": session.started_at,
        "ended_at": session.ended_at,
        "har_entry_count": len(session.har_entries),
        "dom_snapshot_count": len(session.dom_snapshots),
        "console_log_count": len(session.console_logs),
        "screenshot_count": len(session.screenshots),
        "har_entries": [
            {
                "url": e.url,
                "method": e.method,
                "status": e.status,
                "request_headers": e.sanitized_request_headers(),
                "response_headers": e.sanitized_response_headers(),
                "body_size": e.body_size,
                "duration_ms": e.duration_ms,
                "timestamp": e.timestamp,
            }
            for e in session.har_entries
        ],
        "dom_snapshots": [
            {
                "url": s.url,
                "title": s.title,
                "selector_counts": s.selector_counts,
                "timestamp": s.timestamp,
            }
            for s in session.dom_snapshots
        ],
        "console_logs": [
            {"level": l.level, "text": l.text, "timestamp": l.timestamp}
            for l in session.console_logs
        ],
        "screenshots": list(session.screenshots),
    }


# ---------------------------------------------------------------------------
# Internal helpers
# ---------------------------------------------------------------------------


def _now_iso() -> str:
    return datetime.datetime.utcnow().strftime("%Y-%m-%dT%H:%M:%S.%f") + "Z"

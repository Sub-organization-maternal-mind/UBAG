"""Contract-conformant JSON logging for the UBAG worker (§18.1 / Task 2.4).

Emits log records with the required fields:
  timestamp, level, environment, service, message, trace_id

Redacts keys matching FORBIDDEN_FIELD_PATTERNS from
packages/observability/src/safety.mjs.
"""
from __future__ import annotations

import json
import logging
import os
import re
import sys
from datetime import datetime, timezone
from typing import Any, Dict, Optional

# ── Forbidden field patterns (mirrors safety.mjs FORBIDDEN_FIELD_PATTERNS) ─────

_FORBIDDEN_PATTERNS = [
    re.compile(
        r"(?i)(^|_)(authorization|cookie|set_cookie|password|passwd|secret|"
        r"token|api_key|apikey|private_key|credential|session_cookie)($|_)"
    ),
    re.compile(
        r"(?i)(^|_)(raw_prompt|raw_response|html|screenshot_base64|card_number|cvv)($|_)"
    ),
]


def is_forbidden_key(key: str) -> bool:
    """Return True if the key matches any PII/secret pattern."""
    return any(p.search(key) for p in _FORBIDDEN_PATTERNS)


def redact_dict(record: Dict[str, Any]) -> Dict[str, Any]:
    """Remove top-level keys that match forbidden patterns."""
    return {k: v for k, v in record.items() if not is_forbidden_key(k)}


# ── JSON log handler ──────────────────────────────────────────────────────────

_LEVEL_MAP = {
    logging.DEBUG: "debug",
    logging.INFO: "info",
    logging.WARNING: "warn",
    logging.ERROR: "error",
    logging.CRITICAL: "fatal",
}


class UbagJsonHandler(logging.Handler):
    """Emit structured JSON log records to an output stream.

    Each emitted record includes the §18.1 required fields plus any extra
    attributes attached to the LogRecord.  Forbidden keys are silently dropped.
    """

    def __init__(
        self,
        stream=None,
        service: str = "ubag-worker",
        environment: Optional[str] = None,
    ) -> None:
        super().__init__()
        self._stream = stream or sys.stderr
        self._service = service
        self._environment = environment or os.environ.get("UBAG_ENVIRONMENT", "local")

    def emit(self, record: logging.LogRecord) -> None:
        try:
            # Build the required fields.
            trace_id = getattr(record, "trace_id", "") or ""
            level_str = _LEVEL_MAP.get(record.levelno, "info")
            ts = datetime.fromtimestamp(record.created, tz=timezone.utc).strftime(
                "%Y-%m-%dT%H:%M:%S.%f"
            )[:-3] + "Z"  # millisecond precision UTC

            entry: Dict[str, Any] = {
                "timestamp": ts,
                "level": level_str,
                "environment": self._environment,
                "service": self._service,
                "message": self.format(record),
                "trace_id": trace_id,
            }

            # Attach any extra fields from the LogRecord (e.g. via logger.info(..., extra=...)).
            for key, val in record.__dict__.items():
                if key in {
                    "name", "msg", "args", "levelname", "levelno",
                    "pathname", "filename", "module", "exc_info",
                    "exc_text", "stack_info", "lineno", "funcName",
                    "created", "msecs", "relativeCreated", "thread",
                    "threadName", "processName", "process", "message",
                    "taskName",
                }:
                    continue
                if is_forbidden_key(key):
                    continue
                entry[key] = val

            line = json.dumps(entry, ensure_ascii=False, default=str) + "\n"
            self._stream.write(line)
            self._stream.flush()
        except Exception:
            self.handleError(record)


def get_logger(
    name: str = "ubag_worker",
    level: Optional[str] = None,
    trace_id: str = "",
) -> logging.Logger:
    """Return a logger pre-configured with the UbagJsonHandler.

    Args:
        name: Logger name (typically module __name__).
        level: Log level string (DEBUG/INFO/WARN/ERROR).  Falls back to
               UBAG_LOG_LEVEL env var, then INFO.
        trace_id: W3C trace ID to inject into every record via a filter.
    """
    logger = logging.getLogger(name)

    # Avoid adding duplicate handlers in tests that call get_logger multiple times.
    if logger.handlers:
        return logger

    level_str = level or os.environ.get("UBAG_LOG_LEVEL", "INFO")
    logger.setLevel(getattr(logging, level_str.upper(), logging.INFO))

    handler = UbagJsonHandler()
    handler.setFormatter(logging.Formatter("%(message)s"))
    logger.addHandler(handler)
    logger.propagate = False

    if trace_id:
        inject_trace_id(logger, trace_id)

    return logger


def inject_trace_id(logger: logging.Logger, trace_id: str) -> None:
    """Attach a filter that adds trace_id to every record on this logger."""

    class _TraceFilter(logging.Filter):
        def filter(self, record: logging.LogRecord) -> bool:
            record.trace_id = trace_id  # type: ignore[attr-defined]
            return True

    # Remove any existing trace filter to avoid duplication.
    logger.filters = [f for f in logger.filters if not isinstance(f, _TraceFilter)]
    logger.addFilter(_TraceFilter())

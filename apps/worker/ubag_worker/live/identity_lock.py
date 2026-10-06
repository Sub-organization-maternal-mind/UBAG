"""Slot mode helpers: slot id, physical-session key, cross-process identity lock.

Everything here is inert unless ``UBAG_WORKER_SLOT_ID`` is set (slot mode, started
by a pool supervisor); unset keeps today's single-daemon behaviour byte-identical.

Physical-session key = sha256(CDP endpoint, or realpath(user_data_dir), + target).
It deliberately has NO tenant: two tenants mapped to one provider account share one
physical session and must serialize, whereas the warm-page key keeps the tenant.

The lock is a POSIX ``flock`` on a per-key file, so the kernel frees it when the
holder dies (SIGKILL included) - no stale-lock cleanup. Workers run on Linux; on
Windows the lock is a no-op with a one-time warning.
"""
from __future__ import annotations

import hashlib
import os
import re
import sys
from typing import Optional

try:  # pragma: no cover - platform dependent
    import fcntl
except ImportError:  # pragma: no cover - Windows
    fcntl = None  # type: ignore[assignment]

_SLOT_RE = re.compile(r"[^A-Za-z0-9_-]")
_warned_no_flock = False


def slot_id() -> Optional[str]:
    """Sanitised ``UBAG_WORKER_SLOT_ID``, or None when not in slot mode."""
    raw = os.environ.get("UBAG_WORKER_SLOT_ID", "").strip()
    return _SLOT_RE.sub("_", raw)[:32] or None


def identity_lock_enabled() -> bool:
    """Auto-on only in slot mode; UBAG_WORKER_IDENTITY_LOCK=0/1 overrides."""
    raw = os.environ.get("UBAG_WORKER_IDENTITY_LOCK", "").strip().lower()
    if raw in ("0", "false", "no", "off"):
        return False
    if raw in ("1", "true", "yes", "on"):
        return True
    return slot_id() is not None


def physical_session_key(*, endpoint: str, user_data_dir: str, target: str) -> str:
    """Tenant-free identity of the physical browser session (see module doc)."""
    endpoint = (endpoint or "").strip()
    if endpoint:
        where = "cdp:" + endpoint.rstrip("/").lower()
    else:
        where = "dir:" + os.path.realpath(user_data_dir or "")
    return hashlib.sha256(("%s\n%s" % (where, target)).encode("utf-8")).hexdigest()


def _lock_dir() -> str:
    explicit = os.environ.get("UBAG_WORKER_IDENTITY_LOCK_DIR", "").strip()
    if explicit:
        return explicit
    from .chat_ledger import ledger_path

    return os.path.join(os.path.dirname(ledger_path()) or ".", "identity-locks")


class IdentityBusy(Exception):
    """A non-blocking :class:`IdentityLock` found the identity held elsewhere."""


class IdentityLock:
    """Cross-process lock for one physical-session key (context manager).

    Blocking by default; ``blocking=False`` raises :class:`IdentityBusy` instead of
    waiting (used by the read-only readiness probe, which must never queue behind
    or in front of a job).
    """

    def __init__(
        self, key: str, lock_dir: Optional[str] = None, *, blocking: bool = True
    ) -> None:
        self._path = os.path.join(lock_dir or _lock_dir(), "identity-%s.lock" % key)
        self._blocking = blocking
        self._fh = None

    def __enter__(self) -> "IdentityLock":
        global _warned_no_flock
        if fcntl is None:
            if not _warned_no_flock:
                _warned_no_flock = True
                print(
                    "ubag-worker: identity lock unavailable on this platform (no flock); "
                    "running without cross-process serialization",
                    file=sys.stderr,
                )
            return self
        os.makedirs(os.path.dirname(self._path), exist_ok=True)
        fh = open(self._path, "a+")
        try:
            fcntl.flock(
                fh.fileno(), fcntl.LOCK_EX | (0 if self._blocking else fcntl.LOCK_NB)
            )
        except BlockingIOError:
            fh.close()
            raise IdentityBusy(self._path) from None
        except BaseException:
            fh.close()
            raise
        self._fh = fh
        return self

    def __exit__(self, *_exc: object) -> None:
        fh, self._fh = self._fh, None
        if fh is not None:
            fh.close()  # closing the descriptor releases the flock

"""Ledger of chats UBAG itself created, so cleanup can never touch a human's.

WHY THIS EXISTS (read before changing anything here)
----------------------------------------------------
The operator's provider accounts are REAL, user-owned accounts whose sidebars
mix UBAG's throwaway job chats with the human's own work. Deleting a chat on
these providers is PERMANENT — there is no trash to restore from. So the chat
reaper must never reason about "old chats"; it may only ever act on chats UBAG
is *recorded as having created itself*.

This ledger is that record, and it is the sole input to the reaper. A chat that
is not in here can never be deleted, which makes "we only delete our own" a
structural property rather than a heuristic (title matching / age scans would
both happily delete the human's work).

Format: JSON Lines, append-only, one record per chat::

    {"url": "...", "conv_id": "...", "target": "chatgpt_web",
     "created_at": 1752... , "conversation_key": null, "deleted_at": null}

``conversation_key`` is non-null when the chat is bound to a UBAG conversation
(multi-turn affinity). The reaper skips those: deleting a bound thread would
break the next turn with ``UBAG-TARGET-CONVERSATION-NOT-FOUND-001``.

Append-only + best-effort by design: a ledger write must never fail a job that
already produced a good answer. The cost of a missed record is a chat that never
gets cleaned up (harmless clutter) — the opposite error (a wrong record) could
cost the operator real data, so every failure mode here is biased toward
under-recording.
"""

from __future__ import annotations

import json
import os
import threading
from typing import Any, Dict, Iterator, List, Optional

# Default lives beside the executor spool on the gateway's persistent volume so
# the record survives container restarts (the reaper runs in a separate process).
DEFAULT_LEDGER_PATH = "/var/lib/ubag/chat-ledger.jsonl"

# Serializes appends from concurrent jobs inside one process. Cross-process
# safety relies on O_APPEND: each record is written with a single small write,
# which POSIX keeps atomic for pipes/files opened O_APPEND under PIPE_BUF.
_LOCK = threading.Lock()


def ledger_path() -> str:
    return os.environ.get("UBAG_CHAT_LEDGER_PATH") or DEFAULT_LEDGER_PATH


def conversation_id_from_url(url: str) -> Optional[str]:
    """Extract a provider-stable chat id from a captured chat URL.

    ChatGPT: https://chatgpt.com/c/<uuid>  -> <uuid>
    Gemini:  https://gemini.google.com/app/<id> -> <id>
    Falls back to the last non-empty path segment, which is the id for every
    provider whose thread URL we currently capture. Returns None when the URL has
    no usable id (e.g. a bare app root), so callers can skip recording rather
    than store something the reaper could mis-target.
    """

    if not url or "://" not in url:
        return None
    try:
        path = url.split("://", 1)[1].split("/", 1)[1]
    except IndexError:
        return None
    path = path.split("?", 1)[0].split("#", 1)[0]
    segments = [s for s in path.split("/") if s]
    if not segments:
        return None
    last = segments[-1]
    # Guard against recording an app root ("/app", "/c") as if it were a chat.
    if last in ("app", "c", "chat"):
        return None
    return last


def record_chat(
    *,
    url: str,
    target: str,
    created_at: float,
    conversation_key: Optional[str] = None,
    path: Optional[str] = None,
) -> bool:
    """Append one created-chat record. Returns True when written.

    Never raises: a ledger failure must not fail a job whose answer is already
    good. An unrecorded chat is just clutter; a failed job is a real loss.
    """

    conv_id = conversation_id_from_url(url)
    if not conv_id:
        return False
    record = {
        "url": url,
        "conv_id": conv_id,
        "target": target,
        "created_at": created_at,
        "conversation_key": conversation_key,
        "deleted_at": None,
    }
    target_path = path or ledger_path()
    try:
        directory = os.path.dirname(target_path)
        if directory:
            os.makedirs(directory, exist_ok=True)
        line = json.dumps(record, separators=(",", ":")) + "\n"
        with _LOCK:
            with open(target_path, "a", encoding="utf-8") as handle:
                handle.write(line)
        return True
    except Exception:  # noqa: BLE001 - best effort by design (see module docstring)
        return False


def read_chats(path: Optional[str] = None) -> List[Dict[str, Any]]:
    """Read every ledger record. Malformed lines are skipped, not fatal."""

    target_path = path or ledger_path()
    out: List[Dict[str, Any]] = []
    try:
        with open(target_path, "r", encoding="utf-8") as handle:
            for line in handle:
                line = line.strip()
                if not line:
                    continue
                try:
                    out.append(json.loads(line))
                except ValueError:
                    continue
    except FileNotFoundError:
        return []
    except Exception:  # noqa: BLE001
        return []
    return out


def reapable(
    records: List[Dict[str, Any]], *, now: float, ttl_seconds: float
) -> Iterator[Dict[str, Any]]:
    """Yield records the reaper is allowed to delete.

    A record is reapable only when ALL hold:
      * it is older than ttl_seconds (the operator's cutoff),
      * it is not already deleted,
      * it carries no conversation_key — a bound thread is still in use for
        multi-turn affinity and deleting it would break the next turn.

    Deliberately conservative: anything unparseable or ambiguous is skipped.
    """

    for record in records:
        if not isinstance(record, dict):
            continue
        if record.get("deleted_at"):
            continue
        if record.get("conversation_key"):
            continue
        if not record.get("conv_id") or not record.get("url"):
            continue
        created_at = record.get("created_at")
        if not isinstance(created_at, (int, float)):
            continue
        if (now - float(created_at)) < ttl_seconds:
            continue
        yield record


def mark_deleted(
    conv_ids: List[str], *, deleted_at: float, path: Optional[str] = None
) -> int:
    """Rewrite the ledger marking the given chats deleted. Returns the count.

    Rewrite-in-place (read all, write temp, replace) rather than append a
    tombstone, so the ledger stays small and a re-run never retries a chat the
    provider already removed.
    """

    target_path = path or ledger_path()
    wanted = set(conv_ids)
    if not wanted:
        return 0
    records = read_chats(target_path)
    if not records:
        return 0
    changed = 0
    for record in records:
        if record.get("conv_id") in wanted and not record.get("deleted_at"):
            record["deleted_at"] = deleted_at
            changed += 1
    if not changed:
        return 0
    tmp_path = target_path + ".tmp"
    try:
        with _LOCK:
            with open(tmp_path, "w", encoding="utf-8") as handle:
                for record in records:
                    handle.write(json.dumps(record, separators=(",", ":")) + "\n")
            os.replace(tmp_path, target_path)
    except Exception:  # noqa: BLE001
        try:
            os.unlink(tmp_path)
        except Exception:  # noqa: BLE001
            pass
        return 0
    return changed

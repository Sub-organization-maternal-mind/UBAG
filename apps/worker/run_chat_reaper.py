#!/usr/bin/env python3
"""Delete UBAG's own stale job chats. NEVER the human's.

The operator's provider accounts are real and user-owned: their sidebars mix
UBAG's throwaway job chats with the operator's actual work, and deletion on
these providers is PERMANENT (no trash, no undo). This reaper therefore never
reasons about "old chats" — it reads the chat ledger (chats UBAG recorded itself
as creating) and can only ever act on those ids. A chat UBAG did not create is
not in the ledger and is consequently unreachable from here.

Safety rails, in order of importance:
  1. Ledger-only targeting. No sidebar scan, no title match, no age scan.
  2. Conversation-bound chats are skipped (deleting one breaks the next turn
     with UBAG-TARGET-CONVERSATION-NOT-FOUND-001).
  3. Dry-run is the DEFAULT. Deleting requires UBAG_CHAT_REAPER_ENABLED=true,
     so the feature is inert until an operator has read a dry-run report.
  4. Ids are charset-validated before touching a selector (page_driver).
  5. A delete counts only when the chat is verified gone afterwards.

Usage::

    python run_chat_reaper.py                 # dry run: report only
    UBAG_CHAT_REAPER_ENABLED=true python run_chat_reaper.py

Env:
    UBAG_CHAT_REAPER_ENABLED   "true" to actually delete (default: dry run)
    UBAG_CHAT_TTL_SECONDS      age before a chat is reapable (default 7200 = 2h)
    UBAG_CHAT_LEDGER_PATH      ledger location (default /var/lib/ubag/chat-ledger.jsonl)
    UBAG_REMOTE_BROWSER_ENDPOINT  CDP endpoint of the shared logged-in Chrome
"""

from __future__ import annotations

import json
import os
import sys
import time
from typing import Any, Dict, List

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from ubag_worker.live import chat_ledger  # noqa: E402
from ubag_worker.live.page_driver import create_default_driver  # noqa: E402
from ubag_worker.live.selectors import get_provider_selectors  # noqa: E402

DEFAULT_TTL_SECONDS = 7200.0  # 2 hours


def _flag(name: str, default: bool = False) -> bool:
    raw = os.environ.get(name)
    if raw is None:
        return default
    return raw.strip().lower() not in ("", "0", "false", "no", "off")


def _emit(record: Dict[str, Any]) -> None:
    sys.stdout.write(json.dumps(record, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def main() -> int:
    enabled = _flag("UBAG_CHAT_REAPER_ENABLED", False)
    try:
        ttl = float(os.environ.get("UBAG_CHAT_TTL_SECONDS") or DEFAULT_TTL_SECONDS)
    except ValueError:
        ttl = DEFAULT_TTL_SECONDS
    now = time.time()

    records = chat_ledger.read_chats()
    candidates: List[Dict[str, Any]] = list(
        chat_ledger.reapable(records, now=now, ttl_seconds=ttl)
    )
    _emit({
        "type": "reaper.scan",
        "ledger_total": len(records),
        "reapable": len(candidates),
        "ttl_seconds": ttl,
        "mode": "delete" if enabled else "dry_run",
    })

    if not candidates:
        return 0

    if not enabled:
        # Dry run: show exactly what WOULD be deleted so an operator can audit the
        # targeting before anything irreversible happens.
        for record in candidates:
            _emit({
                "type": "reaper.would_delete",
                "conv_id": record.get("conv_id"),
                "target": record.get("target"),
                "url": record.get("url"),
                "age_seconds": round(now - float(record.get("created_at", now))),
            })
        return 0

    # Group by provider: one browser page per target, reused across its chats.
    by_target: Dict[str, List[Dict[str, Any]]] = {}
    for record in candidates:
        by_target.setdefault(str(record.get("target")), []).append(record)

    deleted_ids: List[str] = []
    for target, group in by_target.items():
        try:
            selectors = get_provider_selectors(target)
        except Exception:  # noqa: BLE001 - unknown/removed provider: skip, never guess
            _emit({"type": "reaper.skipped", "target": target, "reason": "unknown_target"})
            continue
        if selectors.delete_chat is None:
            # No VERIFIED delete flow for this provider: refuse rather than
            # improvise one against a real account.
            _emit({"type": "reaper.skipped", "target": target, "reason": "no_delete_flow"})
            continue

        driver = create_default_driver()
        try:
            driver.open(
                target_url=selectors.target_url,
                user_data_dir=os.environ.get("UBAG_PROFILE_DIR") or "var/profiles/reaper",
                headless=False,
            )
            for record in group:
                conv_id = str(record.get("conv_id"))
                ok = False
                try:
                    ok = driver.delete_chat(selectors, conv_id)
                except Exception:  # noqa: BLE001 - one bad chat must not stop the run
                    ok = False
                _emit({
                    "type": "reaper.deleted" if ok else "reaper.delete_failed",
                    "conv_id": conv_id,
                    "target": target,
                })
                if ok:
                    deleted_ids.append(conv_id)
        except Exception as exc:  # noqa: BLE001
            _emit({"type": "reaper.error", "target": target, "error": str(exc)[:200]})
        finally:
            try:
                driver.close()
            except Exception:  # noqa: BLE001
                pass

    marked = chat_ledger.mark_deleted(deleted_ids, deleted_at=time.time())
    _emit({"type": "reaper.done", "deleted": len(deleted_ids), "ledger_marked": marked})
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

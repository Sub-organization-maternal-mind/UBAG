"""Tests for run_chat_reaper.main — the orchestration around the ONE
irreversible thing UBAG can do.

The ledger's targeting rules (``chat_ledger.reapable``) have their own tests in
``test_chat_ledger.py``. This module pins the reaper process behavior on top of
them: dry-run is the default, only ledgered chats are ever reachable, providers
without a VERIFIED delete flow are skipped rather than improvised against, and
only verified deletions are marked in the ledger. Every external effect (ledger
file, browser driver, provider selectors, clock) is mocked — no network, no
filesystem, no browser.
"""

import json
import os
import types
import unittest
from unittest import mock

import run_chat_reaper

NOW = 1_800_000_000.0
DEFAULT_TTL = run_chat_reaper.DEFAULT_TTL_SECONDS

REAPER_ENV_KEYS = ("UBAG_CHAT_REAPER_ENABLED", "UBAG_CHAT_TTL_SECONDS", "UBAG_PROFILE_DIR")


def _record(conv_id, *, target="chatgpt_web", age=DEFAULT_TTL + 60.0, **extra):
    record = {
        "url": "https://chatgpt.com/c/%s" % conv_id,
        "conv_id": conv_id,
        "target": target,
        "created_at": NOW - age,
        "conversation_key": None,
        "deleted_at": None,
    }
    record.update(extra)
    return record


class ReaperMainTests(unittest.TestCase):
    def setUp(self):
        env_patcher = mock.patch.dict(os.environ, {}, clear=False)
        env_patcher.start()
        self.addCleanup(env_patcher.stop)
        for name in REAPER_ENV_KEYS:
            os.environ.pop(name, None)

    def run_reaper(self, *, records=None, selectors=None, driver=None, selectors_side_effect=None):
        """Run main() with the ledger, selectors, driver, and clock mocked.

        Returns (exit_code, emitted_records). ``selectors`` is handed to the
        (mocked) get_provider_selectors as its return value; use
        ``selectors_side_effect`` to simulate an unknown provider raising.
        """
        if records is None:
            records = []
        emitted = []
        patchers = [
            mock.patch.object(run_chat_reaper.chat_ledger, "read_chats", return_value=records),
            mock.patch.object(run_chat_reaper.chat_ledger, "mark_deleted", return_value=len(records)),
            mock.patch.object(run_chat_reaper, "get_provider_selectors", return_value=selectors, side_effect=selectors_side_effect),
            mock.patch.object(run_chat_reaper, "create_default_driver", return_value=driver),
            mock.patch.object(run_chat_reaper.time, "time", return_value=NOW),
            mock.patch.object(run_chat_reaper, "_emit", side_effect=lambda record: emitted.append(record)),
        ]
        for patcher in patchers:
            patcher.start()
            self.addCleanup(patcher.stop)

        exit_code = run_chat_reaper.main()
        self.create_default_driver = run_chat_reaper.create_default_driver
        self.mark_deleted = run_chat_reaper.chat_ledger.mark_deleted
        return exit_code, emitted

    def scan(self, emitted):
        self.assertTrue(emitted, "reaper must always emit a reaper.scan record")
        self.assertEqual(emitted[0]["type"], "reaper.scan")
        return emitted[0]

    def would_delete(self, emitted):
        return [record.get("conv_id") for record in emitted if record["type"] == "reaper.would_delete"]

    # --- switches ------------------------------------------------------------

    def test_dry_run_is_the_default(self):
        _, emitted = self.run_reaper(records=[_record("c_old")])
        scan = self.scan(emitted)
        self.assertEqual(scan["mode"], "dry_run")
        self.assertEqual(scan["ledger_total"], 1)
        self.assertEqual(scan["reapable"], 1)
        # Dry run reports exactly what WOULD be deleted and stops there.
        would = [r for r in emitted if r["type"] == "reaper.would_delete"]
        self.assertEqual(len(would), 1)
        self.assertEqual(would[0]["conv_id"], "c_old")
        self.assertEqual(would[0]["target"], "chatgpt_web")
        # The default record is one minute past the default TTL.
        self.assertEqual(would[0]["age_seconds"], int(DEFAULT_TTL + 60))
        self.create_default_driver.assert_not_called()
        self.mark_deleted.assert_not_called()

    def test_delete_requires_explicit_enabled_flag(self):
        for raw in ("0", "false", "no", "off", ""):
            os.environ["UBAG_CHAT_REAPER_ENABLED"] = raw
            _, emitted = self.run_reaper(records=[_record("c_old")])
            self.assertEqual(self.scan(emitted)["mode"], "dry_run", "flag value %r" % raw)
            self.mark_deleted.assert_not_called()

    def test_enabled_flag_activates_delete_mode(self):
        selectors = types.SimpleNamespace(target_url="https://chatgpt.com", delete_chat=lambda s, cid: True)
        for raw in ("true", "TRUE", "1", "yes", "on"):
            os.environ["UBAG_CHAT_REAPER_ENABLED"] = raw
            driver = mock.MagicMock()
            driver.delete_chat.return_value = True
            _, emitted = self.run_reaper(records=[_record("c_old")], selectors=selectors, driver=driver)
            self.assertEqual(self.scan(emitted)["mode"], "delete", "flag value %r" % raw)
            driver.delete_chat.assert_called_once_with(selectors, "c_old")
            driver.close.assert_called()
            self.mark_deleted.assert_called_once_with(["c_old"], deleted_at=mock.ANY)

    # --- decision logic ------------------------------------------------------

    def test_empty_ledger_exits_cleanly(self):
        exit_code, emitted = self.run_reaper(records=[])
        self.assertEqual(exit_code, 0)
        scan = self.scan(emitted)
        self.assertEqual(scan["ledger_total"], 0)
        self.assertEqual(scan["reapable"], 0)
        self.assertEqual(len(emitted), 1, "nothing else to report on an empty ledger")
        self.create_default_driver.assert_not_called()

    def test_malformed_and_nonreapable_entries_are_never_targets(self):
        records = [
            None,  # not a dict
            "ledger-line-garbage",  # not a dict
            {},  # dict but missing everything
            _record("c_bound", conversation_key="conv-affinity"),  # in active use
            _record("c_deleted", deleted_at=NOW - 10),  # already reaped
            {**_record("c_missing_conv"), "conv_id": None},  # unusable target id
            _record("c_fresh", age=DEFAULT_TTL - 1.0),  # younger than the TTL
            _record("c_old"),
        ]
        _, emitted = self.run_reaper(records=records)
        scan = self.scan(emitted)
        # The real chat_ledger.reapable filter runs inside the reaper: exactly
        # one of the eight entries may ever be reached.
        self.assertEqual(scan["ledger_total"], 8)
        self.assertEqual(scan["reapable"], 1)
        self.assertEqual(self.would_delete(emitted), ["c_old"])

    def test_ttl_boundary_at_cutoff_is_reapable_younger_is_not(self):
        # reapable() uses a strict age < ttl skip, so a chat exactly at the
        # cutoff is old enough to reap; anything younger is not.
        records = [
            _record("c_just_under", age=DEFAULT_TTL - 0.001),
            _record("c_at_cutoff", age=DEFAULT_TTL),
        ]
        _, emitted = self.run_reaper(records=records)
        self.assertEqual(self.would_delete(emitted), ["c_at_cutoff"])

    def test_ttl_env_is_honored_when_numeric(self):
        os.environ["UBAG_CHAT_TTL_SECONDS"] = "3600"
        _, emitted = self.run_reaper(records=[_record("c_hour_old", age=3600 + 1)])
        self.assertEqual(self.scan(emitted)["ttl_seconds"], 3600.0)
        self.assertEqual(self.would_delete(emitted), ["c_hour_old"])

    def test_ttl_env_garbage_falls_back_to_default(self):
        os.environ["UBAG_CHAT_TTL_SECONDS"] = "not-a-number"
        _, emitted = self.run_reaper(records=[_record("c_hour_old", age=3600 + 1)])
        self.assertEqual(self.scan(emitted)["ttl_seconds"], DEFAULT_TTL)
        # At the default TTL a one-hour-old chat is not yet reapable.
        self.assertEqual(self.would_delete(emitted), [])

    # --- delete-mode skips (fail closed) --------------------------------------

    def test_unknown_target_is_skipped_and_driver_never_created(self):
        os.environ["UBAG_CHAT_REAPER_ENABLED"] = "true"
        _, emitted = self.run_reaper(
            records=[_record("c_old", target="mystery_provider")],
            selectors_side_effect=Exception("provider not configured"),
        )
        skips = [r for r in emitted if r["type"] == "reaper.skipped"]
        self.assertEqual(skips, [{"type": "reaper.skipped", "target": "mystery_provider", "reason": "unknown_target"}])
        self.create_default_driver.assert_not_called()
        self.mark_deleted.assert_called_once_with([], deleted_at=mock.ANY)

    def test_provider_without_verified_delete_flow_is_skipped(self):
        os.environ["UBAG_CHAT_REAPER_ENABLED"] = "true"
        selectors = types.SimpleNamespace(target_url="https://example.com", delete_chat=None)
        _, emitted = self.run_reaper(records=[_record("c_old")], selectors=selectors)
        skips = [r for r in emitted if r["type"] == "reaper.skipped"]
        self.assertEqual(len(skips), 1)
        self.assertEqual(skips[0]["reason"], "no_delete_flow")
        self.create_default_driver.assert_not_called()
        self.mark_deleted.assert_called_once_with([], deleted_at=mock.ANY)

    # --- delete-mode behavior ---------------------------------------------------

    def test_delete_marks_only_verified_deletions(self):
        os.environ["UBAG_CHAT_REAPER_ENABLED"] = "true"
        selectors = types.SimpleNamespace(target_url="https://chatgpt.com", delete_chat=lambda s, cid: True)
        driver = mock.MagicMock()
        # Third chat sits on a second target, which gets its own driver.open.
        driver.delete_chat.side_effect = [True, False, True]
        records = [
            _record("c_ok1"),
            _record("c_fail"),
            _record("c_ok2", target="gemini_web"),
        ]
        _, emitted = self.run_reaper(records=records, selectors=selectors, driver=driver)
        deleted = [r["conv_id"] for r in emitted if r["type"] == "reaper.deleted"]
        failed = [r["conv_id"] for r in emitted if r["type"] == "reaper.delete_failed"]
        self.assertEqual(sorted(deleted), ["c_ok1", "c_ok2"])
        self.assertEqual(failed, ["c_fail"])
        # Only VERIFIED deletions may be marked in the ledger.
        self.assertEqual(sorted(self.mark_deleted.call_args[0][0]), ["c_ok1", "c_ok2"])
        done = [r for r in emitted if r["type"] == "reaper.done"]
        self.assertEqual(done[0]["deleted"], 2)
        # The browser session is opened visibly (headless=False): an operator
        # watching the machine must be able to see irreversible deletes happen.
        self.assertFalse(driver.open.call_args.kwargs["headless"])
        driver.close.assert_called()

    def test_delete_failure_of_one_chat_does_not_stop_the_rest(self):
        os.environ["UBAG_CHAT_REAPER_ENABLED"] = "true"
        selectors = types.SimpleNamespace(target_url="https://chatgpt.com", delete_chat=lambda s, cid: True)
        driver = mock.MagicMock()
        driver.delete_chat.side_effect = [RuntimeError("selector drifted"), True]
        _, emitted = self.run_reaper(
            records=[_record("c_boom"), _record("c_ok")], selectors=selectors, driver=driver
        )
        failed = [r["conv_id"] for r in emitted if r["type"] == "reaper.delete_failed"]
        deleted = [r["conv_id"] for r in emitted if r["type"] == "reaper.deleted"]
        self.assertEqual(failed, ["c_boom"])
        self.assertEqual(deleted, ["c_ok"])
        self.mark_deleted.assert_called_once_with(["c_ok"], deleted_at=mock.ANY)
        driver.close.assert_called()

    def test_driver_open_failure_is_contained_and_still_closes(self):
        os.environ["UBAG_CHAT_REAPER_ENABLED"] = "true"
        selectors = types.SimpleNamespace(target_url="https://chatgpt.com", delete_chat=lambda s, cid: True)
        driver = mock.MagicMock()
        driver.open.side_effect = RuntimeError("cdp endpoint down")
        _, emitted = self.run_reaper(records=[_record("c_old")], selectors=selectors, driver=driver)
        errors = [r for r in emitted if r["type"] == "reaper.error"]
        self.assertEqual(len(errors), 1)
        self.assertIn("cdp endpoint down", errors[0]["error"])
        done = [r for r in emitted if r["type"] == "reaper.done"]
        self.assertEqual(done[0]["deleted"], 0)
        driver.close.assert_called()
        self.mark_deleted.assert_called_once_with([], deleted_at=mock.ANY)

    # --- output contract --------------------------------------------------------

    def test_emit_writes_compact_jsonl(self):
        captured = mock.MagicMock()
        record = {"type": "reaper.scan", "mode": "dry_run"}
        with mock.patch.object(run_chat_reaper.sys, "stdout", captured):
            run_chat_reaper._emit(record)
        written = captured.write.call_args[0][0]
        self.assertEqual(written, json.dumps(record, separators=(",", ":")) + "\n")


if __name__ == "__main__":
    unittest.main()

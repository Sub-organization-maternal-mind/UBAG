"""Chat-ledger + reaper-targeting tests.

These guard the ONE irreversible thing UBAG can do. The provider accounts are
real and user-owned, their sidebars mix UBAG's job chats with the operator's own
work, and provider deletion is permanent. So the property under test is not
"the reaper deletes old chats" but "the reaper can only ever reach chats UBAG
recorded itself as creating".
"""

import os
import tempfile
import unittest

from ubag_worker.live import chat_ledger
from ubag_worker.live.engine import LiveSessionEngine
from ubag_worker.live.page_driver import MockPageDriver
from ubag_worker.live.selectors import get_provider_selectors


def _payload(target, *, conversation_id=None):
    job = {
        "target": target,
        "command_type": "chat.prompt",
        "input": {"prompt": "hi"},
        "options": {"return_mode": "final"},
    }
    if conversation_id is not None:
        job["conversation_id"] = conversation_id
    return {
        "api_version": "2026-05-22",
        "job_id": "job_ledger_%s" % target,
        "trace_id": "trace_ledger",
        "job": job,
    }


class ConversationIdFromUrlTests(unittest.TestCase):
    def test_extracts_chatgpt_uuid(self):
        self.assertEqual(
            chat_ledger.conversation_id_from_url("https://chatgpt.com/c/abc-123"),
            "abc-123",
        )

    def test_ignores_query_and_fragment(self):
        self.assertEqual(
            chat_ledger.conversation_id_from_url("https://chatgpt.com/c/abc-123?x=1#f"),
            "abc-123",
        )

    def test_app_root_is_not_a_chat(self):
        # Recording "/c" or "/app" as a chat id could later target something
        # unintended; refuse instead.
        for url in ("https://chatgpt.com/c", "https://gemini.google.com/app", "https://x.com/"):
            self.assertIsNone(chat_ledger.conversation_id_from_url(url), url)

    def test_garbage_is_refused(self):
        for url in ("", "not-a-url", "/c/abc"):
            self.assertIsNone(chat_ledger.conversation_id_from_url(url), url)


class ReapableTests(unittest.TestCase):
    def _record(self, **kw):
        base = {
            "url": "https://chatgpt.com/c/id1",
            "conv_id": "id1",
            "target": "chatgpt_web",
            "created_at": 0.0,
            "conversation_key": None,
            "deleted_at": None,
        }
        base.update(kw)
        return base

    def test_old_unbound_chat_is_reapable(self):
        out = list(chat_ledger.reapable([self._record()], now=10_000, ttl_seconds=7200))
        self.assertEqual([r["conv_id"] for r in out], ["id1"])

    def test_young_chat_is_not_reapable(self):
        out = list(chat_ledger.reapable([self._record(created_at=9_999)], now=10_000, ttl_seconds=7200))
        self.assertEqual(out, [])

    def test_conversation_bound_chat_is_never_reapable(self):
        # Deleting a bound thread breaks the next turn (thread_broken), so it is
        # skipped no matter how old it is.
        out = list(chat_ledger.reapable(
            [self._record(conversation_key="k1")], now=10_000_000, ttl_seconds=7200))
        self.assertEqual(out, [])

    def test_already_deleted_is_not_retried(self):
        out = list(chat_ledger.reapable(
            [self._record(deleted_at=123.0)], now=10_000, ttl_seconds=7200))
        self.assertEqual(out, [])

    def test_malformed_records_are_skipped_not_fatal(self):
        records = [
            "not-a-dict",
            self._record(conv_id=None),
            self._record(created_at="yesterday"),
            self._record(url=""),
            self._record(conv_id="good"),
        ]
        out = list(chat_ledger.reapable(records, now=10_000, ttl_seconds=7200))
        self.assertEqual([r["conv_id"] for r in out], ["good"])


class LedgerRoundTripTests(unittest.TestCase):
    def test_record_read_and_mark_deleted(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "nested", "ledger.jsonl")
            self.assertTrue(chat_ledger.record_chat(
                url="https://chatgpt.com/c/aaa", target="chatgpt_web",
                created_at=1.0, path=path))
            self.assertTrue(chat_ledger.record_chat(
                url="https://chatgpt.com/c/bbb", target="chatgpt_web",
                created_at=2.0, conversation_key="k", path=path))
            records = chat_ledger.read_chats(path)
            self.assertEqual([r["conv_id"] for r in records], ["aaa", "bbb"])
            self.assertEqual(chat_ledger.mark_deleted(["aaa"], deleted_at=9.0, path=path), 1)
            records = chat_ledger.read_chats(path)
            self.assertEqual(records[0]["deleted_at"], 9.0)
            self.assertIsNone(records[1]["deleted_at"])

    def test_unusable_url_is_not_recorded(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = os.path.join(tmp, "ledger.jsonl")
            self.assertFalse(chat_ledger.record_chat(
                url="https://chatgpt.com/c", target="chatgpt_web", created_at=1.0, path=path))
            self.assertEqual(chat_ledger.read_chats(path), [])

    def test_record_failure_never_raises(self):
        # A ledger write must not fail a job that already produced a good answer.
        self.assertFalse(chat_ledger.record_chat(
            url="https://chatgpt.com/c/aaa", target="chatgpt_web", created_at=1.0,
            path="/nonexistent\x00/bad/ledger.jsonl"))


class EngineChatSinkTests(unittest.TestCase):
    def test_engine_records_the_chat_it_creates(self):
        seen = []
        selectors = get_provider_selectors("chatgpt_web")
        driver = MockPageDriver(thread_url="https://chatgpt.com/c/zzz")
        LiveSessionEngine(selectors, chat_sink=lambda **kw: seen.append(kw)).run(
            _payload("chatgpt_web"), driver=driver)
        self.assertEqual(len(seen), 1)
        self.assertEqual(seen[0]["url"], "https://chatgpt.com/c/zzz")
        self.assertEqual(seen[0]["target"], "chatgpt_web")
        self.assertIsNone(seen[0]["conversation_key"])

    def test_no_sink_means_no_side_effects(self):
        # Default engine stays side-effect free for everyone not running the reaper.
        selectors = get_provider_selectors("chatgpt_web")
        driver = MockPageDriver(thread_url="https://chatgpt.com/c/zzz")
        events = LiveSessionEngine(selectors).run(_payload("chatgpt_web"), driver=driver)
        self.assertIn("completed", [e["type"] for e in events])

    def test_sink_failure_never_fails_the_job(self):
        def boom(**kw):
            raise RuntimeError("ledger is on fire")

        selectors = get_provider_selectors("chatgpt_web")
        driver = MockPageDriver(thread_url="https://chatgpt.com/c/zzz", response_text="42")
        events = LiveSessionEngine(selectors, chat_sink=boom).run(
            _payload("chatgpt_web"), driver=driver)
        completed = [e for e in events if e["type"] == "completed"]
        self.assertEqual(len(completed), 1)
        self.assertEqual(completed[0]["data"]["result"]["text"], "42")


class DeleteChatDriverTests(unittest.TestCase):
    def test_delete_is_refused_when_provider_has_no_verified_flow(self):
        selectors = get_provider_selectors("mistral_lechat")
        driver = MockPageDriver()
        self.assertFalse(driver.delete_chat(selectors, "abc123"))
        self.assertEqual(driver.deleted_chats, [])

    def test_delete_targets_the_exact_id(self):
        selectors = get_provider_selectors("chatgpt_web")
        driver = MockPageDriver()
        self.assertTrue(driver.delete_chat(selectors, "abc123"))
        self.assertEqual(driver.deleted_chats, ["abc123"])

    def test_delete_reports_false_when_chat_survives(self):
        selectors = get_provider_selectors("chatgpt_web")
        driver = MockPageDriver(undeletable_chats=("stubborn",))
        self.assertFalse(driver.delete_chat(selectors, "stubborn"))

    def test_chatgpt_delete_flow_declares_a_list_ready_gate(self):
        # Regression: without list_ready, a sidebar that has not painted yet
        # reads as "already deleted", the reaper reports success, the ledger is
        # marked done, and the chat is never retried. Observed on the live
        # account (a reaped-looking chat was still alive), so the gate is
        # load-bearing rather than cosmetic.
        flow = get_provider_selectors("chatgpt_web").delete_chat
        self.assertTrue(flow.list_ready, "chatgpt_web delete flow must gate on a rendered chat list")
        # Every id-addressed template must actually consume the id, so a delete
        # can never be expressed as "whatever is on screen".
        for template in (flow.open_options, flow.still_present):
            self.assertIn("{conv_id}", template)


class ConvIdSelectorGuardTests(unittest.TestCase):
    def test_selector_breaking_ids_are_refused(self):
        from ubag_worker.live.page_driver import _SAFE_CONV_ID_RE

        for bad in ("", "a", "abc'123", 'abc"123', "abc]123", "abc 123", "a" * 200, "../../etc"):
            self.assertIsNone(_SAFE_CONV_ID_RE.match(bad), bad)
        for good in ("6a59fc0d-2d34-83ee-9bae-559427cf9987", "abc_123-XYZ"):
            self.assertIsNotNone(_SAFE_CONV_ID_RE.match(good), good)


if __name__ == "__main__":
    unittest.main()

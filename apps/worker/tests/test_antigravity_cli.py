import asyncio
import json
import os
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock, patch

REPO_ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(REPO_ROOT / "adapters" / "antigravity_cli"))

from ubag_antigravity_cli_adapter import account_server  # noqa: E402
from ubag_antigravity_cli_adapter.account_server import handle_connection  # noqa: E402
from ubag_antigravity_cli_adapter.adapter import AntigravityCLIAdapter  # noqa: E402
from ubag_antigravity_cli_adapter.process import (  # noqa: E402
    AgyProcessManager,
    AgyQuotaError,
    AgySocketClient,
    CLIConfig,
)


class AgyProcessManagerTests(unittest.IsolatedAsyncioTestCase):
    async def test_print_session_uses_requested_model_and_effort(self):
        config = CLIConfig(agy_binary="agy-test", model="gemini-3.8-flash", effort="high")
        with patch.object(asyncio, "create_subprocess_exec", new_callable=AsyncMock) as spawn:
            spawn.return_value = SimpleNamespace(returncode=None)
            await AgyProcessManager(config).start()

        args = spawn.call_args.args
        self.assertIn("--print", args)
        self.assertEqual(args[args.index("--model") + 1], "gemini-3.8-flash")
        self.assertEqual(args[args.index("--effort") + 1], "high")
        self.assertEqual(spawn.call_args.kwargs["stderr"], asyncio.subprocess.DEVNULL)

    async def test_eof_before_result_is_an_error(self):
        stdout = asyncio.StreamReader()
        stdout.feed_eof()
        process = SimpleNamespace(
            stdin=SimpleNamespace(write=Mock(), drain=AsyncMock()),
            stdout=stdout,
            returncode=1,
        )
        with patch.object(asyncio, "create_subprocess_exec", new_callable=AsyncMock) as spawn:
            spawn.return_value = process
            manager = AgyProcessManager(CLIConfig())
            with self.assertRaisesRegex(RuntimeError, "without a result"):
                async for _ in manager.send_prompt("test"):
                    pass


class AgySocketClientTests(unittest.IsolatedAsyncioTestCase):
    async def test_sends_one_turn_to_account_worker_without_secrets(self):
        stdout = asyncio.StreamReader()
        stdout.feed_data(b'{"event":"result","text":"hello"}\n')
        stdout.feed_eof()
        writer = SimpleNamespace(write=Mock(), drain=AsyncMock(), close=Mock(), wait_closed=AsyncMock())
        with patch.object(asyncio, "open_unix_connection", new_callable=AsyncMock, create=True) as connect:
            connect.return_value = stdout, writer
            events = [event async for event in AgySocketClient("/run/acct_1.sock", CLIConfig()).send_prompt("hello")]

        self.assertEqual(events, [{"event": "result", "text": "hello"}])
        self.assertEqual(connect.call_args.args[0], "/run/acct_1.sock")
        self.assertIn(b'"prompt": "hello"', writer.write.call_args.args[0])
        self.assertNotIn(b"api_key", writer.write.call_args.args[0])
        writer.close.assert_called_once()

    async def test_only_upfront_structured_quota_error_allows_failover(self):
        for prefix, expected_error in [
            (b"", AgyQuotaError),
            (b'{"event":"step_update","delta":{"text":"partial"}}\n', RuntimeError),
        ]:
            with self.subTest(prefix=prefix):
                reader = asyncio.StreamReader()
                reader.feed_data(prefix + b'{"event":"error","status":429,"code":"RESOURCE_EXHAUSTED"}\n')
                reader.feed_eof()
                writer = SimpleNamespace(write=Mock(), drain=AsyncMock(), close=Mock(), wait_closed=AsyncMock())
                with patch.object(asyncio, "open_unix_connection", new_callable=AsyncMock, create=True) as connect:
                    connect.return_value = reader, writer
                    with self.assertRaises(expected_error):
                        async for _ in AgySocketClient("/run/acct_1.sock", CLIConfig()).send_prompt("test"):
                            pass

    async def test_unstructured_errors_never_allow_failover(self):
        reader = asyncio.StreamReader()
        reader.feed_data(b'{"event":"error","message":"rate limit maybe","status":429}\n')
        reader.feed_eof()
        writer = SimpleNamespace(write=Mock(), drain=AsyncMock(), close=Mock(), wait_closed=AsyncMock())
        with patch.object(asyncio, "open_unix_connection", new_callable=AsyncMock, create=True) as connect:
            connect.return_value = reader, writer
            with self.assertRaises(RuntimeError) as caught:
                async for _ in AgySocketClient("/run/acct_1.sock", CLIConfig()).send_prompt("test"):
                    pass
        self.assertNotIsInstance(caught.exception, AgyQuotaError)


class AgyAccountServerTests(unittest.IsolatedAsyncioTestCase):
    async def test_login_session_only_exposes_a_first_party_authorization_url(self):
        session_type = getattr(account_server, "AgyLoginSession", None)
        self.assertIsNotNone(session_type)
        login = session_type("agy-test")
        login.record_output(b"Open https://example.invalid/steal?state=synthetic\n")
        self.assertNotIn("authorization_url", login.snapshot())

        login.record_output(b"Open https://accounts.google.com/o/oauth2/auth?state=synthetic\n")
        snapshot = login.snapshot()
        self.assertEqual(snapshot["state"], "awaiting_code")
        self.assertEqual(
            snapshot["authorization_url"],
            "https://accounts.google.com/o/oauth2/auth?state=synthetic",
        )

    async def test_login_snapshot_does_not_forward_cli_output_or_codes(self):
        login = account_server.AgyLoginSession("agy-test")
        login.record_output(b"Google code: synthetic-private-code\n")
        login.record_output(b"Open https://accounts.google.com/o/oauth2/auth?state=synthetic\n")
        snapshot = login.snapshot()

        self.assertEqual(snapshot["state"], "awaiting_code")
        self.assertEqual(snapshot["authorization_url"], "https://accounts.google.com/o/oauth2/auth?state=synthetic")
        self.assertNotIn("output", snapshot)
        self.assertNotIn("cursor", snapshot)
        self.assertNotIn("synthetic-private-code", str(snapshot))

    async def test_login_stops_buffering_cli_output_after_authorization_url(self):
        login = account_server.AgyLoginSession("agy-test")
        login.record_output(b"Open https://accounts.google.com/o/oauth2/auth?state=synthetic\n")
        login.record_output(b"CLI echo: synthetic-private-code\n")

        self.assertEqual(login._output, "")

    async def test_closed_cli_clears_stale_authorization_url(self):
        login = account_server.AgyLoginSession("agy-test")
        login.record_output(b"Open https://accounts.google.com/o/oauth2/auth?state=synthetic\n")
        process = SimpleNamespace(returncode=0)
        login._process = process

        await login._watch(process)

        self.assertEqual(login.snapshot()["state"], "closed")
        self.assertNotIn("authorization_url", login.snapshot())

    async def test_login_only_accepts_a_one_time_alphanumeric_code(self):
        login = account_server.AgyLoginSession("agy-test")
        login._process = SimpleNamespace(returncode=None)
        login._master_fd = 123
        login._state = "awaiting_code"
        login._authorization_url = "https://accounts.google.com/o/oauth2/auth?state=synthetic"

        with patch("ubag_antigravity_cli_adapter.login_session.os.write", return_value=len(b"synthetic-code\r")) as write:
            for code in ("/logout", "synthetic-code\n", "secret with spaces"):
                with self.subTest(code=code), self.assertRaises(ValueError):
                    await login.send_input(code)
            write.assert_not_called()

            response = await login.send_input("synthetic-code")
            write.assert_called_once_with(123, b"synthetic-code\r")
            self.assertEqual(response["state"], "verifying")
            self.assertNotIn("authorization_url", response)
            with self.assertRaises(RuntimeError):
                await login.send_input("synthetic-code")

    async def test_login_start_returns_the_isolated_cli_authorization_url(self):
        reader = asyncio.StreamReader()
        reader.feed_data(b'{"operation":"login_start"}\n')
        reader.feed_eof()
        writer = SimpleNamespace(write=Mock(), drain=AsyncMock(), close=Mock(), wait_closed=AsyncMock())

        login = SimpleNamespace(start=AsyncMock(return_value={
            "state": "awaiting_code",
            "authorization_url": "https://accounts.google.com/o/oauth2/auth?state=synthetic",
        }))
        with patch("ubag_antigravity_cli_adapter.account_server._login_session", login, create=True):
            await handle_connection(reader, writer)

        login.start.assert_awaited_once()
        response = b"".join(call.args[0] for call in writer.write.call_args_list)
        self.assertIn(b'"state": "awaiting_code"', response)
        self.assertIn(b'https://accounts.google.com/o/oauth2/auth', response)
        self.assertNotIn(b'access_token', response)

    async def test_login_input_goes_only_to_the_active_cli_session(self):
        reader = asyncio.StreamReader()
        reader.feed_data(b'{"operation":"login_input","input":"synthetic-code"}\n')
        reader.feed_eof()
        writer = SimpleNamespace(write=Mock(), drain=AsyncMock(), close=Mock(), wait_closed=AsyncMock())

        login = SimpleNamespace(send_input=AsyncMock(return_value={"state": "verifying"}))
        with patch("ubag_antigravity_cli_adapter.account_server._login_session", login, create=True):
            await handle_connection(reader, writer)

        login.send_input.assert_awaited_once_with("synthetic-code")
        output = b"".join(call.args[0] for call in writer.write.call_args_list)
        self.assertIn(b'"state": "verifying"', output)
        self.assertNotIn(b"synthetic-code", output)

    async def test_login_input_rejects_non_code_characters_at_socket(self):
        for value in ("/logout", "code\nnext", "x" * 129):
            with self.subTest(value=value):
                reader = asyncio.StreamReader()
                reader.feed_data((json.dumps({"operation": "login_input", "input": value}) + "\n").encode())
                reader.feed_eof()
                writer = SimpleNamespace(write=Mock(), drain=AsyncMock(), close=Mock(), wait_closed=AsyncMock())
                login = SimpleNamespace(send_input=AsyncMock())
                with patch("ubag_antigravity_cli_adapter.account_server._login_session", login):
                    await handle_connection(reader, writer)
                login.send_input.assert_not_awaited()
                self.assertEqual(writer.write.call_args.args[0], b'{"event":"error","code":"CLI_FAILURE"}\n')

    async def test_login_poll_returns_only_public_state(self):
        reader = asyncio.StreamReader()
        reader.feed_data(b'{"operation":"login_poll"}\n')
        reader.feed_eof()
        writer = SimpleNamespace(write=Mock(), drain=AsyncMock(), close=Mock(), wait_closed=AsyncMock())

        login = SimpleNamespace(poll=AsyncMock(return_value={
            "state": "awaiting_code", "authorization_url": "https://accounts.google.com/o/oauth2/auth?state=synthetic",
        }))
        with patch("ubag_antigravity_cli_adapter.account_server._login_session", login, create=True):
            await handle_connection(reader, writer)

        login.poll.assert_awaited_once_with()
        self.assertNotIn(b'"cursor"', writer.write.call_args.args[0])

    async def test_login_stop_closes_the_isolated_cli_session(self):
        reader = asyncio.StreamReader()
        reader.feed_data(b'{"operation":"login_stop"}\n')
        reader.feed_eof()
        writer = SimpleNamespace(write=Mock(), drain=AsyncMock(), close=Mock(), wait_closed=AsyncMock())

        login = SimpleNamespace(stop=AsyncMock(return_value={"state": "stopped"}))
        with patch("ubag_antigravity_cli_adapter.account_server._login_session", login, create=True):
            await handle_connection(reader, writer)

        login.stop.assert_awaited_once()
        self.assertIn(b'"state": "stopped"', writer.write.call_args.args[0])

    async def test_account_worker_forwards_result_without_credential_fields(self):
        reader = asyncio.StreamReader()
        reader.feed_data(b'{"prompt":"hello","model":"gemini-3.8-flash","effort":"high"}\n')
        reader.feed_eof()
        writer = SimpleNamespace(write=Mock(), drain=AsyncMock(), close=Mock(), wait_closed=AsyncMock())

        class FakeCLI:
            async def send_prompt(self, prompt, *, conversation_id=None):
                yield {"event": "result", "text": "hello", "access_token": "not-for-gateway"}

            async def stop(self):
                pass

        with patch("ubag_antigravity_cli_adapter.account_server.AgyProcessManager", return_value=FakeCLI()) as manager:
            await handle_connection(reader, writer)

        self.assertIn(b'"text": "hello"', writer.write.call_args.args[0])
        self.assertNotIn(b"access_token", writer.write.call_args.args[0])
        self.assertTrue(manager.call_args.args[0].sandbox)

    async def test_unknown_cli_events_block_unsafe_quota_replay(self):
        reader = asyncio.StreamReader()
        reader.feed_data(b'{"prompt":"hello","model":"gemini-3.8-flash","effort":"high"}\n')
        reader.feed_eof()
        writer = SimpleNamespace(write=Mock(), drain=AsyncMock(), close=Mock(), wait_closed=AsyncMock())

        class FakeCLI:
            async def send_prompt(self, prompt, *, conversation_id=None):
                yield {"event": "tool_call", "access_token": "not-for-gateway"}
                yield {"event": "error", "status": 429, "code": "RESOURCE_EXHAUSTED"}

            async def stop(self):
                pass

        with patch("ubag_antigravity_cli_adapter.account_server.AgyProcessManager", return_value=FakeCLI()):
            await handle_connection(reader, writer)

        first = writer.write.call_args_list[0].args[0]
        self.assertEqual(first, b'{"event": "progress"}\n')
        self.assertNotIn(b"access_token", b"".join(call.args[0] for call in writer.write.call_args_list))


class AntigravityCLIAdapterTests(unittest.TestCase):
    def test_ambiguous_cli_errors_are_not_retryable(self):
        class BrokenClient:
            async def send_prompt(self, prompt, *, conversation_id=None):
                raise RuntimeError("connection lost after submission")
                yield

        events = AntigravityCLIAdapter(BrokenClient()).run({
            "job_id": "job-cli-error", "job": {"target": "antigravity_cli", "input": {"prompt": "test"}},
        })
        self.assertEqual(events[-1]["type"], "failed")
        self.assertFalse(events[-1]["data"]["retryable"])

    def test_gateway_adapter_never_starts_local_cli_without_isolated_socket(self):
        payload = {"job_id": "job-cli-isolation", "job": {"target": "antigravity_cli", "input": {"prompt": "test"}}}
        with patch.dict(os.environ, {"UBAG_ANTIGRAVITY_ACCOUNT_SOCKET": ""}):
            with patch("ubag_antigravity_cli_adapter.adapter.AgyProcessManager") as local_process:
                events = AntigravityCLIAdapter().run(payload)

        self.assertEqual(events[-1]["type"], "failed")
        self.assertIn("isolated account socket", events[-1]["data"]["error"])
        local_process.assert_not_called()

    def test_gateway_adapter_uses_account_socket_instead_of_local_cli(self):
        class FakeSocketClient:
            async def send_prompt(self, prompt, *, conversation_id=None):
                yield {"event": "result", "text": "isolated reply"}

            async def stop(self):
                pass

        payload = {"job_id": "job-cli-isolated", "job": {"target": "antigravity_cli", "input": {"prompt": "test"}}}
        with patch.dict(os.environ, {"UBAG_ANTIGRAVITY_ACCOUNT_SOCKET": "/tmp/account.sock"}):
            with patch("ubag_antigravity_cli_adapter.adapter.AgyProcessManager") as local_process:
                with patch("ubag_antigravity_cli_adapter.adapter.AgySocketClient", return_value=FakeSocketClient(), create=True):
                    events = AntigravityCLIAdapter().run(payload)

        self.assertEqual(events[-1]["type"], "completed")
        self.assertEqual(events[-1]["data"]["result"]["text"], "isolated reply")
        local_process.assert_not_called()

    def test_final_result_replaces_incremental_preview(self):
        class FakeProcessManager:
            async def send_prompt(self, prompt, *, conversation_id=None):
                yield {"event": "step_update", "delta": {"text": "partial "}}
                yield {"event": "result", "text": "full response", "conversation_id": "conversation-1"}

        payload = {
            "api_version": "2026-05-22",
            "job_id": "job-cli-test",
            "trace_id": "trace-cli-test",
            "job": {"target": "antigravity_cli", "input": {"prompt": "test"}},
        }
        events = AntigravityCLIAdapter(FakeProcessManager()).run(payload)

        self.assertEqual(events[-1]["type"], "completed")
        self.assertEqual(events[-1]["data"]["result"]["text"], "full response")

    def test_internal_socket_client_is_closed_after_turn(self):
        class FakeSocketClient:
            stopped = False

            async def send_prompt(self, prompt, *, conversation_id=None):
                yield {"event": "result", "text": "done"}

            async def stop(self):
                self.stopped = True

        socket_client = FakeSocketClient()
        with patch.dict(os.environ, {"UBAG_ANTIGRAVITY_ACCOUNT_SOCKET": "/tmp/account.sock"}):
            with patch("ubag_antigravity_cli_adapter.adapter.AgySocketClient", return_value=socket_client):
                events = AntigravityCLIAdapter().run({
                    "job_id": "job-cli-cleanup",
                    "job": {"target": "antigravity_cli", "input": {"prompt": "test"}},
                })

        self.assertEqual(events[-1]["type"], "completed")
        self.assertTrue(socket_client.stopped)

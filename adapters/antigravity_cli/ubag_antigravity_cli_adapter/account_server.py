"""One-account Unix socket worker; run under an account-specific OS user and keyring."""

from __future__ import annotations

import asyncio
import json
import os
import re
import stat
from pathlib import Path
from typing import Any

from .process import AgyProcessManager, CLIConfig


def _public_event(event: dict[str, Any]) -> dict[str, Any] | None:
    event_type = event.get("event")
    if event_type == "step_update":
        delta = event.get("delta")
        if isinstance(delta, dict) and isinstance(delta.get("text"), str):
            return {"event": "step_update", "delta": {"text": delta["text"]}}
    if event_type == "result":
        return {
            "event": "result",
            "text": event.get("text", "") if isinstance(event.get("text", ""), str) else "",
            "stop_reason": event.get("stop_reason", "") if isinstance(event.get("stop_reason", ""), str) else "",
            "conversation_id": event.get("conversation_id", "") if isinstance(event.get("conversation_id", ""), str) else "",
            "usage": {key: value for key, value in event.get("usage", {}).items()
                      if isinstance(value, (int, float))} if isinstance(event.get("usage"), dict) else {},
        }
    if event_type == "error":
        response: dict[str, Any] = {"event": "error"}
        if event.get("status") == 429:
            response["status"] = 429
        if event.get("code") in ("RESOURCE_EXHAUSTED", "QUOTA_EXCEEDED"):
            response["code"] = event["code"]
        return response
    if isinstance(event_type, str):
        return {"event": "progress"}
    return None


async def handle_connection(reader: asyncio.StreamReader, writer: asyncio.StreamWriter) -> None:
    manager: AgyProcessManager | None = None
    try:
        line = await asyncio.wait_for(reader.readline(), timeout=10)
        if len(line) > 1024 * 1024:
            raise ValueError("request is too large")
        request = json.loads(line)
        if not isinstance(request, dict) or set(request) - {"prompt", "model", "effort", "conversation_id"}:
            raise ValueError("invalid request")
        prompt = request.get("prompt")
        model = request.get("model")
        effort = request.get("effort")
        if not isinstance(prompt, str) or not prompt or not isinstance(model, str) or not re.fullmatch(r"[A-Za-z0-9][\w.\-]{0,79}", model):
            raise ValueError("invalid prompt or model")
        if effort not in ("low", "medium", "high", "max"):
            raise ValueError("invalid effort")
        conversation_id = request.get("conversation_id")
        if conversation_id is not None and not isinstance(conversation_id, str):
            raise ValueError("invalid conversation")
        manager = AgyProcessManager(CLIConfig(
            agy_binary=os.environ.get("AGY_BINARY", "agy"), model=model, effort=effort, sandbox=True,
        ))
        async for event in manager.send_prompt(prompt, conversation_id=conversation_id):
            public = _public_event(event)
            if public is not None:
                writer.write((json.dumps(public) + "\n").encode("utf-8"))
                await writer.drain()
                if public["event"] in ("result", "error"):
                    break
    except (Exception, asyncio.CancelledError):
        writer.write(b'{"event":"error","code":"CLI_FAILURE"}\n')
        await writer.drain()
    finally:
        if manager is not None:
            await manager.stop()
        writer.close()
        await writer.wait_closed()


async def serve() -> None:
    account_id = os.environ.get("UBAG_ANTIGRAVITY_ACCOUNT_ID", "")
    socket_dir = os.environ.get("UBAG_ANTIGRAVITY_SOCKET_DIR", "")
    if not re.fullmatch(r"acct_[0-9]+", account_id) or not socket_dir:
        raise RuntimeError("account id and socket directory are required")
    directory = Path(socket_dir)
    directory.mkdir(mode=0o770, parents=True, exist_ok=True)
    path = directory / f"{account_id}.sock"
    try:
        if stat.S_ISSOCK(path.lstat().st_mode):
            path.unlink()
        else:
            raise RuntimeError("account socket path is not a socket")
    except FileNotFoundError:
        pass
    server = await asyncio.start_unix_server(handle_connection, path=str(path), limit=1024 * 1024)
    os.chmod(path, 0o660)
    async with server:
        await server.serve_forever()


if __name__ == "__main__":
    asyncio.run(serve())

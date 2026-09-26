"""Persistent agy CLI process manager for stream-json communication."""

from __future__ import annotations

import asyncio
import json
import os
import signal
from dataclasses import dataclass, field
from typing import Any, AsyncGenerator, Dict, List, Optional


@dataclass
class CLIConfig:
    agy_binary: str = "agy"
    model: str = "gemini-3.8-flash"
    effort: str = "high"
    sandbox: bool = False
    print_timeout: str = "5m"
    env: Dict[str, str] = field(default_factory=dict)


@dataclass
class CLITurnResult:
    text: str = ""
    stop_reason: str = ""
    conversation_id: str = ""
    token_usage: Dict[str, int] = field(default_factory=dict)
    tool_calls: List[Dict[str, Any]] = field(default_factory=list)
    metadata: Dict[str, Any] = field(default_factory=dict)


class AgyQuotaError(RuntimeError):
    pass


class AgySocketClient:
    def __init__(self, socket_path: str, config: CLIConfig) -> None:
        self._socket_path = socket_path
        self._config = config

    async def send_prompt(
        self, prompt: str, *, conversation_id: Optional[str] = None,
    ) -> AsyncGenerator[Dict[str, Any], None]:
        reader, writer = await asyncio.open_unix_connection(self._socket_path, limit=2 * 1024 * 1024)
        try:
            request = {
                "prompt": prompt,
                "model": self._config.model,
                "effort": self._config.effort,
                "conversation_id": conversation_id,
            }
            writer.write((json.dumps(request) + "\n").encode("utf-8"))
            await writer.drain()
            seen_progress = False
            while True:
                line = await reader.readline()
                if not line:
                    raise RuntimeError("isolated CLI worker closed without a result")
                event = json.loads(line)
                if not isinstance(event, dict) or not isinstance(event.get("event"), str):
                    raise RuntimeError("invalid isolated CLI worker event")
                if event["event"] == "error":
                    if event.get("status") == 429 and event.get("code") in (
                        "RESOURCE_EXHAUSTED", "QUOTA_EXCEEDED",
                    ) and not seen_progress:
                        raise AgyQuotaError("OAuth account quota exhausted")
                    raise RuntimeError("isolated CLI worker reported a failure")
                seen_progress = True
                yield event
                if event["event"] == "result":
                    break
        finally:
            writer.close()
            await writer.wait_closed()

    async def stop(self) -> None:
        pass


class AgyProcessManager:
    def __init__(self, config: CLIConfig) -> None:
        self._config = config
        self._process: Optional[asyncio.subprocess.Process] = None
        self._lock = asyncio.Lock()
        self._closed = False
        self._conversation_counter = 0

    async def start(self) -> None:
        if self._process is not None:
            return
        env = os.environ.copy()
        env.update(self._config.env)
        args = [
            self._config.agy_binary,
            "--print",
            "--input-format", "stream-json",
            "--output-format", "stream-json",
            "--model", self._config.model,
            "--effort", self._config.effort,
            "--print-timeout", self._config.print_timeout,
        ]
        if self._config.sandbox:
            args.append("--sandbox")
        self._process = await asyncio.create_subprocess_exec(
            *args,
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.DEVNULL,
            env=env,
        )

    async def stop(self) -> None:
        self._closed = True
        if self._process is not None:
            try:
                self._process.send_signal(signal.SIGTERM)
                await asyncio.wait_for(self._process.wait(), timeout=5.0)
            except (asyncio.TimeoutError, ProcessLookupError):
                try:
                    self._process.kill()
                except ProcessLookupError:
                    pass
            self._process = None

    async def send_prompt(
        self,
        prompt: str,
        *,
        conversation_id: Optional[str] = None,
    ) -> AsyncGenerator[Dict[str, Any], None]:
        if self._closed:
            raise RuntimeError("Process manager is shut down")
        if self._process is None or self._process.stdin is None:
            await self.start()
            if self._process is None or self._process.stdin is None:
                raise RuntimeError("Failed to start agy process")

        self._conversation_counter += 1
        cid = conversation_id or f"ubag_cli_{self._conversation_counter}"

        request = {
            "event": "user",
            "message": {"text": prompt},
            "conversation_id": cid,
        }

        async with self._lock:
            try:
                self._process.stdin.write((json.dumps(request) + "\n").encode("utf-8"))
                await self._process.stdin.drain()

                while True:
                    line = await self._process.stdout.readline()
                    if not line:
                        raise RuntimeError("agy closed output without a result")
                    try:
                        event = json.loads(line.decode("utf-8").strip())
                    except (json.JSONDecodeError, UnicodeDecodeError):
                        continue
                    yield event
                    if event.get("event") == "result":
                        break
            except (BrokenPipeError, ConnectionResetError) as exc:
                raise RuntimeError(f"agy process connection lost: {exc}") from exc

    async def health_check(self) -> bool:
        if self._process is None:
            return False
        return self._process.returncode is None

    @property
    def is_running(self) -> bool:
        return self._process is not None and self._process.returncode is None


__all__ = ["CLIConfig", "CLITurnResult", "AgyProcessManager"]

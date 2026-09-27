"""Human-operated CLI login session in one isolated account worker."""

from __future__ import annotations

import asyncio
import os
import re
import time
from urllib.parse import urlsplit

_URL_PATTERN = re.compile(r"https://[^\s\x1b<>\"']+")
_ANSI_PATTERN = re.compile(r"\x1b\[[0-9;?]*[ -/]*[@-~]")
_MAX_OUTPUT = 32 * 1024
_IDLE_SECONDS = 10 * 60


class AgyLoginSession:
    def __init__(self, agy_binary: str) -> None:
        self._binary = agy_binary
        self._process: asyncio.subprocess.Process | None = None
        self._master_fd: int | None = None
        self._loop: asyncio.AbstractEventLoop | None = None
        self._lock = asyncio.Lock()
        self._output = ""
        self._authorization_url: str | None = None
        self._state = "not_started"
        self._last_activity = time.monotonic()

    @property
    def active(self) -> bool:
        return self._process is not None and self._process.returncode is None

    def record_output(self, output: bytes) -> None:
        if self._authorization_url is not None:
            return
        self._output += output.decode("utf-8", errors="replace")
        if len(self._output) > _MAX_OUTPUT:
            self._output = self._output[-_MAX_OUTPUT:]
        if self._authorization_url is None and self._state in ("starting", "not_started"):
            visible = _ANSI_PATTERN.sub("", self._output)
            for candidate in _URL_PATTERN.findall(visible):
                candidate = candidate.rstrip(".,;)]}")
                try:
                    parsed = urlsplit(candidate)
                    host = parsed.hostname
                    if (parsed.scheme == "https" and parsed.username is None and parsed.password is None
                            and parsed.port in (None, 443) and host is not None
                            and (host == "antigravity.google" or host.endswith(".antigravity.google")
                                 or host == "accounts.google.com")):
                        self._authorization_url = candidate
                        self._state = "awaiting_code"
                        self._output = ""
                        break
                except ValueError:
                    continue

    def snapshot(self) -> dict[str, str]:
        result = {"state": self._state}
        if self._authorization_url is not None:
            result["authorization_url"] = self._authorization_url
        return result

    async def start(self) -> dict[str, str]:
        async with self._lock:
            if self.active:
                self._last_activity = time.monotonic()
                return self.snapshot()
            if os.name != "posix":
                raise RuntimeError("interactive CLI login requires the isolated Linux worker")

            import pty
            import termios

            master, slave = pty.openpty()
            env = os.environ.copy()
            env.pop("GEMINI_API_KEY", None)
            env.pop("GOOGLE_API_KEY", None)
            env.setdefault("TERM", "xterm-256color")
            env.setdefault("SSH_CONNECTION", "127.0.0.1 0 127.0.0.1 0")
            try:
                terminal = termios.tcgetattr(slave)
                terminal[3] &= ~(termios.ECHO | termios.ECHONL)
                termios.tcsetattr(slave, termios.TCSANOW, terminal)
                process = await asyncio.create_subprocess_exec(
                    self._binary, "--sandbox", stdin=slave, stdout=slave, stderr=slave,
                    env=env, start_new_session=True,
                )
            except BaseException:
                os.close(master)
                raise
            finally:
                os.close(slave)

            self._process = process
            self._master_fd = master
            self._loop = asyncio.get_running_loop()
            self._output = ""
            self._authorization_url = None
            self._state = "starting"
            self._last_activity = time.monotonic()
            self._loop.add_reader(master, self._read_output)
            asyncio.create_task(self._watch(process))
            return self.snapshot()

    def _read_output(self) -> None:
        if self._master_fd is None:
            return
        try:
            output = os.read(self._master_fd, 4096)
        except (OSError, BlockingIOError):
            self._detach_reader()
            return
        if output:
            self.record_output(output)
        else:
            self._detach_reader()

    def _detach_reader(self) -> None:
        if self._master_fd is not None:
            if self._loop is not None:
                self._loop.remove_reader(self._master_fd)
            os.close(self._master_fd)
            self._master_fd = None

    async def _watch(self, process: asyncio.subprocess.Process) -> None:
        while process.returncode is None:
            try:
                await asyncio.wait_for(process.wait(), timeout=60)
            except asyncio.TimeoutError:
                if time.monotonic() - self._last_activity > _IDLE_SECONDS:
                    await self.stop()
                    return
        if self._process is process:
            self._state = "closed"
            self._authorization_url = None
            self._output = ""
            self._detach_reader()

    async def poll(self) -> dict[str, str]:
        self._last_activity = time.monotonic()
        return self.snapshot()

    async def send_input(self, value: str) -> dict[str, str]:
        if not self.active or self._master_fd is None:
            raise RuntimeError("CLI login session is not running")
        if self._state != "awaiting_code":
            raise RuntimeError("CLI login is not awaiting an authorization code")
        if not re.fullmatch(r"[A-Za-z0-9-]{1,128}", value):
            raise ValueError("invalid authorization code")
        self._last_activity = time.monotonic()
        data = (value + "\r").encode("ascii")
        if os.write(self._master_fd, data) != len(data):
            raise RuntimeError("CLI login input was not delivered")
        self._state = "verifying"
        self._authorization_url = None
        return self.snapshot()

    async def stop(self) -> dict[str, str]:
        async with self._lock:
            process = self._process
            self._process = None
            self._detach_reader()
            if process is not None and process.returncode is None:
                process.terminate()
                try:
                    await asyncio.wait_for(process.wait(), timeout=5)
                except asyncio.TimeoutError:
                    process.kill()
                    await process.wait()
            self._output = ""
            self._authorization_url = None
            self._state = "stopped"
            return self.snapshot()

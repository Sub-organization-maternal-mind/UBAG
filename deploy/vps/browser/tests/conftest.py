"""Test rig for the voice audio relay: no PulseAudio, parec, FIFO or libopus.

``audio-relay.py`` has a hyphen, so it is loaded by path (fresh module per
test = fresh slot/ready-flag state). libopus is replaced by FakeLib, parec by
FakeProc, the mic FIFO by a plain temp file, and pactl by monkeypatching
``subprocess.run`` inside individual tests. Runs on Windows and Linux with
stdlib + pytest only.

Fake packet format: byte0 = duration in ms (0xFF = corrupt), byte1 = PCM fill.
"""

import ctypes
import importlib.util
import os
import queue
import socket
import sys
import threading
import time

import pytest

HERE = os.path.dirname(os.path.abspath(__file__))
BROWSER_DIR = os.path.dirname(HERE)
if BROWSER_DIR not in sys.path:
    sys.path.insert(0, BROWSER_DIR)

import opus_bridge  # noqa: E402

SECRET = "test-secret"


def wait_for(cond, timeout=4.0):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if cond():
            return True
        time.sleep(0.01)
    return cond()


class FakeLib:
    def __init__(self):
        self.live = set()
        self.destroyed = []
        self.decode_calls = []  # (packet_len, frame_size)
        self.fail_encoder_create = False
        self._next = 100

    def _handle(self):
        self._next += 1
        self.live.add(self._next)
        return self._next

    def opus_decoder_create(self, rate, channels, err):
        return self._handle()

    def opus_encoder_create(self, rate, channels, app, err):
        if self.fail_encoder_create:
            err._obj.value = -1
            return None
        return self._handle()

    def opus_decode(self, handle, packet, n, out, frame_size, fec):
        assert handle in self.live, "use after destroy"
        self.decode_calls.append((n, frame_size))
        if packet[0] == 0xFF:
            return -4  # OPUS_INVALID_PACKET
        samples = packet[0] * 48
        if samples > frame_size:
            return -2  # OPUS_BUFFER_TOO_SMALL
        view = (ctypes.c_int16 * samples).from_address(ctypes.addressof(out))
        for i in range(samples):
            view[i] = packet[1]
        return samples

    def opus_encode(self, handle, pcm, frame_size, out, max_bytes):
        assert handle in self.live, "use after destroy"
        ctypes.memmove(out, b"OP", 2)
        return 2

    def _destroy(self, handle):
        self.destroyed.append(handle)
        self.live.discard(handle)

    opus_decoder_destroy = _destroy
    opus_encoder_destroy = _destroy


class FakeStdout:
    def __init__(self):
        self.q = queue.Queue()
        self.closed = False

    def read(self, n):
        item = self.q.get()  # blocks like parec's pipe until data/EOF
        return b"" if item is None else item

    def close(self):
        self.closed = True
        self.q.put(None)


class FakeProc:
    def __init__(self):
        self.stdout = FakeStdout()
        self.returncode = None
        self.terminated = False

    def poll(self):
        return self.returncode

    def terminate(self):
        self.terminated = True
        self.returncode = -15
        self.stdout.q.put(None)

    def kill(self):
        self.terminate()

    def wait(self, timeout=None):
        return self.returncode

    def feed(self, data):
        self.stdout.q.put(data)

    def die(self):
        self.returncode = 1
        self.stdout.q.put(None)


def pkt(ms=20, fill=7, extra=0):
    return bytes([ms, fill]) + b"\x00" * extra


def raw_frame(ftype, payload):
    return (len(payload) + 1).to_bytes(4, "little") + bytes([ftype]) + payload


class Rig:
    def __init__(self, relay, lib, tmp_path):
        self.relay = relay
        self.lib = lib
        self.procs = []
        self.sessions = []
        self.fifo_path = str(tmp_path / "mic.pcm")
        self.fifo_fds = []
        self.threads = []
        self.clients = []

    # -- client side helpers
    def hello(self, sid="s1", exp=None, secret=SECRET, token=None):
        import json
        exp = int(time.time()) + 60 if exp is None else exp
        token = self.relay.relay_token(secret.encode(), sid, exp) if token is None else token
        body = {"op": "hello", "session_id": sid, "exp": exp, "token": token}
        return raw_frame(2, json.dumps(body).encode())

    def connect(self, handshake=True, **hello_kw):
        """Start handle_session on a socketpair; returns the client socket."""
        server, client = socket.socketpair()
        client.settimeout(4.0)
        self.clients.append(client)
        thread = threading.Thread(target=self.relay.handle_session, args=(server,), daemon=True)
        thread.start()
        self.threads.append(thread)
        if handshake:
            client.sendall(self.hello(**hello_kw))
        return client

    def recv(self, client):
        return self.relay.read_frame(client)

    def recv_control(self, client):
        import json
        ftype, payload = self.recv(client)
        assert ftype == 2
        return json.loads(payload)

    def open_session(self):
        client = self.connect()
        assert self.recv_control(client) == {"op": "ready"}
        return client

    def fifo_bytes(self):
        with open(self.fifo_path, "rb") as fh:
            return fh.read()

    def join_all(self, timeout=5.0):
        for thread in self.threads:
            thread.join(timeout)
        return all(not t.is_alive() for t in self.threads)


@pytest.fixture
def lib(monkeypatch):
    fake = FakeLib()
    monkeypatch.setattr(opus_bridge, "_load_libopus", lambda: fake)
    return fake


@pytest.fixture
def rig(monkeypatch, tmp_path, lib):
    monkeypatch.setenv("UBAG_VOICE_RELAY_SECRET", SECRET)
    spec = importlib.util.spec_from_file_location("audio_relay", os.path.join(BROWSER_DIR, "audio-relay.py"))
    relay = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(relay)
    r = Rig(relay, lib, tmp_path)

    def fake_spawn():
        proc = FakeProc()
        r.procs.append(proc)
        return proc

    def fake_open_mic(timeout_s=2.0):
        fd = os.open(r.fifo_path, os.O_WRONLY | os.O_CREAT | os.O_APPEND | getattr(os, "O_BINARY", 0))
        r.fifo_fds.append(fd)
        return fd

    class RecordingSession(relay.Session):
        def __init__(self, *args, **kwargs):
            super().__init__(*args, **kwargs)
            r.sessions.append(self)

    monkeypatch.setattr(relay, "spawn_monitor", fake_spawn)
    monkeypatch.setattr(relay, "open_mic_fifo", fake_open_mic)
    monkeypatch.setattr(relay, "Session", RecordingSession)
    monkeypatch.setattr(relay, "JOIN_TIMEOUT_S", 1.0)
    relay._devices_ready.set()
    yield r
    for client in r.clients:
        try:
            client.close()
        except OSError:
            pass
    r.join_all()

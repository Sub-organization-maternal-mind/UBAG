#!/usr/bin/env python3
"""UBAG voice audio relay — browser-container side of the voice media plane.

One authenticated framed-TCP connection per voice session; the relay serves
exactly ONE session per audio environment at a time. The gateway forwards the
CLIENT's Opus packets verbatim from the WebRTC leg; we decode them (libopus
via opus_bridge) and write the PCM into the virtual-microphone FIFO Chrome
uses as its mic, so the provider's voice UI hears the client. The provider's
spoken output is captured from the provider sink's MONITOR and encoded back
to Opus frames.

    gateway MediaHub ──framed Opus──► this relay ──PCM──► ubag_virtual_mic ──► provider voice UI
    gateway MediaHub ◄─framed Opus── this relay ◄─Opus── monitor of ubag_provider_sink ◄── provider

WIRE PROTOCOL v2 (breaking vs v1; both directions use the same framing)

  frame   = length:u32-LE  type:u8  payload:(length-1 bytes)
            `length` counts the type byte plus the payload, so 1 <= length
            <= 65536 (64 KiB). length == 0 or > 65536 is a protocol violation
            and the connection is closed.
  type    0x01  audio    payload = exactly one Opus packet (may be 2.5..120 ms)
          0x02  control  payload = one UTF-8 JSON object
          any other type is a protocol violation: connection closed.

  Handshake. The FIRST frame on every connection MUST be control:
      {"op":"hello","session_id":"<str 1..128>","exp":<unix seconds, int>,
       "token":"<hex>"}
    token = hex(HMAC-SHA256(secret, f"voice-relay|{session_id}|{exp}"))
    where secret is the UTF-8 bytes of env UBAG_VOICE_RELAY_SECRET.
    BOUND hello (optional, helper-hosted voice, P5.7): the hello may also
    carry node_id (str 1..128, no "|") and generation (int 1..2**53-1), BOTH
    or neither, and then
    token = hex(HMAC-SHA256(secret, f"voice-relay|{session_id}|{exp}|{node_id}|{generation}"))
    so the credential only verifies for that node and lease generation. A
    helper never holds the global relay secret: its UBAG_VOICE_RELAY_SECRET
    is the primary-derived PER-ATTEMPT relay key (hex string, see
    apps/gateway/internal/voice/attempt.go). When UBAG_VOICE_RELAY_NODE_ID is
    set the relay accepts only a bound hello naming that node (else
    "unauthorized"), and it refuses a hello whose generation is lower than the
    last one accepted for the same session_id ("unauthorized": stale writer).
    An unbound hello is the legacy primary-hosted form and is unchanged.
    The relay FAILS CLOSED: an empty/unset secret refuses every session.
    It also refuses: hello not fully received within 5 s (whole-frame
    deadline), first frame not a control hello, malformed hello, exp already
    in the past or more than 300 s in the future, token mismatch
    (constant-time compare).
    Replies (control, then the relay closes on error):
      {"op":"ready"}                      devices ready, session admitted
      {"op":"error","reason":"<code>"}    then close. Reason codes:
          busy                 another session already owns this environment
                               (also sent before reading hello when the slot
                               is taken, and when too many connections are
                               pending)
          unconfigured         relay has no secret
          bad_hello            wrong first frame / malformed hello / framing
          hello_timeout        no complete hello within 5 s
          expired              exp in the past (or implausibly far ahead)
          unauthorized         token mismatch
          devices_unavailable  virtual mic / provider sink not ready in time
          codec_unavailable    libopus failed to initialise
          mic_unavailable      mic FIFO could not be opened
          monitor_unavailable  parec monitor could not be started
          internal             unexpected failure
    A session may also end later with {"op":"error","reason":"<code>"} sent
    just before close: devices_lost (PulseAudio restarted / device vanished),
    monitor_exited (parec died), mic_closed (mic FIFO reader gone).

  Session (after "ready"):
    gateway → relay   audio    client Opus packet; decoded and written to the
                               mic FIFO (dropped, never blocking, if the FIFO
                               is full). Empty/corrupt packets are dropped
                               and logged; the session continues.
    gateway → relay   control  {"op":"mute","muted":true|false}
                               While muted, incoming audio is decoded and
                               DISCARDED: nothing reaches the mic FIFO. Takes
                               effect strictly in frame order. Starts unmuted.
                               Unknown ops are logged and ignored.
    relay → gateway   audio    20 ms Opus packets of the provider's output.
    Idle timeout: no frame received from the gateway for 30 s
    (UBAG_VOICE_RELAY_IDLE_S) ends the session, as does a write that cannot
    complete within the same timeout. Either side closing the TCP connection
    ends the session; the relay then terminates parec, closes the FIFO fd and
    codec handles, joins its threads, and only then frees the session slot.

Hard safety rules preserved: this process never touches the provider page,
never logs in, never reads credentials — it only moves audio bytes between
TCP and the container's OWN virtual audio devices.

Usage: audio-relay.py [--addr 127.0.0.1:9099]
Environment: UBAG_VOICE_RELAY_ADDR (default 127.0.0.1:9099),
UBAG_VOICE_RELAY_SECRET (required; fail closed when empty),
UBAG_VOICE_RELAY_IDLE_S (default 30),
UBAG_VOICE_RELAY_NODE_ID (optional; helper deployments: require a bound hello
for exactly this node).
Optional, all inert unless set:
  UBAG_VOICE_RELAY_STATS=1   sampled per-frame timings, mic FIFO depth, drops
                             and error counters as one JSON summary line on
                             stderr (/run/ubag/audio-relay.log) every 10 s
                             while a session is active and once when it ends.
                             No protocol change.
  UBAG_VOICE_ENV_ID          per-environment namespace ([A-Za-z0-9_-]{1,32}):
                             devices become ubag_virtual_mic_<id> /
                             ubag_provider_sink_<id> and the default mic FIFO
                             /tmp/ubag-voice-mic-<id>.pcm, so two environments
                             on one host never share names. Invalid = refuse
                             to start. The listen port is derived by
                             entrypoint.sh (UBAG_VOICE_RELAY_PORT_OFFSET).
  UBAG_VOICE_MIC_PIPE        mic FIFO path (default /tmp/ubag-voice-mic.pcm)
  UBAG_VOICE_PAREC           parec executable (default parec)
  UBAG_VOICE_PACTL           pactl executable (default pactl)
Dependencies: pulseaudio (pacat/pactl), libopus0 (via opus_bridge ctypes),
installed by the browser image.
"""

from __future__ import annotations

import argparse
import hashlib
import hmac
import json
import os
import re
import signal
import socket
import struct
import subprocess
import sys
import threading
import time

from opus_bridge import OpusDecoder, OpusEncoder, OpusError

FRAME_HEADER = struct.Struct("<I")
MAX_FRAME_BYTES = 64 * 1024  # length field bound (type byte + payload)
TYPE_AUDIO = 0x01
TYPE_CONTROL = 0x02
SAMPLE_RATE = 48000
CHANNELS = 1
FRAME_MS = 20
FRAME_SAMPLES = SAMPLE_RATE * FRAME_MS // 1000  # 960 @48k

_ENV_ID_RE = re.compile(r"[A-Za-z0-9_-]{1,32}")


def env_names(env_id):
    """(mic source, speaker sink, default mic FIFO path) for one environment.

    An empty/unset id keeps the historic fixed names. A set id namespaces all
    three so two environments sharing a host (or a PulseAudio server / /tmp)
    never collide on devices or the FIFO. An id that is not [A-Za-z0-9_-]{1,32}
    raises: silently falling back to the shared names would defeat isolation.
    """
    env_id = (env_id or "").strip()
    if not env_id:
        return "ubag_virtual_mic", "ubag_provider_sink", "/tmp/ubag-voice-mic.pcm"
    if not _ENV_ID_RE.fullmatch(env_id):
        raise ValueError("UBAG_VOICE_ENV_ID must match [A-Za-z0-9_-]{1,32}")
    return f"ubag_virtual_mic_{env_id}", f"ubag_provider_sink_{env_id}", f"/tmp/ubag-voice-mic-{env_id}.pcm"


# MIC_SOURCE: what Chrome uses as its microphone; SPEAKER_SINK: what Chrome
# plays provider audio into.
MIC_SOURCE, SPEAKER_SINK, _DEFAULT_MIC_PIPE = env_names(os.environ.get("UBAG_VOICE_ENV_ID"))
# Black-box seams: tests and non-standard images can point the relay at fakes.
# Empty/unset keeps the current value, so production behaviour is unchanged.
MIC_PIPE = os.environ.get("UBAG_VOICE_MIC_PIPE") or _DEFAULT_MIC_PIPE
PACTL = os.environ.get("UBAG_VOICE_PACTL") or "pactl"
PAREC = os.environ.get("UBAG_VOICE_PAREC") or "parec"
SPEAKER_MONITOR = f"{SPEAKER_SINK}.monitor"

HELLO_TIMEOUT_S = 5.0
HELLO_MAX_FUTURE_S = 300
MAX_BOUND_GENERATION = 2**53 - 1  # JSON-safe integer bound shared with the gateway
MAX_PENDING = 4              # connections allowed to sit in the handshake
DEVICE_READY_WAIT_S = 8.0    # < the gateway's 10 s ready timeout
HEALTH_INTERVAL_S = 5.0      # device re-check cadence once ready
HEALTH_FAILS_TO_DROP = 2     # consecutive failed checks before "devices lost"
JOIN_TIMEOUT_S = 2.0


def _env_float(name: str, default: float) -> float:
    try:
        value = float(os.environ.get(name, ""))
        return value if value > 0 else default
    except ValueError:
        return default


IDLE_TIMEOUT_S = _env_float("UBAG_VOICE_RELAY_IDLE_S", 30.0)


class HandshakeError(Exception):
    """Session refused; ``reason`` is the wire error code."""

    def __init__(self, reason: str):
        super().__init__(reason)
        self.reason = reason


# --- framing -------------------------------------------------------------

def read_exact(sock: socket.socket, size: int, deadline: float | None = None) -> bytes:
    chunks = []
    remaining = size
    while remaining > 0:
        if deadline is not None:
            left = deadline - time.monotonic()
            if left <= 0:
                raise socket.timeout("deadline exceeded")
            sock.settimeout(left)
        chunk = sock.recv(remaining)
        if not chunk:
            raise ConnectionError("peer closed")
        chunks.append(chunk)
        remaining -= len(chunk)
    return b"".join(chunks)


def read_frame(sock: socket.socket, deadline: float | None = None) -> tuple[int, bytes]:
    """Return (type, payload). Violations raise ConnectionError; a stalled
    peer raises socket.timeout (socket timeout, or ``deadline`` if given)."""
    (length,) = FRAME_HEADER.unpack(read_exact(sock, FRAME_HEADER.size, deadline))
    if length == 0 or length > MAX_FRAME_BYTES:
        raise ConnectionError(f"frame length out of range: {length}")
    body = read_exact(sock, length, deadline)
    ftype = body[0]
    if ftype not in (TYPE_AUDIO, TYPE_CONTROL):
        raise ConnectionError(f"unknown frame type: {ftype:#x}")
    return ftype, body[1:]


def write_frame(sock: socket.socket, ftype: int, payload: bytes) -> None:
    if len(payload) + 1 > MAX_FRAME_BYTES:
        raise ValueError("frame too large")
    sock.sendall(FRAME_HEADER.pack(len(payload) + 1) + bytes((ftype,)) + payload)


def write_control(sock: socket.socket, msg: dict) -> None:
    write_frame(sock, TYPE_CONTROL, json.dumps(msg, separators=(",", ":")).encode("utf-8"))


# --- authentication ------------------------------------------------------

def relay_token(secret: bytes, session_id: str, exp: int,
                node_id: str | None = None, generation: int | None = None) -> str:
    msg = f"voice-relay|{session_id}|{exp}"
    if node_id is not None:  # bound hello: node and lease generation are signed
        msg += f"|{node_id}|{generation}"
    return hmac.new(secret, msg.encode("utf-8"), hashlib.sha256).hexdigest()


# Last accepted (session_id, generation): fences a stale lease generation.
# ponytail: one slot (the relay serves one session at a time); a map if that changes.
_last_bound: tuple[str, int] | None = None


def authenticate(conn: socket.socket) -> str:
    """Read and verify the hello frame; return the session id or raise
    HandshakeError. The 5 s deadline covers the WHOLE frame (slow-drip safe)."""
    secret = os.environ.get("UBAG_VOICE_RELAY_SECRET", "").encode("utf-8")
    if not secret:
        raise HandshakeError("unconfigured")
    try:
        ftype, payload = read_frame(conn, time.monotonic() + HELLO_TIMEOUT_S)
    except socket.timeout:
        raise HandshakeError("hello_timeout")
    except (ConnectionError, OSError):
        raise HandshakeError("bad_hello")
    if ftype != TYPE_CONTROL:
        raise HandshakeError("bad_hello")
    try:
        hello = json.loads(payload.decode("utf-8"))
    except (UnicodeDecodeError, ValueError):
        raise HandshakeError("bad_hello")
    if not isinstance(hello, dict) or hello.get("op") != "hello":
        raise HandshakeError("bad_hello")
    session_id, exp, token = hello.get("session_id"), hello.get("exp"), hello.get("token")
    if (not isinstance(session_id, str) or not 0 < len(session_id) <= 128
            or isinstance(exp, bool) or not isinstance(exp, int)
            or not isinstance(token, str)):
        raise HandshakeError("bad_hello")
    node_id, generation = hello.get("node_id"), hello.get("generation")
    bound = node_id is not None or generation is not None
    if bound and (not isinstance(node_id, str) or not 0 < len(node_id) <= 128 or "|" in node_id
                  or isinstance(generation, bool) or not isinstance(generation, int)
                  or not 0 < generation <= MAX_BOUND_GENERATION):
        raise HandshakeError("bad_hello")
    now = time.time()
    if exp < now or exp > now + HELLO_MAX_FUTURE_S:
        raise HandshakeError("expired")
    expected = relay_token(secret, session_id, exp, node_id, generation) if bound         else relay_token(secret, session_id, exp)
    if not hmac.compare_digest(token.encode("utf-8"), expected.encode("utf-8")):
        raise HandshakeError("unauthorized")
    required_node = os.environ.get("UBAG_VOICE_RELAY_NODE_ID", "")
    if required_node and (not bound or node_id != required_node):
        raise HandshakeError("unauthorized")
    if bound:
        global _last_bound
        if _last_bound is not None and _last_bound[0] == session_id and generation < _last_bound[1]:
            raise HandshakeError("unauthorized")
        _last_bound = (session_id, generation)
    return session_id


# --- PulseAudio devices --------------------------------------------------

def run_checked(*args: str) -> None:
    """Run a pactl command and FAIL VISIBLE on error (startup races and
    unknown modules must surface in the log, not vanish)."""
    result = subprocess.run(list(args), capture_output=True, text=True, timeout=5)
    if result.returncode != 0:
        print(f"audio-relay: {' '.join(args)} failed rc={result.returncode}: "
              f"{(result.stderr or result.stdout or '').strip()}", file=sys.stderr)


_fifo_writer_fd = None

def hold_fifo_writer() -> None:
    """Keep one write end of the mic FIFO open for the process lifetime.

    module-pipe-source needs a writer to initialize and to avoid EOF on the
    reader side; a held fd also makes the setup order-independent (the
    module can load before or after the first session opens). Non-blocking
    open fails with ENXIO until a reader exists, hence the retry.
    """
    global _fifo_writer_fd
    if _fifo_writer_fd is not None:
        return
    deadline = time.monotonic() + 15.0
    while time.monotonic() < deadline:
        try:
            _fifo_writer_fd = os.open(MIC_PIPE, os.O_WRONLY | os.O_NONBLOCK)
            print("audio-relay: holding mic FIFO writer open", file=sys.stderr)
            return
        except OSError:
            time.sleep(0.5)
    print("audio-relay: no reader on the mic FIFO yet; sessions will open it "
          "themselves", file=sys.stderr)


def wait_for_pulse(timeout_s: float = 15.0) -> bool:
    """Poll until the PulseAudio server answers so device setup never races
    daemon startup (observed: a sink could load while a later module call
    still failed)."""
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        try:
            result = subprocess.run([PACTL, "info"], capture_output=True, timeout=3)
            if result.returncode == 0:
                return True
        except (OSError, subprocess.SubprocessError):
            pass
        time.sleep(0.5)
    return False


def pactl_names(kind: str) -> set:
    """Device names from `pactl list short sources|sinks` (exact names, not a
    substring match: `x.monitor` must not satisfy a check for `x`)."""
    out = subprocess.run([PACTL, "list", "short", kind],
                         capture_output=True, text=True, timeout=5).stdout
    return {parts[1] for parts in (line.split() for line in out.splitlines()) if len(parts) > 1}


def devices_ok() -> bool:
    """True only when BOTH the virtual mic source and the provider sink exist
    (whose monitor we capture)."""
    try:
        return MIC_SOURCE in pactl_names("sources") and SPEAKER_SINK in pactl_names("sinks")
    except (OSError, subprocess.SubprocessError):
        return False


def ensure_audio_devices() -> None:
    """Load the virtual mic source and provider sink into PulseAudio.

    Idempotent. The provider null-sink is made the DEFAULT SINK (Chrome's
    voice UI plays into the monitor we capture) and the pipe source is made
    the DEFAULT SOURCE (getUserMedia picks it up without per-site device
    selection).
    """
    if not wait_for_pulse():
        print("audio-relay: PulseAudio did not answer; audio devices NOT configured", file=sys.stderr)
        return
    try:
        sources = pactl_names("sources")
        sinks = pactl_names("sinks")
    except (OSError, subprocess.SubprocessError) as exc:
        print(f"audio-relay: pactl unavailable ({exc}); audio devices not configured", file=sys.stderr)
        return
    if SPEAKER_SINK not in sinks:
        run_checked(PACTL, "load-module", "module-null-sink",
                    f"sink_name={SPEAKER_SINK}",
                    "sink_properties=device.description=UBAG_Provider_Voice")
    if MIC_SOURCE not in sources:
        # module-pipe-source exposes a FIFO as a capture device; the relay's
        # mic pump writes decoded client PCM into that FIFO. The module's
        # init fails when no writer holds the FIFO open, so hold a non-
        # blocking write end FIRST (retrying until the reader exists) and
        # only then load the module.
        try:
            if not os.path.exists(MIC_PIPE):
                os.mkfifo(MIC_PIPE, 0o600)
        except OSError as exc:
            print(f"audio-relay: mkfifo {MIC_PIPE} failed: {exc}", file=sys.stderr)
        hold_fifo_writer()
        loaded = False
        for attempt in range(3):
            result = subprocess.run(
                [PACTL, "load-module", "module-pipe-source",
                 f"source_name={MIC_SOURCE}", f"file={MIC_PIPE}",
                 "format=s16le", f"rate={SAMPLE_RATE}", f"channels={CHANNELS}"],
                capture_output=True, text=True, timeout=5)
            if result.returncode == 0:
                loaded = True
                break
            print(f"audio-relay: module-pipe-source attempt {attempt + 1} failed: "
                  f"{(result.stderr or result.stdout or '').strip()}", file=sys.stderr)
            time.sleep(2)
        if not loaded:
            print("audio-relay: virtual microphone NOT available; sessions will "
                  "have no mic direction", file=sys.stderr)
    run_checked(PACTL, "set-default-source", MIC_SOURCE)
    run_checked(PACTL, "set-default-sink", SPEAKER_SINK)


_devices_ready = threading.Event()
_health_failures = 0


def device_health_step() -> None:
    """One health iteration. Not ready: ensure devices, mark ready when both
    exist. Ready: re-check; HEALTH_FAILS_TO_DROP consecutive failures clear
    the ready flag (active sessions then end with devices_lost, new ones are
    refused) and the next iterations re-ensure the devices (PulseAudio
    restart recovery)."""
    global _health_failures
    if _devices_ready.is_set():
        if devices_ok():
            _health_failures = 0
            return
        _health_failures += 1
        if _health_failures < HEALTH_FAILS_TO_DROP:
            return
        _devices_ready.clear()
        print("audio-relay: audio devices LOST; ending sessions and re-ensuring", file=sys.stderr)
    try:
        ensure_audio_devices()
        if devices_ok():
            _health_failures = 0
            _devices_ready.set()
            print("audio-relay: audio devices ready", file=sys.stderr)
        else:
            print("audio-relay: device ensure incomplete; retrying", file=sys.stderr)
    except (OSError, subprocess.SubprocessError) as exc:
        print(f"audio-relay: device ensure failed: {exc}; retrying", file=sys.stderr)


def ensure_devices_forever() -> None:
    """Keep the virtual audio devices present AND healthy for the process
    lifetime (a late PulseAudio boot or a later restart must not leave the
    relay serving sessions with no devices)."""
    while True:
        device_health_step()
        time.sleep(HEALTH_INTERVAL_S if _devices_ready.is_set() else 3)


# --- sessions ------------------------------------------------------------

_session_slot = threading.Lock()  # one active session per audio environment


def open_mic_fifo(timeout_s: float = 2.0) -> int:
    """Non-blocking write end of the mic FIFO (ENXIO until the pipe source
    holds the read end, hence the short retry). Never blocks a thread that
    cannot be interrupted."""
    deadline = time.monotonic() + timeout_s
    while True:
        try:
            return os.open(MIC_PIPE, os.O_WRONLY | getattr(os, "O_NONBLOCK", 0))
        except OSError:
            if time.monotonic() >= deadline:
                raise
            time.sleep(0.1)


def spawn_monitor() -> subprocess.Popen:
    return subprocess.Popen(
        [PAREC, "--raw", "--format=s16le", f"--rate={SAMPLE_RATE}",
         f"--channels={CHANNELS}", f"--device={SPEAKER_MONITOR}",
         "--latency-msec=20", "--stream-name=ubag-voice-provider"],
        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)


# --- opt-in measurement (UBAG_VOICE_RELAY_STATS=1) -----------------------

STATS_INTERVAL_S = 10.0
STATS_SAMPLE_EVERY = 10   # time 1 frame in N: two perf_counter calls per sample
STATS_MAX_SAMPLES = 512   # bounded per direction per window


def stats_enabled() -> bool:
    return os.environ.get("UBAG_VOICE_RELAY_STATS", "").strip().lower() in ("1", "true", "yes", "on")


def _fifo_depth(fd: int | None) -> int | None:
    """Unread bytes queued in the mic FIFO (Linux FIONREAD); None if unknown."""
    if fd is None:
        return None
    try:
        import fcntl
        import termios
        buf = bytearray(4)
        fcntl.ioctl(fd, termios.FIONREAD, buf)
        return int.from_bytes(buf, sys.byteorder)
    except (ImportError, OSError, ValueError):
        return None


def _pctl_ms(values: list, q: float) -> float:
    ordered = sorted(values)
    return round(ordered[min(len(ordered) - 1, int(q * len(ordered)))] * 1000, 3)


class RelayStats:
    """Per-session counters plus sampled timings. Each pump thread is the only
    writer of its own direction's counters; the supervising thread reads them.
    Only 1 frame in STATS_SAMPLE_EVERY is timed (two perf_counter() calls);
    every other frame costs one int increment.

    mic     = decode + FIFO write (frame already read from the gateway)
    speaker = Opus encode + gateway socket write (PCM already read from parec)
    """

    def __init__(self, session_id: str, clock=time.monotonic):
        self.session_id = session_id
        self._clock = clock
        self._lock = threading.Lock()
        self._next_emit = clock() + STATS_INTERVAL_S
        self.counters = {
            "mic_frames": 0, "mic_decode_errors": 0, "mic_fifo_dropped": 0,
            "mic_muted_discarded": 0, "speaker_frames": 0, "speaker_encode_errors": 0,
        }
        self._samples = {"mic": [], "speaker": []}

    def tick(self, name: str) -> bool:
        """Count one frame under ``name``; True when this frame is to be timed."""
        self.counters[name] += 1
        return self.counters[name] % STATS_SAMPLE_EVERY == 0

    def observe(self, direction: str, seconds: float) -> None:
        with self._lock:
            samples = self._samples[direction]
            if len(samples) < STATS_MAX_SAMPLES:
                samples.append(seconds)

    def summary(self, mic_fd: int | None = None) -> dict:
        with self._lock:
            window, self._samples = self._samples, {"mic": [], "speaker": []}
        out = {"event": "relay_stats", "session_id": self.session_id,
               "interval_s": STATS_INTERVAL_S, "sample_every": STATS_SAMPLE_EVERY,
               "mic_fifo_bytes": _fifo_depth(mic_fd), **self.counters}
        for direction, values in window.items():
            out[f"{direction}_samples"] = len(values)
            if values:
                out[f"{direction}_p50_ms"] = _pctl_ms(values, 0.50)
                out[f"{direction}_p95_ms"] = _pctl_ms(values, 0.95)
                out[f"{direction}_max_ms"] = round(max(values) * 1000, 3)
        return out

    def maybe_emit(self, mic_fd: int | None = None, force: bool = False) -> None:
        now = self._clock()
        if not force and now < self._next_emit:
            return
        self._next_emit = now + STATS_INTERVAL_S
        print("audio-relay: stats " + json.dumps(self.summary(mic_fd), separators=(",", ":")),
              file=sys.stderr, flush=True)


class Session:
    """One admitted voice session. Every resource starts as None and close()
    is deterministic and idempotent, so a failure at ANY construction step
    still releases whatever was acquired."""

    def __init__(self, conn: socket.socket, session_id: str):
        self.conn = conn
        self.session_id = session_id
        self.stop = threading.Event()
        self.muted = False
        self.fail_reason: str | None = None
        self.decoder = None
        self.encoder = None
        self.mic_fd: int | None = None
        self.stats = RelayStats(session_id) if stats_enabled() else None
        self.procs: list = []
        self.threads: list = []
        self._write_lock = threading.Lock()
        self._close_lock = threading.Lock()
        self._closed = False

    # -- setup ----
    def start(self) -> None:
        try:
            self.decoder = OpusDecoder(SAMPLE_RATE, CHANNELS)
            self.encoder = OpusEncoder(SAMPLE_RATE, CHANNELS)
        except (OpusError, OSError) as exc:
            print(f"audio-relay: codec init failed: {exc}", file=sys.stderr)
            raise HandshakeError("codec_unavailable")
        try:
            self.mic_fd = open_mic_fifo()
        except OSError as exc:
            print(f"audio-relay: mic FIFO open failed: {exc}", file=sys.stderr)
            raise HandshakeError("mic_unavailable")
        try:
            self.procs.append(spawn_monitor())
        except OSError as exc:
            print(f"audio-relay: parec start failed: {exc}", file=sys.stderr)
            raise HandshakeError("monitor_unavailable")
        self.conn.settimeout(IDLE_TIMEOUT_S)  # reads AND writes: no stalled peer holds us
        self._send(lambda: write_control(self.conn, {"op": "ready"}))
        self.threads = [threading.Thread(target=self._pump_mic, daemon=True),
                        threading.Thread(target=self._pump_speaker, daemon=True)]
        for thread in self.threads:
            thread.start()

    def run(self) -> None:
        """Block until the session ends; watch device health meanwhile."""
        while not self.stop.wait(0.5):
            if not _devices_ready.is_set():
                self.fail("devices_lost")
            if self.stats:
                self.stats.maybe_emit(self.mic_fd)

    # -- helpers ----
    def _send(self, fn) -> None:
        with self._write_lock:
            fn()

    def fail(self, reason: str) -> None:
        """End the session visibly: best-effort error frame, then stop."""
        if self.fail_reason is None:
            self.fail_reason = reason
            print(f"audio-relay: session {self.session_id} ending: {reason}", file=sys.stderr)
            try:
                self._send(lambda: write_control(self.conn, {"op": "error", "reason": reason}))
            except (OSError, ValueError):
                pass
        self.stop.set()

    def _handle_control(self, payload: bytes) -> None:
        try:
            msg = json.loads(payload.decode("utf-8"))
        except (UnicodeDecodeError, ValueError):
            print("audio-relay: ignoring malformed control frame", file=sys.stderr)
            return
        if isinstance(msg, dict) and msg.get("op") == "mute" and isinstance(msg.get("muted"), bool):
            self.muted = msg["muted"]
        else:
            print("audio-relay: ignoring unknown control message", file=sys.stderr)

    # -- pumps ----
    def _pump_mic(self) -> None:
        """Gateway Opus frames → decoded PCM → the module-pipe-source FIFO.

        The FIFO IS the virtual microphone: module-pipe-source holds the
        read end. No pacat here — pacat plays to SINKS; the mic direction
        must feed the SOURCE device. Control frames (mute) are handled in
        this same thread, so mute takes effect strictly in frame order.
        """
        bad = dropped = 0
        stats = self.stats
        try:
            while not self.stop.is_set():
                ftype, payload = read_frame(self.conn)
                if ftype == TYPE_CONTROL:
                    self._handle_control(payload)
                    continue
                timed = stats.tick("mic_frames") if stats else False
                started = time.perf_counter() if timed else 0.0
                try:
                    pcm = self.decoder.decode(payload)  # also keeps decoder state while muted
                except OpusError as exc:
                    if stats:
                        stats.counters["mic_decode_errors"] += 1
                    bad += 1
                    if bad == 1 or bad % 100 == 0:
                        print(f"audio-relay: mic frame decode failed ({bad}): {exc}", file=sys.stderr)
                    continue
                if self.muted or not pcm:
                    if stats and self.muted:
                        stats.counters["mic_muted_discarded"] += 1
                    continue
                try:
                    os.write(self.mic_fd, pcm)
                    if timed:
                        stats.observe("mic", time.perf_counter() - started)
                except BlockingIOError:
                    if stats:
                        stats.counters["mic_fifo_dropped"] += 1
                    dropped += 1  # reader slow: drop, never block the pump
                    if dropped == 1 or dropped % 100 == 0:
                        print(f"audio-relay: mic FIFO full, dropped {dropped} frames", file=sys.stderr)
                except OSError:
                    self.fail("mic_closed")
                    return
        except socket.timeout:
            print("audio-relay: mic pump idle timeout", file=sys.stderr)
        except Exception as exc:  # a media thread crash must be visible
            if not self.stop.is_set():
                print(f"audio-relay: mic pump ended: {type(exc).__name__}: {exc}", file=sys.stderr)
        finally:
            self.stop.set()

    def _pump_speaker(self) -> None:
        """Sink-monitor PCM → 20 ms Opus frames → gateway."""
        pcm_bytes = FRAME_SAMPLES * CHANNELS * 2  # s16le mono
        monitor = self.procs[0]
        stats = self.stats
        try:
            while not self.stop.is_set():
                chunk = monitor.stdout.read(pcm_bytes)
                if not chunk:
                    if not self.stop.is_set():
                        self.fail("monitor_exited")
                    return
                if len(chunk) < pcm_bytes:
                    chunk = chunk + b"\x00" * (pcm_bytes - len(chunk))
                timed = stats.tick("speaker_frames") if stats else False
                started = time.perf_counter() if timed else 0.0
                try:
                    frame = self.encoder.encode(chunk, FRAME_SAMPLES)
                except OpusError:
                    if stats:
                        stats.counters["speaker_encode_errors"] += 1
                    continue
                self._send(lambda: write_frame(self.conn, TYPE_AUDIO, frame))
                if timed:
                    stats.observe("speaker", time.perf_counter() - started)
        except Exception as exc:  # a media thread crash must be visible
            if not self.stop.is_set():
                print(f"audio-relay: speaker pump ended: {type(exc).__name__}: {exc}", file=sys.stderr)
        finally:
            self.stop.set()

    # -- teardown ----
    def close(self) -> None:
        """Release everything exactly once: signal, unblock, join, destroy."""
        with self._close_lock:
            if self._closed:
                return
            self._closed = True
        self.stop.set()
        if self.stats:  # final summary while the FIFO fd is still open
            self.stats.maybe_emit(self.mic_fd, force=True)
        for fn in (lambda: self.conn.shutdown(socket.SHUT_RDWR), self.conn.close):
            try:  # shutdown wakes recv/sendall on Linux; close does on Windows
                fn()
            except OSError:
                pass
        for proc in self.procs:  # unblocks the speaker pump's stdout read
            try:
                if proc.poll() is None:
                    proc.terminate()
                    try:
                        proc.wait(timeout=2)
                    except subprocess.TimeoutExpired:
                        proc.kill()
                        proc.wait(timeout=2)
            except Exception as exc:
                print(f"audio-relay: parec cleanup failed: {exc}", file=sys.stderr)
        me = threading.current_thread()
        for thread in self.threads:
            if thread is not me:
                thread.join(JOIN_TIMEOUT_S)
                if thread.is_alive():
                    print("audio-relay: pump thread did not exit in time", file=sys.stderr)
        for proc in self.procs:
            try:
                if proc.stdout:
                    proc.stdout.close()
            except Exception:
                pass
        if self.mic_fd is not None:
            try:
                os.close(self.mic_fd)
            except OSError:
                pass
            self.mic_fd = None
        for codec in (self.decoder, self.encoder):  # lock-serialized vs in-flight calls
            if codec is not None:
                try:
                    codec.close()
                except Exception as exc:
                    print(f"audio-relay: codec close failed: {exc}", file=sys.stderr)
        self.decoder = self.encoder = None
        self.procs = []


def reject(conn: socket.socket, reason: str, drain: bool = True) -> None:
    """Best-effort error frame (short timeout, never blocks) then close.

    ``drain`` does a graceful half-close and briefly reads out whatever the
    peer already sent (the unread hello): closing with unread data makes the
    kernel send RST, which can destroy the error frame before the peer reads
    it. Skipped on the accept thread, which must never wait."""
    try:
        conn.settimeout(1.0)
        write_control(conn, {"op": "error", "reason": reason})
        if drain:
            conn.shutdown(socket.SHUT_WR)
            end = time.monotonic() + 0.5
            while time.monotonic() < end:
                conn.settimeout(max(end - time.monotonic(), 0.01))
                if not conn.recv(4096):
                    break
    except (OSError, ValueError):
        pass
    for fn in (lambda: conn.shutdown(socket.SHUT_RDWR), conn.close):
        try:
            fn()
        except OSError:
            pass


def handle_session(conn: socket.socket, pending: threading.Semaphore | None = None) -> None:
    """Authenticate, admit (one per environment), then pump until either side
    ends. ``pending`` is the accept loop's handshake-slot semaphore, released
    as soon as the handshake phase ends (or the connection is refused)."""
    released = False

    def release_pending() -> None:
        nonlocal released
        if pending is not None and not released:
            released = True
            pending.release()

    session: Session | None = None
    slot = False
    refusal: str | None = None
    try:
        if _session_slot.locked():  # cheap early refusal; the acquire below is authoritative
            raise HandshakeError("busy")
        session_id = authenticate(conn)
        release_pending()
        if not _session_slot.acquire(blocking=False):
            raise HandshakeError("busy")
        slot = True
        if not _devices_ready.wait(timeout=DEVICE_READY_WAIT_S):
            raise HandshakeError("devices_unavailable")
        session = Session(conn, session_id)
        session.start()
        session.run()
    except HandshakeError as exc:
        refusal = exc.reason
    except Exception as exc:
        print(f"audio-relay: session failed: {type(exc).__name__}: {exc}", file=sys.stderr)
        refusal = "internal"
    finally:
        release_pending()
        try:
            if session is not None:
                if refusal:
                    session.fail(refusal)  # error frame, then stop
                session.close()
            if refusal:
                print(f"audio-relay: session refused: {refusal}", file=sys.stderr)
        finally:
            if slot:
                _session_slot.release()  # only after full cleanup: next session owns the FIFO
        if session is None:  # no slot held any more: a slow-closing peer costs nothing
            if refusal:
                reject(conn, refusal)
            else:
                conn.close()


def make_server(addr: str) -> socket.socket:
    host, port = addr.rsplit(":", 1)
    server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind((host, int(port)))
    server.listen(MAX_PENDING)  # bounded backlog
    return server


def serve_forever(server: socket.socket, stop: threading.Event | None = None) -> None:
    pending = threading.BoundedSemaphore(MAX_PENDING)
    if stop is not None:
        server.settimeout(0.2)
    while stop is None or not stop.is_set():
        try:
            conn, peer = server.accept()
        except socket.timeout:
            continue
        except OSError as exc:
            if stop is not None and stop.is_set():
                return
            print(f"audio-relay: accept failed: {exc}", file=sys.stderr)
            time.sleep(0.1)
            continue
        if not pending.acquire(blocking=False):  # too many unauthenticated peers
            reject(conn, "busy", drain=False)
            continue
        print(f"audio-relay: connection from {peer}", flush=True)
        threading.Thread(target=handle_session, args=(conn, pending), daemon=True).start()


def serve(addr: str) -> None:
    server = make_server(addr)
    print(f"audio-relay: listening on {addr} (mic={MIC_SOURCE} monitor={SPEAKER_MONITOR})", flush=True)
    serve_forever(server)


def main() -> int:
    parser = argparse.ArgumentParser(description="UBAG voice audio relay")
    parser.add_argument("--addr", default=os.environ.get("UBAG_VOICE_RELAY_ADDR", "127.0.0.1:9099"))
    args = parser.parse_args()
    if not os.environ.get("UBAG_VOICE_RELAY_SECRET"):
        print("audio-relay: UBAG_VOICE_RELAY_SECRET is empty; refusing ALL sessions (fail closed)",
              file=sys.stderr)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    threading.Thread(target=ensure_devices_forever, daemon=True).start()
    serve(args.addr)
    return 0


if __name__ == "__main__":
    sys.exit(main())

#!/usr/bin/env python3
"""UBAG voice audio relay — browser-container side of the voice media plane.

One framed-TCP connection per voice session: 4-byte little-endian length +
one Opus packet per frame, both directions (matching the gateway MediaHub's
framed protocol exactly). The gateway forwards the CLIENT's Opus packets
verbatim from the WebRTC leg; we decode them (libopus via opuslib) and play
the resulting PCM into the virtual-microphone source Chrome uses as its mic,
so the provider's voice UI hears the client. The provider's spoken output is
captured from the provider sink's MONITOR and encoded back to Opus frames.

    gateway MediaHub ──framed Opus──► this relay ──PCM──► ubag_virtual_mic ──► provider voice UI
    gateway MediaHub ◄─framed Opus── this relay ◄─Opus── monitor of ubag_provider_sink ◄── provider

Hard safety rules preserved: this process never touches the provider page,
never logs in, never reads credentials — it only moves audio bytes between
TCP and the container's OWN virtual audio devices.

Usage: audio-relay.py [--addr 127.0.0.1:9099]
Environment: UBAG_VOICE_RELAY_ADDR (default 127.0.0.1:9099).
Dependencies: pulseaudio (pacat/pactl), python3-opuslib + libopus0,
installed by the browser image.
"""

from __future__ import annotations

import argparse
import os
import signal
import socket
import struct
import subprocess
import sys
import threading

from opus_bridge import OpusDecoder, OpusEncoder, OpusError

FRAME_HEADER = struct.Struct("<I")
MAX_FRAME_BYTES = 64 * 1024
SAMPLE_RATE = 48000
CHANNELS = 1
FRAME_MS = 20
FRAME_SAMPLES = SAMPLE_RATE * FRAME_MS // 1000  # 960 @48k

MIC_SOURCE = "ubag_virtual_mic"      # what Chrome uses as its microphone
MIC_PIPE = "/tmp/ubag-voice-mic.pcm"
SPEAKER_SINK = "ubag_provider_sink"  # what Chrome plays provider audio into
SPEAKER_MONITOR = f"{SPEAKER_SINK}.monitor"


def read_exact(sock: socket.socket, size: int) -> bytes:
    chunks = []
    remaining = size
    while remaining > 0:
        chunk = sock.recv(remaining)
        if not chunk:
            raise ConnectionError("peer closed")
        chunks.append(chunk)
        remaining -= len(chunk)
    return b"".join(chunks)


def read_frame(sock: socket.socket) -> bytes:
    (length,) = FRAME_HEADER.unpack(read_exact(sock, FRAME_HEADER.size))
    if length == 0 or length > MAX_FRAME_BYTES:
        raise ConnectionError(f"frame length out of range: {length}")
    return read_exact(sock, length)


def write_frame(sock: socket.socket, frame: bytes) -> None:
    sock.sendall(FRAME_HEADER.pack(len(frame)) + frame)


def run_quiet(*args: str) -> None:
    subprocess.run(list(args), capture_output=True, timeout=5)


def ensure_audio_devices() -> None:
    """Load the virtual mic source and provider sink into PulseAudio.

    Idempotent. The provider null-sink is made the DEFAULT SINK (Chrome's
    voice UI plays into the monitor we capture) and the pipe source is made
    the DEFAULT SOURCE (getUserMedia picks it up without per-site device
    selection).
    """
    try:
        sources = subprocess.run(["pactl", "list", "short", "sources"],
                                 capture_output=True, text=True, timeout=5).stdout
        sinks = subprocess.run(["pactl", "list", "short", "sinks"],
                               capture_output=True, text=True, timeout=5).stdout
    except (OSError, subprocess.SubprocessError) as exc:
        print(f"audio-relay: pactl unavailable ({exc}); audio devices not configured", file=sys.stderr)
        return
    if SPEAKER_SINK not in sinks:
        run_quiet("pactl", "load-module", "module-null-sink",
                  f"sink_name={SPEAKER_SINK}",
                  "sink_properties=device.description=UBAG_Provider_Voice")
    if MIC_SOURCE not in sources:
        # module-pipe-source exposes a FIFO as a capture device; the relay's
        # mic pump writes decoded client PCM into that FIFO.
        try:
            if not os.path.exists(MIC_PIPE):
                os.mkfifo(MIC_PIPE, 0o600)
        except OSError:
            pass
        run_quiet("pactl", "load-module", "module-pipe-source",
                  f"source_name={MIC_SOURCE}", f"file={MIC_PIPE}",
                  "format=s16le", f"rate={SAMPLE_RATE}", f"channels={CHANNELS}")
    run_quiet("pactl", "set-default-source", MIC_SOURCE)
    run_quiet("pactl", "set-default-sink", SPEAKER_SINK)


def handle_session(conn: socket.socket) -> None:
    """One voice session's bidirectional audio pump."""
    mic_proc: subprocess.Popen | None = None
    monitor_proc: subprocess.Popen | None = None
    procs: list[subprocess.Popen] = []
    try:
        decoder = OpusDecoder(SAMPLE_RATE, CHANNELS)
        encoder = OpusEncoder(SAMPLE_RATE, CHANNELS)

        mic_proc = subprocess.Popen(
            ["pacat", "--raw", "--format=s16le", f"--rate={SAMPLE_RATE}",
             f"--channels={CHANNELS}", f"--device={MIC_SOURCE}",
             "--latency-msec=20", "--stream-name=ubag-voice-client"],
            stdin=subprocess.PIPE, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        procs.append(mic_proc)
        monitor_proc = subprocess.Popen(
            ["parec", "--raw", "--format=s16le", f"--rate={SAMPLE_RATE}",
             f"--channels={CHANNELS}", f"--device={SPEAKER_MONITOR}",
             "--latency-msec=20", "--stream-name=ubag-voice-provider"],
            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        procs.append(monitor_proc)

        stop = threading.Event()

        def pump_mic() -> None:
            """Gateway Opus frames → decoded PCM → virtual microphone."""
            try:
                while not stop.is_set():
                    frame = read_frame(conn)
                    pcm = decoder.decode(frame, FRAME_SAMPLES)
                    try:
                        mic_proc.stdin.write(pcm)
                        mic_proc.stdin.flush()
                    except (BrokenPipeError, ValueError, OSError):
                        return
            except (ConnectionError, OSError, OpusError):
                stop.set()

        def pump_speaker() -> None:
            """Sink-monitor PCM → 20 ms Opus frames → gateway."""
            pcm_bytes = FRAME_SAMPLES * CHANNELS * 2  # s16le mono
            try:
                while not stop.is_set():
                    chunk = monitor_proc.stdout.read(pcm_bytes)
                    if not chunk:
                        return
                    if len(chunk) < pcm_bytes:
                        chunk = chunk + b"\x00" * (pcm_bytes - len(chunk))
                    try:
                        frame = encoder.encode(chunk, FRAME_SAMPLES)
                    except OpusError:
                        continue
                    write_frame(conn, frame)
            except (ConnectionError, OSError):
                stop.set()

        threads = [threading.Thread(target=pump_mic, daemon=True),
                   threading.Thread(target=pump_speaker, daemon=True)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
    finally:
        for proc in procs:
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    proc.kill()
        try:
            conn.close()
        except Exception:
            pass


def serve(addr: str) -> None:
    host, port = addr.rsplit(":", 1)
    server = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind((host, int(port)))
    server.listen(4)  # bounded: one connection per active voice session
    print(f"audio-relay: listening on {addr} (mic={MIC_SOURCE} monitor={SPEAKER_MONITOR})", flush=True)
    while True:
        conn, peer = server.accept()
        print(f"audio-relay: session connected from {peer}", flush=True)
        threading.Thread(target=handle_session, args=(conn,), daemon=True).start()


def main() -> int:
    parser = argparse.ArgumentParser(description="UBAG voice audio relay")
    parser.add_argument("--addr", default=os.environ.get("UBAG_VOICE_RELAY_ADDR", "127.0.0.1:9099"))
    args = parser.parse_args()
    ensure_audio_devices()
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    serve(args.addr)
    return 0


if __name__ == "__main__":
    sys.exit(main())

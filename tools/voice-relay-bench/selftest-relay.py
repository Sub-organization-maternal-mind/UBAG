#!/usr/bin/env python3
"""Offline stand-in launcher for the A/B harness self-test (perf-fleet P7.7). NOT a measurement tool.

Runs deploy/vps/browser/audio-relay.py UNMODIFIED, with two substitutions so it works on a box without libopus (and on Windows):
  * libopus is replaced by a deterministic, stateful pseudo-codec. Decode output depends on the packet bytes and on every earlier
    packet; encode output depends on the PCM and on every earlier frame. Any reordering, loss, duplication or mute error therefore
    changes the bytes, which is what lets the byte-compare of the harness prove itself.
  * Windows only: the mic "FIFO" (a regular file there) is opened in binary append mode, so every session adds to the end that the
    harness sink tails (a POSIX FIFO has no offset; an offset-0 reopen would overwrite what the sink already read).
UBAG_SELFTEST_MUTATE=speaker|mic|mute breaks one behaviour on purpose: the negative control that proves the compare can fail.
"""
import ctypes
import importlib.util
import os
import struct
import sys
import zlib

HERE = os.path.dirname(os.path.abspath(__file__))
BROWSER = os.path.abspath(os.path.join(HERE, "..", "..", "deploy", "vps", "browser"))
sys.path.insert(0, BROWSER)
import opus_bridge  # noqa: E402

MUTATE = os.environ.get("UBAG_SELFTEST_MUTATE", "")


def units(config):
    """Frame duration of a TOC config in half-milliseconds (RFC 6716 table)."""
    if config < 12:
        return (20, 40, 80, 120)[config % 4]
    if config < 16:
        return (20, 40)[config % 2]
    return (5, 10, 20, 40)[config % 4]


def parse(packet):
    """Samples per channel at 48 kHz, or None for a packet libopus would reject."""
    n, toc = len(packet), packet[0]
    code, u = toc & 3, units(toc >> 3)
    if code == 0:
        frames = 1 if n - 1 <= 1275 else None
    elif code == 1:
        frames = 2 if (n - 1) % 2 == 0 else None
    elif code == 2:
        frames = None
        if n >= 2:
            first = packet[1]
            if first < 252:
                frames = 2 if first <= n - 2 else None
            elif n >= 3:
                frames = 2 if 4 * packet[2] + first <= n - 3 else None
    else:
        count = packet[1] & 0x3F if n >= 2 else 0
        frames = count if 0 < count and count * u <= 240 else None
    return None if frames is None else frames * u * 24


def lcg(x):
    return (x * 1103515245 + 12345) & 0x7FFFFFFF


class StatefulFakeLib:
    def __init__(self):
        self.state = {}
        self.calls = {}
        self._next = 100

    def _create(self):
        self._next += 1
        self.state[self._next] = 0
        self.calls[self._next] = 0
        return self._next

    def opus_decoder_create(self, rate, channels, err):
        return self._create()

    def opus_encoder_create(self, rate, channels, app, err):
        return self._create()

    def opus_decode(self, handle, packet, n, out, frame_size, fec):
        samples = parse(bytes(packet))
        if samples is None:
            return -4  # OPUS_INVALID_PACKET
        if samples > frame_size:
            return -2  # OPUS_BUFFER_TOO_SMALL
        crc = zlib.crc32(bytes(packet), self.state[handle])
        self.state[handle] = crc
        self.calls[handle] += 1
        x, vals = crc or 1, []
        for _ in range(samples):
            x = lcg(x)
            vals.append(((x >> 8) & 0xFFFF) - 32768)
        data = bytearray(struct.pack("<%dh" % samples, *vals))
        if MUTATE == "mic" and self.calls[handle] == 5:
            data[0] ^= 1
        ctypes.memmove(out, bytes(data), len(data))
        return samples

    def opus_encode(self, handle, pcm, frame_size, out, max_bytes):
        crc = zlib.crc32(bytes(pcm), self.state[handle])
        self.state[handle] = crc
        self.calls[handle] += 1
        x, body = crc or 1, bytearray([31 << 3])  # CELT, fullband, 20 ms, one frame
        for _ in range(1 + crc % 38):
            x = lcg(x)
            body.append((x >> 8) & 0xFF)
        if MUTATE == "speaker" and self.calls[handle] == 7:
            body[1] ^= 1
        ctypes.memmove(out, bytes(body), len(body))
        return len(body)

    def _destroy(self, handle):
        self.state.pop(handle, None)

    opus_decoder_destroy = _destroy
    opus_encoder_destroy = _destroy


opus_bridge._load_libopus = lambda lib=StatefulFakeLib(): lib
spec = importlib.util.spec_from_file_location("audio_relay", os.path.join(BROWSER, "audio-relay.py"))
relay = importlib.util.module_from_spec(spec)
spec.loader.exec_module(relay)
if os.name == "nt":
    relay.open_mic_fifo = lambda timeout_s=2.0: os.open(relay.MIC_PIPE, os.O_WRONLY | os.O_BINARY | os.O_APPEND)
if MUTATE == "mute":
    relay.Session._handle_control = lambda self, payload: None  # mute is ignored: decoded audio reaches the mic

if __name__ == "__main__":
    sys.exit(relay.main())

#!/usr/bin/env python3
"""Minimal libopus bindings for the UBAG voice audio relay.

Direct ctypes bindings to libopus (installed as libopus0 in the browser
image) covering exactly what the relay needs: decode one Opus packet to
s16le PCM and encode one PCM frame back. This intentionally replaces a pip
dependency (opuslib) so the browser image stays apt-only.

The libopus API is stable; only the symbols below are used:
  opus_decoder_create / opus_decode / opus_decoder_destroy  (client audio → virtual mic)
  opus_encoder_create / opus_encode / opus_encoder_destroy  (monitor PCM → gateway)
Error handling fails the frame, never the session: a corrupt or empty packet
raises OpusError for that one packet and the caller drops it.

Lifetime: both classes own a native handle. close() (also via ``with``)
destroys it exactly once; it is idempotent and serialized with decode/encode
by a per-codec lock, so a close racing a pump thread can never free the
handle mid-call. Use after close raises OpusError.
"""

from __future__ import annotations

import ctypes
import ctypes.util
import threading

# opus_defines.h
OPUS_OK = 0
OPUS_APPLICATION_AUDIO = 2049

SAMPLE_RATE = 48000
CHANNELS = 1
MAX_FRAME_SAMPLES = 5760  # per channel @48k: the longest legal packet (120 ms)
# 48 frames x 1275 bytes: the largest well-formed Opus packet; anything
# bigger is corrupt by construction and never reaches libopus.
MAX_PACKET_BYTES = 1275 * 48


class OpusError(Exception):
    """Raised when a decode/encode call fails or its input is invalid."""


def _load_libopus() -> ctypes.CDLL:
    name = ctypes.util.find_library("opus") or "libopus.so.0"
    lib = ctypes.CDLL(name)
    lib.opus_decoder_create.restype = ctypes.c_void_p
    lib.opus_decoder_create.argtypes = [ctypes.c_int32, ctypes.c_int32, ctypes.POINTER(ctypes.c_int)]
    lib.opus_decode.restype = ctypes.c_int
    lib.opus_decode.argtypes = [
        ctypes.c_void_p, ctypes.c_char_p, ctypes.c_int32,
        ctypes.c_void_p, ctypes.c_int32, ctypes.c_int,
    ]
    lib.opus_encoder_create.restype = ctypes.c_void_p
    lib.opus_encoder_create.argtypes = [ctypes.c_int32, ctypes.c_int32, ctypes.c_int32, ctypes.POINTER(ctypes.c_int)]
    lib.opus_encode.restype = ctypes.c_int
    lib.opus_encode.argtypes = [
        ctypes.c_void_p, ctypes.c_void_p, ctypes.c_int32,
        ctypes.c_void_p, ctypes.c_int32,
    ]
    lib.opus_decoder_destroy.argtypes = [ctypes.c_void_p]
    lib.opus_encoder_destroy.argtypes = [ctypes.c_void_p]
    return lib


class _Codec:
    """Shared handle lifetime: create-once, destroy-once, lock-serialized."""

    _destroy_name = ""

    def __init__(self) -> None:
        # State first: a failing create must leave a closable object.
        self._lib = None
        self._handle = None
        self._channels = 1
        self._lock = threading.Lock()

    def close(self) -> None:
        with self._lock:
            handle, self._handle = self._handle, None
            if handle:
                getattr(self._lib, self._destroy_name)(handle)

    def __enter__(self):
        return self

    def __exit__(self, *exc) -> None:
        self.close()


class OpusDecoder(_Codec):
    _destroy_name = "opus_decoder_destroy"

    def __init__(self, sample_rate: int = SAMPLE_RATE, channels: int = 1):
        super().__init__()
        self._lib = _load_libopus()
        error = ctypes.c_int()
        handle = self._lib.opus_decoder_create(sample_rate, channels, ctypes.byref(error))
        if error.value != OPUS_OK or not handle:
            raise OpusError(f"opus_decoder_create failed: {error.value}")
        self._handle = handle
        self._channels = channels

    def decode(self, packet: bytes, max_frame_samples: int = MAX_FRAME_SAMPLES) -> bytes:
        """Decode one Opus packet to s16le interleaved PCM.

        ``max_frame_samples`` is the output buffer capacity per channel; the
        default fits every legal packet duration (2.5 ms .. 120 ms).
        """
        if not packet:
            raise OpusError("empty opus packet")
        if len(packet) > MAX_PACKET_BYTES:
            raise OpusError(f"opus packet too large: {len(packet)}")
        if not 0 < max_frame_samples <= MAX_FRAME_SAMPLES:
            raise OpusError(f"invalid max_frame_samples: {max_frame_samples}")
        out = (ctypes.c_int16 * (max_frame_samples * self._channels))()
        with self._lock:
            if not self._handle:
                raise OpusError("decoder closed")
            written = self._lib.opus_decode(
                self._handle, bytes(packet), len(packet), out, max_frame_samples, 0,
            )
        if written < 0:
            raise OpusError(f"opus_decode failed: {written}")
        return ctypes.string_at(out, written * self._channels * 2)


class OpusEncoder(_Codec):
    _destroy_name = "opus_encoder_destroy"

    def __init__(self, sample_rate: int = SAMPLE_RATE, channels: int = 1):
        super().__init__()
        self._lib = _load_libopus()
        error = ctypes.c_int()
        handle = self._lib.opus_encoder_create(
            sample_rate, channels, OPUS_APPLICATION_AUDIO, ctypes.byref(error),
        )
        if error.value != OPUS_OK or not handle:
            raise OpusError(f"opus_encoder_create failed: {error.value}")
        self._handle = handle
        self._channels = channels

    def encode(self, pcm: bytes, frame_samples: int) -> bytes:
        """Encode one frame of s16le interleaved PCM to an Opus packet."""
        if len(pcm) != frame_samples * self._channels * 2:
            raise OpusError(f"pcm length {len(pcm)} does not match {frame_samples} samples")
        out = ctypes.create_string_buffer(MAX_FRAME_SAMPLES * self._channels * 2)
        with self._lock:
            if not self._handle:
                raise OpusError("encoder closed")
            written = self._lib.opus_encode(
                self._handle, pcm, frame_samples, out, ctypes.sizeof(out),
            )
        if written < 0:
            raise OpusError(f"opus_encode failed: {written}")
        return out.raw[:written]

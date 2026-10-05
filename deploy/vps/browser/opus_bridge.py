#!/usr/bin/env python3
"""Minimal libopus bindings for the UBAG voice audio relay.

Direct ctypes bindings to libopus (installed as libopus0 in the browser
image) covering exactly what the relay needs: decode one Opus packet to
s16le PCM and encode one PCM frame back. This intentionally replaces a pip
dependency (opuslib) so the browser image stays apt-only.

The libopus API is stable; only the symbols below are used:
  opus_decoder_create / opus_decode        (client audio → virtual mic)
  opus_encoder_create / opus_encode        (monitor PCM → gateway)
  opus_decoder_ctl(OPUS_GET_LAST_PACKET_DURATION)
Error handling fails the frame, never the session: a corrupt packet drops
audio for one 20 ms frame rather than killing the call.
"""

from __future__ import annotations

import ctypes
import ctypes.util

# opus_defines.h
OPUS_OK = 0
OPUS_APPLICATION_AUDIO = 2049
OPUS_GET_LAST_PACKET_DURATION = 4031

SAMPLE_RATE = 48000
CHANNELS = 1
MAX_FRAME_SAMPLES = 5760  # opus_decoder_get_nb_samples upper bound (120 ms)


class OpusError(Exception):
    """Raised when a decode/encode call returns a negative opus error code."""


def _load_libopus() -> ctypes.CDLL:
    name = ctypes.util.find_library("opus") or "libopus.so.0"
    lib = ctypes.CDLL(name)
    lib.opus_decoder_create.restype = ctypes.c_void_p
    lib.opus_decoder_create.argtypes = [ctypes.c_int32, ctypes.c_int32, ctypes.POINTER(ctypes.c_int)]
    lib.opus_decode.restype = ctypes.c_int
    lib.opus_decode.argtypes = [
        ctypes.c_void_p, ctypes.c_char_p, ctypes.c_int32,
        ctypes.c_int16, ctypes.c_int32, ctypes.c_int,
    ]
    lib.opus_decoder_get_last_packet_duration.restype = ctypes.c_int
    lib.opus_decoder_get_last_packet_duration.argtypes = [ctypes.c_void_p, ctypes.POINTER(ctypes.c_int32)]
    lib.opus_encoder_create.restype = ctypes.c_void_p
    lib.opus_encoder_create.argtypes = [ctypes.c_int32, ctypes.c_int32, ctypes.c_int32, ctypes.POINTER(ctypes.c_int)]
    lib.opus_encode.restype = ctypes.c_int
    lib.opus_encode.argtypes = [
        ctypes.c_void_p, ctypes.c_int16, ctypes.c_int32,
        ctypes.c_char_p, ctypes.c_int32,
    ]
    lib.opus_decoder_destroy.argtypes = [ctypes.c_void_p]
    lib.opus_encoder_destroy.argtypes = [ctypes.c_void_p]
    return lib


class OpusDecoder:
    def __init__(self, sample_rate: int = SAMPLE_RATE, channels: int = 1):
        self._lib = _load_libopus()
        error = ctypes.c_int()
        self._handle = self._lib.opus_decoder_create(sample_rate, channels, ctypes.byref(error))
        if error.value != OPUS_OK or not self._handle:
            raise OpusError(f"opus_decoder_create failed: {error.value}")
        self._channels = channels

    def decode(self, packet: bytes, frame_samples: int) -> bytes:
        """Decode one Opus packet to s16le interleaved PCM."""
        out = (ctypes.c_int16 * (frame_samples * self._channels))()
        written = self._lib.opus_decode(
            self._handle, packet, len(packet), out, frame_samples, 0,
        )
        if written < 0:
            raise OpusError(f"opus_decode failed: {written}")
        return ctypes.string_at(out, written * self._channels * 2)

    def close(self) -> None:
        self._lib.opus_decoder_destroy(self._handle)


class OpusEncoder:
    def __init__(self, sample_rate: int = SAMPLE_RATE, channels: int = 1):
        self._lib = _load_libopus()
        error = ctypes.c_int()
        self._handle = self._lib.opus_encoder_create(
            sample_rate, channels, OPUS_APPLICATION_AUDIO, ctypes.byref(error),
        )
        if error.value != OPUS_OK or not self._handle:
            raise OpusError(f"opus_encoder_create failed: {error.value}")
        self._channels = channels

    def encode(self, pcm: bytes, frame_samples: int) -> bytes:
        """Encode one frame of s16le interleaved PCM to an Opus packet."""
        out = ctypes.create_string_buffer(MAX_FRAME_SAMPLES * self._channels * 2)
        written = self._lib.opus_encode(
            self._handle, pcm, frame_samples, out, ctypes.sizeof(out),
        )
        if written < 0:
            raise OpusError(f"opus_encode failed: {written}")
        return out.raw[:written]

    def close(self) -> None:
        self._lib.opus_encoder_destroy(self._handle)

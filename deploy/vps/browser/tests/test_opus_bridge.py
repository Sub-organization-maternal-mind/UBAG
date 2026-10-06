"""opus_bridge lifetime and validation tests (libopus replaced by FakeLib)."""

import ctypes
import struct
import threading

import pytest

import opus_bridge
from opus_bridge import OpusDecoder, OpusEncoder, OpusError
from conftest import pkt


def test_close_destroys_native_handle_once_and_is_idempotent(lib):
    dec, enc = OpusDecoder(), OpusEncoder()
    assert len(lib.live) == 2
    dec.close()
    dec.close()
    enc.close()
    enc.close()
    assert not lib.live
    assert len(lib.destroyed) == 2 and len(set(lib.destroyed)) == 2


def test_context_manager_closes(lib):
    with OpusDecoder() as dec:
        assert dec.decode(pkt(20)) != b""
    assert not lib.live
    with OpusEncoder() as enc:
        assert enc.encode(b"\x00" * 1920, 960) == b"OP"
    assert not lib.live


def test_use_after_close_raises_instead_of_touching_freed_handle(lib):
    dec, enc = OpusDecoder(), OpusEncoder()
    dec.close()
    enc.close()
    with pytest.raises(OpusError):
        dec.decode(pkt(20))
    with pytest.raises(OpusError):
        enc.encode(b"\x00" * 1920, 960)


def test_failed_encoder_create_raises_opuserror(lib):
    lib.fail_encoder_create = True
    with pytest.raises(OpusError):
        OpusEncoder()


@pytest.mark.parametrize("ms", [2, 10, 20, 60, 120])
def test_decoder_handles_every_legal_duration_up_to_120ms(lib, ms):
    with OpusDecoder() as dec:
        pcm = dec.decode(pkt(ms, fill=1))
    assert len(pcm) == ms * 48 * 2
    assert lib.decode_calls[-1][1] == opus_bridge.MAX_FRAME_SAMPLES == 5760


def test_small_buffer_cannot_be_used_to_truncate_silently(lib):
    with OpusDecoder() as dec:
        with pytest.raises(OpusError):
            dec.decode(pkt(120), max_frame_samples=960)  # libopus: buffer too small
        with pytest.raises(OpusError):
            dec.decode(pkt(20), max_frame_samples=0)
        with pytest.raises(OpusError):
            dec.decode(pkt(20), max_frame_samples=5761)


def test_empty_corrupt_and_oversize_packets_raise_but_decoder_survives(lib):
    with OpusDecoder() as dec:
        with pytest.raises(OpusError):
            dec.decode(b"")
        with pytest.raises(OpusError):
            dec.decode(bytes([0xFF, 0]))
        with pytest.raises(OpusError):
            dec.decode(pkt(20, extra=opus_bridge.MAX_PACKET_BYTES))
        assert dec.decode(pkt(20)) != b""
    assert len(lib.decode_calls) == 2  # empty/oversize never reached libopus


def test_encoder_rejects_wrong_pcm_length(lib):
    with OpusEncoder() as enc:
        with pytest.raises(OpusError):
            enc.encode(b"\x00" * 10, 960)


# --- P7.6: per-codec buffer reuse must not change a single output byte ----

def _expected_pcm(ms, fill):
    return struct.pack("<h", fill) * (ms * 48)


CORPUS = [(20, 7), (120, 9), (2, 1), (60, 5), (10, 3), (20, 0), (120, 255), (10, 4)]


def test_decode_corpus_bytes_identical_with_reused_buffer(lib):
    # long packet then short ones: a stale tail from the reused buffer would show
    with OpusDecoder() as dec:
        for ms, fill in CORPUS:
            assert dec.decode(pkt(ms, fill=fill)) == _expected_pcm(ms, fill)


def test_decoder_reuses_one_11520_byte_buffer(lib):
    ptrs = []
    real = lib.opus_decode

    def spy(handle, packet, n, out, frame_size, fec):
        ptrs.append(ctypes.addressof(out))
        assert ctypes.sizeof(out) == opus_bridge.MAX_FRAME_SAMPLES * 2 == 11520
        return real(handle, packet, n, out, frame_size, fec)

    lib.opus_decode = spy
    with OpusDecoder() as dec:
        for ms, fill in CORPUS:
            dec.decode(pkt(ms, fill=fill))
    assert len(set(ptrs)) == 1 and len(ptrs) == len(CORPUS)


def test_encoder_reuses_buffer_and_returns_exact_packet(lib):
    ptrs = []
    real = lib.opus_encode

    def spy(handle, pcm, frame_size, out, max_bytes):
        ptrs.append(ctypes.addressof(out))
        assert max_bytes == 11520
        return real(handle, pcm, frame_size, out, max_bytes)

    lib.opus_encode = spy
    with OpusEncoder() as enc:
        outs = [enc.encode(b"\x00" * 1920, 960) for _ in range(5)]
    assert outs == [b"OP"] * 5 and len(set(ptrs)) == 1


def test_concurrent_decodes_never_see_each_others_buffer(lib):
    errors = []
    with OpusDecoder() as dec:
        def worker(ms, fill):
            for _ in range(200):
                if dec.decode(pkt(ms, fill=fill)) != _expected_pcm(ms, fill):
                    errors.append((ms, fill))

        threads = [threading.Thread(target=worker, args=a) for a in ((120, 1), (10, 2), (60, 3), (2, 4))]
        for t in threads:
            t.start()
        for t in threads:
            t.join(10)
    assert not errors

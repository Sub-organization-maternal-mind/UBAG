"""opus_bridge lifetime and validation tests (libopus replaced by FakeLib)."""

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

"""Real-libopus tests for opus_bridge (the other suites use FakeLib).

Skipped unless libopus loads, so they only run in the Debian browser image (or
any host with libopus0). Property tests hold on every libopus 1.x build. The
byte-exact golden check depends on the libopus build, so the golden file
records the version and the check only runs on that exact version.

Record or refresh the golden in the Debian image:
    UBAG_OPUS_GOLDEN_UPDATE=1 python3 -m pytest tests/test_opus_real.py
"""

import hashlib
import json
import os

import pytest

import opus_bridge
from opus_bridge import OpusDecoder, OpusEncoder, OpusError

try:
    LIBOPUS_VERSION = opus_bridge.opus_version()
except (OSError, AttributeError) as exc:  # no libopus on this host
    pytest.skip(f"libopus not available: {exc}", allow_module_level=True)

GOLDEN_PATH = os.path.join(os.path.dirname(os.path.abspath(__file__)), "golden", "opus_real_golden.json")
FRAME_SAMPLES = 960  # 20 ms @ 48 kHz mono
FRAMES = 5


def _tri(i: int, period: int, amp: int) -> int:
    """Integer triangle wave in [-amp, amp]; no floats, so the corpus is
    bit-identical on every platform."""
    half = period // 2
    phase = i % period
    ramp = phase if phase < half else period - phase
    return ramp * 2 * amp // half - amp


def corpus_pcm() -> list:
    """FRAMES x 20 ms s16le frames of two mixed triangle tones."""
    frames = []
    for f in range(FRAMES):
        samples = [_tri(f * FRAME_SAMPLES + i, 96, 9000) + _tri(f * FRAME_SAMPLES + i, 37, 3000)
                   for i in range(FRAME_SAMPLES)]
        frames.append(b"".join(s.to_bytes(2, "little", signed=True) for s in samples))
    return frames


def _rms(pcm: bytes) -> float:
    values = [int.from_bytes(pcm[i:i + 2], "little", signed=True) for i in range(0, len(pcm), 2)]
    return (sum(v * v for v in values) / len(values)) ** 0.5


def _encode_corpus():
    with OpusEncoder() as enc:
        return [enc.encode(frame, FRAME_SAMPLES) for frame in corpus_pcm()]


def _decode_all(packets):
    with OpusDecoder() as dec:
        return [dec.decode(p) for p in packets]


def test_version_string_is_libopus():
    assert LIBOPUS_VERSION.lower().startswith("libopus")


def test_encoded_packets_are_wellformed_and_bounded():
    packets = _encode_corpus()
    assert len(packets) == FRAMES
    for packet in packets:
        assert 1 <= len(packet) <= 1275


def test_decode_returns_exactly_one_frame_per_packet():
    for pcm in _decode_all(_encode_corpus()):
        assert len(pcm) == FRAME_SAMPLES * 2


def test_roundtrip_preserves_signal_energy():
    original, decoded = corpus_pcm(), _decode_all(_encode_corpus())
    for src, out in list(zip(original, decoded))[1:]:  # skip the encoder's first-frame lookahead
        ratio = _rms(out) / _rms(src)
        assert 0.3 < ratio < 2.0, ratio


def test_silence_costs_less_than_signal():
    with OpusEncoder() as enc:
        silence = enc.encode(b"\x00" * (FRAME_SAMPLES * 2), FRAME_SAMPLES)
    assert len(silence) < sum(len(p) for p in _encode_corpus()) / FRAMES


def test_bad_inputs_are_rejected_without_killing_the_decoder():
    with OpusDecoder() as dec:
        with pytest.raises(OpusError):
            dec.decode(b"")
        with pytest.raises(OpusError):
            dec.decode(bytes(opus_bridge.MAX_PACKET_BYTES + 1))
        assert len(dec.decode(_encode_corpus()[0])) == FRAME_SAMPLES * 2


def _golden_record():
    packets = _encode_corpus()
    return {
        "libopus_version": LIBOPUS_VERSION,
        "sample_rate": opus_bridge.SAMPLE_RATE,
        "channels": opus_bridge.CHANNELS,
        "frame_samples": FRAME_SAMPLES,
        "frames": FRAMES,
        "packets_hex": [p.hex() for p in packets],
        "decoded_sha256": [hashlib.sha256(pcm).hexdigest() for pcm in _decode_all(packets)],
    }


def test_golden_packets_for_recorded_libopus_version():
    if os.environ.get("UBAG_OPUS_GOLDEN_UPDATE") == "1":
        record = {"_note": "Golden Opus packets for the fixed corpus in test_opus_real.py; the byte check "
                           "only runs on the recorded libopus_version.", **_golden_record()}
        with open(GOLDEN_PATH, "w", encoding="utf-8", newline="\n") as fh:
            json.dump(record, fh, indent=2)
            fh.write("\n")
        return
    with open(GOLDEN_PATH, encoding="utf-8") as fh:
        golden = json.load(fh)
    if not golden.get("libopus_version"):
        pytest.skip("golden not recorded yet; run with UBAG_OPUS_GOLDEN_UPDATE=1 in the Debian image")
    if golden["libopus_version"] != LIBOPUS_VERSION:
        pytest.skip(f"golden recorded with {golden['libopus_version']!r}, running {LIBOPUS_VERSION!r}")
    current = _golden_record()
    assert current["packets_hex"] == golden["packets_hex"]
    assert current["decoded_sha256"] == golden["decoded_sha256"]

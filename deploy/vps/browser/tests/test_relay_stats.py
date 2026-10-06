"""UBAG_VOICE_RELAY_STATS measurement hooks and the UBAG_VOICE_{MIC_PIPE,PAREC,PACTL}
seams (FakeLib/FakeProc rig; no real libopus, PulseAudio or FIFO)."""

import importlib.util
import json
import os
import subprocess

import pytest

from conftest import BROWSER_DIR, pkt, raw_frame, wait_for

AUDIO, CONTROL = 1, 2
PREFIX = "audio-relay: stats "


def stats_lines(text):
    return [json.loads(line[len(PREFIX):]) for line in text.splitlines() if line.startswith(PREFIX)]


def load_relay():
    spec = importlib.util.spec_from_file_location("audio_relay_fresh", os.path.join(BROWSER_DIR, "audio-relay.py"))
    relay = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(relay)
    return relay


# --- RelayStats unit ---------------------------------------------------------

def test_tick_samples_one_frame_in_n(rig):
    stats = rig.relay.RelayStats("s")
    timed = [stats.tick("mic_frames") for _ in range(rig.relay.STATS_SAMPLE_EVERY * 3)]
    assert sum(timed) == 3 and stats.counters["mic_frames"] == rig.relay.STATS_SAMPLE_EVERY * 3


def test_summary_percentiles_and_window_reset(rig):
    stats = rig.relay.RelayStats("s")
    for ms in range(1, 101):
        stats.observe("mic", ms / 1000)
    out = stats.summary()
    assert out["event"] == "relay_stats" and out["session_id"] == "s"
    assert out["mic_samples"] == 100
    assert out["mic_p50_ms"] == 51.0 and out["mic_p95_ms"] == 96.0 and out["mic_max_ms"] == 100.0
    assert "speaker_p50_ms" not in out and out["speaker_samples"] == 0
    assert stats.summary()["mic_samples"] == 0  # window was drained


def test_samples_are_bounded_per_window(rig):
    stats = rig.relay.RelayStats("s")
    for _ in range(rig.relay.STATS_MAX_SAMPLES * 2):
        stats.observe("speaker", 0.001)
    assert stats.summary()["speaker_samples"] == rig.relay.STATS_MAX_SAMPLES


def test_emit_is_rate_limited_to_the_interval(rig, capsys):
    now = [100.0]
    stats = rig.relay.RelayStats("s", clock=lambda: now[0])
    stats.maybe_emit()
    assert not stats_lines(capsys.readouterr().err)
    now[0] += rig.relay.STATS_INTERVAL_S
    stats.maybe_emit()
    stats.maybe_emit()  # same instant: next emit is a full interval away
    assert len(stats_lines(capsys.readouterr().err)) == 1
    stats.maybe_emit(force=True)
    assert len(stats_lines(capsys.readouterr().err)) == 1


# --- wired into a session ----------------------------------------------------

def test_stats_off_by_default(rig, capsys):
    client = rig.open_session()
    client.sendall(raw_frame(AUDIO, pkt(20)))
    assert wait_for(lambda: len(rig.lib.decode_calls) == 1)
    assert rig.sessions[0].stats is None
    client.close()
    assert rig.join_all()
    assert not stats_lines(capsys.readouterr().err)


def test_session_counts_frames_drops_and_emits_final_summary(rig, monkeypatch, capsys):
    monkeypatch.setenv("UBAG_VOICE_RELAY_STATS", "1")
    monkeypatch.setattr(rig.relay, "STATS_SAMPLE_EVERY", 1)  # time every frame
    client = rig.open_session()
    for _ in range(4):
        client.sendall(raw_frame(AUDIO, pkt(20, fill=1)))
    client.sendall(raw_frame(AUDIO, bytes([0xFF, 0])))  # corrupt
    client.sendall(raw_frame(CONTROL, b'{"op":"mute","muted":true}'))
    for _ in range(2):
        client.sendall(raw_frame(AUDIO, pkt(20, fill=1)))
    for _ in range(3):
        rig.procs[0].feed(b"\x01\x00" * 960)
        assert rig.recv(client) == (AUDIO, b"OP")
    stats = rig.sessions[0].stats
    assert wait_for(lambda: stats.counters["mic_frames"] == 7 and len(stats._samples["mic"]) == 4
                    and len(stats._samples["speaker"]) == 3)
    client.close()
    assert rig.join_all()
    final = stats_lines(capsys.readouterr().err)[-1]
    assert final["session_id"] == "s1"
    assert final["mic_frames"] == 7 and final["mic_decode_errors"] == 1
    assert final["mic_muted_discarded"] == 2 and final["mic_fifo_dropped"] == 0
    assert final["speaker_frames"] == 3 and final["speaker_encode_errors"] == 0
    assert final["mic_samples"] == 4 and final["speaker_samples"] == 3
    assert final["mic_p95_ms"] >= 0 and final["speaker_max_ms"] >= 0
    assert "mic_fifo_bytes" in final  # null off Linux, a byte count on it


def test_fifo_full_is_counted_as_a_drop(rig, monkeypatch, capsys):
    monkeypatch.setenv("UBAG_VOICE_RELAY_STATS", "1")

    def full(fd, data):
        raise BlockingIOError()

    monkeypatch.setattr(rig.relay.os, "write", full)
    client = rig.open_session()
    for _ in range(3):
        client.sendall(raw_frame(AUDIO, pkt(20)))
    stats = rig.sessions[0].stats
    assert wait_for(lambda: stats.counters["mic_fifo_dropped"] == 3)
    client.close()
    assert rig.join_all()
    assert stats_lines(capsys.readouterr().err)[-1]["mic_fifo_dropped"] == 3


def test_encode_errors_are_counted(rig, monkeypatch):
    monkeypatch.setenv("UBAG_VOICE_RELAY_STATS", "1")
    client = rig.open_session()
    session = rig.sessions[0]

    def boom(*args):
        raise rig.relay.OpusError("nope")

    monkeypatch.setattr(session.encoder, "encode", boom)
    rig.procs[0].feed(b"\x01\x00" * 960)
    assert wait_for(lambda: session.stats.counters["speaker_encode_errors"] == 1)
    client.close()
    assert rig.join_all()


# --- env seams ---------------------------------------------------------------

@pytest.mark.parametrize("value", [None, ""])
def test_seams_default_to_current_values(monkeypatch, value):
    for name in ("UBAG_VOICE_MIC_PIPE", "UBAG_VOICE_PAREC", "UBAG_VOICE_PACTL"):
        if value is None:
            monkeypatch.delenv(name, raising=False)
        else:
            monkeypatch.setenv(name, value)
    relay = load_relay()
    assert (relay.MIC_PIPE, relay.PAREC, relay.PACTL) == ("/tmp/ubag-voice-mic.pcm", "parec", "pactl")


def test_seams_override_every_external_command(monkeypatch):
    monkeypatch.setenv("UBAG_VOICE_MIC_PIPE", "/x/mic.pcm")
    monkeypatch.setenv("UBAG_VOICE_PAREC", "/x/parec")
    monkeypatch.setenv("UBAG_VOICE_PACTL", "/x/pactl")
    relay = load_relay()
    assert relay.MIC_PIPE == "/x/mic.pcm"

    calls = []

    class Done:
        returncode, stdout, stderr = 0, "", ""

    monkeypatch.setattr(subprocess, "run", lambda args, **kw: calls.append(args[0]) or Done())
    monkeypatch.setattr(subprocess, "Popen", lambda args, **kw: calls.append(args[0]))
    monkeypatch.setattr(relay, "wait_for_pulse", lambda timeout_s=15.0: True)
    monkeypatch.setattr(relay, "hold_fifo_writer", lambda: None)
    monkeypatch.setattr(relay.os, "mkfifo", lambda *a: None, raising=False)
    monkeypatch.setattr(relay.os.path, "exists", lambda p: True)
    relay.ensure_audio_devices()
    relay.spawn_monitor()
    assert set(calls) == {"/x/pactl", "/x/parec"}

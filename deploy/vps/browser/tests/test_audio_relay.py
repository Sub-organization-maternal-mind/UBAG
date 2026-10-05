"""Regression tests for audio-relay.py (wire protocol v2, lifecycle, health)."""

import os
import socket
import subprocess
import threading
import time

import pytest

import opus_bridge
from conftest import SECRET, pkt, raw_frame, wait_for

AUDIO, CONTROL = 1, 2


def closed_by_peer(client, timeout=3.0):
    """True when the relay closed its end (recv sees EOF / reset)."""
    client.settimeout(timeout)
    try:
        while True:
            if client.recv(4096) == b"":
                return True
    except (ConnectionError, OSError):
        return True
    except socket.timeout:
        return False


def assert_clean(rig):
    """No thread, subprocess, fd, codec handle or slot survives a session."""
    assert rig.join_all(), "handler thread still alive"
    for session in rig.sessions:
        assert all(not t.is_alive() for t in session.threads)
        assert session.mic_fd is None and session.decoder is None and session.encoder is None
    for proc in rig.procs:
        assert proc.stdout.closed and proc.poll() is not None
    for fd in rig.fifo_fds:
        with pytest.raises(OSError):
            os.fstat(fd)
    assert not rig.lib.live, "native codec handle leaked"
    assert not rig.relay._session_slot.locked()


# --- handshake / auth ----------------------------------------------------

def test_ready_reply_uses_v2_wire_format(rig):
    client = rig.connect()
    header = client.recv(4)
    body = client.recv(int.from_bytes(header, "little"))
    assert body[0] == CONTROL and body[1:] == b'{"op":"ready"}'


def test_empty_secret_fails_closed(rig, monkeypatch):
    monkeypatch.setenv("UBAG_VOICE_RELAY_SECRET", "")
    client = rig.connect(secret="")
    assert rig.recv_control(client) == {"op": "error", "reason": "unconfigured"}
    assert closed_by_peer(client)
    assert not rig.sessions


@pytest.mark.parametrize("mutate,reason", [
    (dict(token="0" * 64), "unauthorized"),
    (dict(exp=int(time.time()) - 5), "expired"),
    (dict(exp=int(time.time()) + 10_000), "expired"),
    (dict(secret="wrong"), "unauthorized"),
])
def test_bad_hello_rejected(rig, mutate, reason):
    client = rig.connect(**mutate)
    assert rig.recv_control(client) == {"op": "error", "reason": reason}
    assert closed_by_peer(client)
    assert not rig.sessions


@pytest.mark.parametrize("first", [
    raw_frame(AUDIO, pkt()),                                  # audio before hello
    raw_frame(CONTROL, b'{"op":"mute","muted":true}'),         # wrong control op
    raw_frame(CONTROL, b"not json"),
    raw_frame(7, b"x"),                                       # unknown type
    (70_000).to_bytes(4, "little") + b"\x02",                 # oversize length
    (0).to_bytes(4, "little"),                                # zero length
])
def test_wrong_first_frame_rejected(rig, first):
    client = rig.connect(handshake=False)
    client.sendall(first)
    assert rig.recv_control(client)["op"] == "error"
    assert closed_by_peer(client)
    assert not rig.sessions


def test_hello_must_arrive_within_deadline(rig, monkeypatch):
    monkeypatch.setattr(rig.relay, "HELLO_TIMEOUT_S", 0.3)
    client = rig.connect(handshake=False)
    assert rig.recv_control(client) == {"op": "error", "reason": "hello_timeout"}
    assert closed_by_peer(client)


def test_slow_drip_hello_hits_whole_frame_deadline(rig, monkeypatch):
    monkeypatch.setattr(rig.relay, "HELLO_TIMEOUT_S", 0.4)
    client = rig.connect(handshake=False)
    frame = rig.hello()

    def drip():
        for i in range(len(frame)):
            try:
                client.sendall(frame[i:i + 1])
            except OSError:
                return
            time.sleep(0.05)

    threading.Thread(target=drip, daemon=True).start()
    assert rig.recv_control(client) == {"op": "error", "reason": "hello_timeout"}


# --- fix 1: one session per environment, bounded accepts, timeouts -------

def test_second_connection_rejected_busy_first_unaffected(rig):
    first = rig.open_session()
    second = rig.connect()
    assert rig.recv_control(second) == {"op": "error", "reason": "busy"}
    assert closed_by_peer(second)
    # the first session is untouched: provider audio still flows
    rig.procs[0].feed(b"\x01\x00" * 960)
    assert rig.recv(first) == (AUDIO, b"OP")
    assert len(rig.sessions) == 1


def test_busy_without_hello_is_immediate(rig):
    rig.open_session()
    second = rig.connect(handshake=False)
    start = time.monotonic()
    assert rig.recv_control(second) == {"op": "error", "reason": "busy"}
    assert time.monotonic() - start < 1.0


def test_slot_freed_after_session_ends(rig):
    first = rig.open_session()
    first.close()
    assert wait_for(lambda: not rig.relay._session_slot.locked())
    second = rig.open_session()
    assert second


def test_idle_read_timeout_ends_session(rig, monkeypatch):
    monkeypatch.setattr(rig.relay, "IDLE_TIMEOUT_S", 0.4)
    client = rig.open_session()
    assert closed_by_peer(client, timeout=3.0)  # no frames sent: relay hangs up
    assert_clean(rig)


def test_pending_accepts_are_bounded(rig, monkeypatch):
    relay = rig.relay
    monkeypatch.setattr(relay, "MAX_PENDING", 2)
    monkeypatch.setattr(relay, "HELLO_TIMEOUT_S", 1.5)
    server = relay.make_server("127.0.0.1:0")
    stop = threading.Event()
    port = server.getsockname()[1]
    t = threading.Thread(target=relay.serve_forever, args=(server, stop), daemon=True)
    t.start()
    try:
        idle = [socket.create_connection(("127.0.0.1", port)) for _ in range(2)]
        time.sleep(0.3)  # both are now parked in the handshake
        extra = socket.create_connection(("127.0.0.1", port))
        extra.settimeout(1.0)
        start = time.monotonic()
        assert rig.recv_control(extra) == {"op": "error", "reason": "busy"}
        assert time.monotonic() - start < 1.0
        # the parked peers are released by the hello deadline, not held forever
        for sock in idle:
            assert rig.recv_control(sock) == {"op": "error", "reason": "hello_timeout"}
        for sock in idle + [extra]:
            sock.close()
    finally:
        stop.set()
        t.join(3)
        server.close()


# --- fix 2: cleanup state initialised before risky construction -----------

def test_decoder_init_failure_reports_cause_and_leaks_nothing(rig, monkeypatch, capsys):
    def boom(*a, **k):
        raise opus_bridge.OpusError("opus_decoder_create failed: -1")

    monkeypatch.setattr(rig.relay, "OpusDecoder", boom)
    client = rig.connect()
    assert rig.recv_control(client) == {"op": "error", "reason": "codec_unavailable"}
    assert closed_by_peer(client)
    assert_clean(rig)
    assert "UnboundLocalError" not in capsys.readouterr().err


def test_encoder_init_failure_destroys_decoder(rig):
    rig.lib.fail_encoder_create = True
    client = rig.connect()
    assert rig.recv_control(client) == {"op": "error", "reason": "codec_unavailable"}
    assert closed_by_peer(client)
    assert_clean(rig)
    assert len(rig.lib.destroyed) == 1  # the decoder built before the failure


def test_parec_spawn_failure_cleans_up(rig, monkeypatch):
    def boom():
        raise OSError("no parec")

    monkeypatch.setattr(rig.relay, "spawn_monitor", boom)
    client = rig.connect()
    assert rig.recv_control(client) == {"op": "error", "reason": "monitor_unavailable"}
    assert_clean(rig)


def test_mic_fifo_failure_cleans_up(rig, monkeypatch):
    def boom(timeout_s=2.0):
        raise OSError("ENXIO")

    monkeypatch.setattr(rig.relay, "open_mic_fifo", boom)
    client = rig.connect()
    assert rig.recv_control(client) == {"op": "error", "reason": "mic_unavailable"}
    assert_clean(rig)


# --- fix 3: every exit path signals, unblocks and joins -------------------

def test_client_eof_tears_everything_down(rig):
    client = rig.open_session()
    client.close()
    assert_clean(rig)
    assert rig.procs[0].terminated
    assert len(rig.lib.destroyed) == 2


def test_monitor_exit_ends_session_visibly(rig):
    client = rig.open_session()
    rig.procs[0].die()
    assert rig.recv_control(client) == {"op": "error", "reason": "monitor_exited"}
    assert_clean(rig)


def test_protocol_violation_mid_session_ends_it(rig):
    client = rig.open_session()
    client.sendall(raw_frame(9, b"junk"))  # unknown type
    assert closed_by_peer(client)
    assert_clean(rig)


def test_oversize_frame_mid_session_ends_it(rig):
    client = rig.open_session()
    client.sendall((70_000).to_bytes(4, "little"))
    assert closed_by_peer(client)
    assert_clean(rig)


def test_close_is_idempotent(rig):
    client = rig.open_session()
    session = rig.sessions[0]
    client.close()
    assert_clean(rig)
    session.close()
    session.close()
    assert len(rig.lib.destroyed) == 2


# --- audio paths -----------------------------------------------------------

def test_speaker_audio_reaches_gateway(rig):
    client = rig.open_session()
    rig.procs[0].feed(b"\x01\x00" * 960)
    assert rig.recv(client) == (AUDIO, b"OP")


def test_mic_audio_decoded_into_fifo_and_corrupt_packet_survives(rig):
    client = rig.open_session()
    client.sendall(raw_frame(AUDIO, bytes([0xFF, 0])))      # corrupt -> dropped
    client.sendall(raw_frame(AUDIO, b""))                    # empty -> dropped
    client.sendall(raw_frame(AUDIO, pkt(20, fill=5)))
    assert wait_for(lambda: len(rig.fifo_bytes()) == 1920)
    assert rig.fifo_bytes() == (5).to_bytes(2, "little") * 960
    assert not rig.sessions[0].stop.is_set()


def test_120ms_packet_is_accepted(rig):
    client = rig.open_session()
    client.sendall(raw_frame(AUDIO, pkt(120, fill=3)))
    assert wait_for(lambda: len(rig.fifo_bytes()) == 5760 * 2)
    assert any(frame_size >= 5760 for _, frame_size in rig.lib.decode_calls)


# --- fix 6: mute enforcement ---------------------------------------------

def test_mute_blocks_fifo_writes_until_unmuted(rig):
    client = rig.open_session()
    client.sendall(raw_frame(CONTROL, b'{"op":"mute","muted":true}'))
    client.sendall(raw_frame(AUDIO, pkt(20, fill=9)))
    client.sendall(raw_frame(AUDIO, pkt(20, fill=9)))
    client.sendall(raw_frame(CONTROL, b'{"op":"mute","muted":false}'))
    client.sendall(raw_frame(AUDIO, pkt(20, fill=2)))
    assert wait_for(lambda: len(rig.fifo_bytes()) >= 1920)
    time.sleep(0.2)
    assert rig.fifo_bytes() == (2).to_bytes(2, "little") * 960  # nothing from the muted frames
    assert len(rig.lib.decode_calls) == 3  # decoded-and-discarded keeps decoder state


def test_mute_requires_a_real_bool_and_unknown_ops_are_ignored(rig):
    client = rig.open_session()
    client.sendall(raw_frame(CONTROL, b'{"op":"mute","muted":"true"}'))
    client.sendall(raw_frame(CONTROL, b'{"op":"explode"}'))
    client.sendall(raw_frame(AUDIO, pkt(20, fill=4)))
    assert wait_for(lambda: len(rig.fifo_bytes()) == 1920)


# --- fix 5: device health ------------------------------------------------

def fake_pactl(sources, sinks):
    def run(args, **kwargs):
        kind = args[3] if args[:3] == ["pactl", "list", "short"] else ""
        names = {"sources": sources, "sinks": sinks}[kind]
        out = "".join(f"{i}\t{n}\tmodule\ts16le 1ch 48000Hz\tRUNNING\n" for i, n in enumerate(names))
        return subprocess.CompletedProcess(args, 0, stdout=out, stderr="")
    return run


def test_devices_ok_requires_both_mic_and_sink(rig, monkeypatch):
    relay = rig.relay
    both = (["ubag_virtual_mic", "ubag_provider_sink.monitor"], ["ubag_provider_sink"])
    monkeypatch.setattr(subprocess, "run", fake_pactl(*both))
    assert relay.devices_ok()
    monkeypatch.setattr(subprocess, "run", fake_pactl(["ubag_provider_sink.monitor"], ["ubag_provider_sink"]))
    assert not relay.devices_ok()  # mic missing
    monkeypatch.setattr(subprocess, "run", fake_pactl(["ubag_virtual_mic"], []))
    assert not relay.devices_ok()  # sink missing
    # a monitor source alone must not satisfy the sink check
    monkeypatch.setattr(subprocess, "run", fake_pactl(["ubag_virtual_mic", "ubag_provider_sink.monitor"], ["other"]))
    assert not relay.devices_ok()


def test_devices_ok_false_when_pactl_missing(rig, monkeypatch):
    def run(*a, **k):
        raise FileNotFoundError("pactl")

    monkeypatch.setattr(subprocess, "run", run)
    assert not rig.relay.devices_ok()


def test_pulse_restart_ends_active_session_then_recovers(rig, monkeypatch):
    relay = rig.relay
    client = rig.open_session()
    state = {"ok": False}
    monkeypatch.setattr(relay, "devices_ok", lambda: state["ok"])
    ensured = []
    monkeypatch.setattr(relay, "ensure_audio_devices", lambda: ensured.append(1))

    relay.device_health_step()           # first miss: tolerated (transient pactl hiccup)
    assert relay._devices_ready.is_set()
    relay.device_health_step()           # second consecutive miss: devices lost
    assert not relay._devices_ready.is_set()
    assert ensured                       # recovery attempted immediately

    assert rig.recv_control(client) == {"op": "error", "reason": "devices_lost"}
    assert_clean(rig)

    # new sessions are refused while devices are down ...
    monkeypatch.setattr(relay, "DEVICE_READY_WAIT_S", 0.2)
    refused = rig.connect()
    assert rig.recv_control(refused) == {"op": "error", "reason": "devices_unavailable"}

    # ... and accepted again once PulseAudio is back
    state["ok"] = True
    relay.device_health_step()
    assert relay._devices_ready.is_set()
    assert rig.open_session()


def test_single_health_miss_does_not_drop_ready(rig, monkeypatch):
    relay = rig.relay
    answers = iter([False, True])
    monkeypatch.setattr(relay, "devices_ok", lambda: next(answers))
    relay.device_health_step()
    relay.device_health_step()
    assert relay._devices_ready.is_set()

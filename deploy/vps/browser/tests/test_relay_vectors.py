"""Relay protocol v2 shared vectors, asserted against the Python relay.

The SAME fixture (packages/conformance/fixtures/voice-relay/v2.json) is
asserted by apps/gateway/internal/voice/relay_vectors_test.go for the Go
gateway, so the two sides cannot drift apart silently.
"""

import json
import os
import re
import socket
import time

import pytest

HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.abspath(os.path.join(HERE, "..", "..", "..", ".."))
VECTORS = json.load(open(os.path.join(
    REPO, "packages", "conformance", "fixtures", "voice-relay", "v2.json"), encoding="utf-8"))
RELAY_SOURCE = open(os.path.join(HERE, "..", "audio-relay.py"), encoding="utf-8").read()


def test_constants_match_relay(rig):
    c, relay = VECTORS["constants"], rig.relay
    assert relay.FRAME_HEADER.size == c["frame_header_bytes"]
    assert relay.MAX_FRAME_BYTES == c["max_frame_bytes"]
    assert (relay.TYPE_AUDIO, relay.TYPE_CONTROL) == (c["type_audio"], c["type_control"])
    assert relay.HELLO_TIMEOUT_S == c["hello_timeout_s"]
    assert relay.HELLO_MAX_FUTURE_S == c["hello_max_future_s"]
    assert relay.DEVICE_READY_WAIT_S == c["device_ready_wait_s"]
    assert relay.MAX_PENDING == c["max_pending"]
    assert c["ready_timeout_s"] > relay.DEVICE_READY_WAIT_S  # gateway waits longer than the relay
    if "UBAG_VOICE_RELAY_IDLE_S" not in os.environ:  # default only; env may override
        assert relay.IDLE_TIMEOUT_S == c["idle_timeout_s"]


@pytest.mark.parametrize("vec", VECTORS["token_vectors"], ids=lambda v: v["session_id"] + "-" + v["secret"])
def test_token_vectors(rig, vec):
    assert rig.relay.relay_token(vec["secret"].encode("utf-8"), vec["session_id"], vec["exp"]) == vec["token"]


@pytest.mark.parametrize("case", VECTORS["framing"], ids=lambda c: c["id"])
def test_framing(rig, case):
    payload = bytes.fromhex(case["payload_hex"]) if "payload_hex" in case else bytes(case.get("payload_len", 0))
    wire = case["length"].to_bytes(4, "little")
    if case["length"] > 0:
        wire += bytes([case["type"]]) + payload
    server, client = socket.socketpair()
    try:
        client.sendall(wire)  # connection stays open: a violation must not wait for more bytes
        server.settimeout(2.0)
        if case["outcome"] == "violation":
            with pytest.raises(ConnectionError) as exc:
                rig.relay.read_frame(server)
            assert "peer closed" not in str(exc.value)
        else:
            assert rig.relay.read_frame(server) == (case["type"], payload)
    finally:
        server.close()
        client.close()


def build_hello_frame(case, secret):
    """Return (frame_type, payload_bytes) for a hello_validation case."""
    if case.get("frame_type") == "audio":
        return 1, b"\x00\x01"
    if "raw_payload" in case:
        return 2, case["raw_payload"].encode("utf-8")
    hello = dict(case["hello"])
    if "session_id_len" in case:
        hello["session_id"] = "a" * case["session_id_len"]
    if "exp_offset_s" in case:
        hello["exp"] = int(time.time()) + case["exp_offset_s"]
    mode = case.get("token_mode")
    if mode == "valid":
        hello["token"] = hmac_token(secret, hello["session_id"], hello["exp"])
    elif mode == "wrong":
        hello["token"] = "0" * 64
    return 2, json.dumps(hello).encode("utf-8")


def hmac_token(secret, session_id, exp):
    import hashlib
    import hmac
    return hmac.new(secret.encode("utf-8"), f"voice-relay|{session_id}|{exp}".encode("utf-8"),
                    hashlib.sha256).hexdigest()


@pytest.mark.parametrize("case", VECTORS["hello_validation"], ids=lambda c: c["id"])
def test_hello_validation(rig, monkeypatch, case):
    configured = case.get("secret_configured", True)
    monkeypatch.setenv("UBAG_VOICE_RELAY_SECRET", VECTORS["secret"] if configured else "")
    ftype, payload = build_hello_frame(case, VECTORS["secret"])
    server, client = socket.socketpair()
    try:
        client.sendall((len(payload) + 1).to_bytes(4, "little") + bytes([ftype]) + payload)
        if case["outcome"] == "ok":
            assert rig.relay.authenticate(server) == (
                "a" * case["session_id_len"] if "session_id_len" in case else case["hello"]["session_id"])
        else:
            with pytest.raises(rig.relay.HandshakeError) as exc:
                rig.relay.authenticate(server)
            assert exc.value.reason == case["outcome"]
    finally:
        server.close()
        client.close()


def test_reply_reasons_are_the_relays_reasons():
    reasons = VECTORS["reply_reasons"]
    documented = set(reasons["handshake"]) | set(reasons["session"])
    # Every reason the relay can raise or send appears in the shared enum...
    used = set(re.findall(r'HandshakeError\("(\w+)"\)', RELAY_SOURCE))
    assert used <= documented, used - documented
    # ...and every enum member is something the relay documents.
    for reason in documented:
        assert reason in RELAY_SOURCE, reason
    assert reasons["gateway_busy_reason"] in reasons["handshake"]


def test_control_frames_encode_compact(rig):
    """Python's encoder emits compact JSON; key order is not significant."""
    a, b = socket.socketpair()
    try:
        rig.relay.write_control(a, {"op": "mute", "muted": True})
        _, payload = rig.relay.read_frame(b)
        assert payload == b'{"op":"mute","muted":true}'
        assert json.loads(payload) == {"muted": True, "op": "mute"}
    finally:
        a.close()
        b.close()

"""UBAG_VOICE_RELAY_KEY_FILE: the Helper Node hands the relay a per-attempt key per call (P5.10)."""

import json
import socket
import time

import pytest

from test_relay_vectors import hmac_token


def _hello(rig, secret, **hello):
    hello = {"op": "hello", "exp": int(time.time()) + 60, **hello}
    bound = (hello["node_id"], hello["generation"]) if "node_id" in hello else ()
    hello.setdefault("token", hmac_token(secret, hello["session_id"], hello["exp"], *bound))
    payload = json.dumps(hello).encode("utf-8")
    server, client = socket.socketpair()
    try:
        client.sendall((len(payload) + 1).to_bytes(4, "little") + bytes([2]) + payload)
        return rig.relay.authenticate(server)
    finally:
        server.close()
        client.close()


def _refused(rig, secret, **hello):
    with pytest.raises(rig.relay.HandshakeError) as exc:
        _hello(rig, secret, **hello)
    return exc.value.reason


def test_key_file_is_the_secret_and_is_reread_on_every_hello(rig, monkeypatch, tmp_path):
    key = tmp_path / "relay-key"
    monkeypatch.setenv("UBAG_VOICE_RELAY_KEY_FILE", str(key))
    monkeypatch.delenv("UBAG_VOICE_RELAY_SECRET", raising=False)

    key.write_text("attempt-key-1\n")  # a trailing newline is not part of the key
    assert _hello(rig, "attempt-key-1", session_id="s1", node_id="n", generation=1) == "s1"

    # The helper rewrites the file for the next call: no relay restart.
    key.write_text("attempt-key-2")
    assert _refused(rig, "attempt-key-1", session_id="s2", node_id="n", generation=1) == "unauthorized"
    assert _hello(rig, "attempt-key-2", session_id="s2", node_id="n", generation=1) == "s2"


def test_missing_or_empty_key_file_refuses_every_session(rig, monkeypatch, tmp_path):
    key = tmp_path / "relay-key"
    monkeypatch.setenv("UBAG_VOICE_RELAY_KEY_FILE", str(key))
    # The file does not exist (helper removed it after the call, or never wrote it).
    assert _refused(rig, "anything", session_id="s1") == "unconfigured"
    key.write_text("")
    assert _refused(rig, "anything", session_id="s1") == "unconfigured"


def test_key_file_never_falls_back_to_the_env_secret(rig, monkeypatch, tmp_path):
    """A helper's relay must not accept the global secret even when it is in the environment."""
    monkeypatch.setenv("UBAG_VOICE_RELAY_SECRET", "global-secret")
    monkeypatch.setenv("UBAG_VOICE_RELAY_KEY_FILE", str(tmp_path / "absent"))
    assert _refused(rig, "global-secret", session_id="s1") == "unconfigured"
    (tmp_path / "absent").write_text("attempt-key")
    assert _refused(rig, "global-secret", session_id="s1") == "unauthorized"
    assert _hello(rig, "attempt-key", session_id="s1") == "s1"


def test_without_a_key_file_the_env_secret_is_unchanged(rig, monkeypatch):
    monkeypatch.delenv("UBAG_VOICE_RELAY_KEY_FILE", raising=False)
    monkeypatch.setenv("UBAG_VOICE_RELAY_SECRET", "s3")
    assert _hello(rig, "s3", session_id="plain") == "plain"


def test_oversized_key_file_is_truncated_not_trusted_whole(rig, monkeypatch, tmp_path):
    key = tmp_path / "relay-key"
    key.write_text("k" * 10_000)
    monkeypatch.setenv("UBAG_VOICE_RELAY_KEY_FILE", str(key))
    assert rig.relay.relay_secret() == b"k" * rig.relay.KEY_FILE_MAX_BYTES

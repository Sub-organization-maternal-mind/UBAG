"""UBAG_VOICE_ENV_ID namespacing of the relay's Pulse devices and mic FIFO."""

import importlib.util
import os

import pytest

from conftest import BROWSER_DIR


def load_relay():
    spec = importlib.util.spec_from_file_location("audio_relay_env", os.path.join(BROWSER_DIR, "audio-relay.py"))
    relay = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(relay)
    return relay


def test_unset_keeps_historic_names(monkeypatch):
    monkeypatch.delenv("UBAG_VOICE_ENV_ID", raising=False)
    monkeypatch.delenv("UBAG_VOICE_MIC_PIPE", raising=False)
    relay = load_relay()
    assert (relay.MIC_SOURCE, relay.SPEAKER_SINK, relay.MIC_PIPE) == (
        "ubag_virtual_mic", "ubag_provider_sink", "/tmp/ubag-voice-mic.pcm")
    assert relay.SPEAKER_MONITOR == "ubag_provider_sink.monitor"


def test_env_id_namespaces_devices_and_fifo(monkeypatch):
    monkeypatch.delenv("UBAG_VOICE_MIC_PIPE", raising=False)
    names = {}
    for env_id in ("env-a", "env-b"):
        monkeypatch.setenv("UBAG_VOICE_ENV_ID", env_id)
        r = load_relay()
        names[env_id] = (r.MIC_SOURCE, r.SPEAKER_SINK, r.SPEAKER_MONITOR, r.MIC_PIPE)
    assert names["env-a"] == ("ubag_virtual_mic_env-a", "ubag_provider_sink_env-a",
                              "ubag_provider_sink_env-a.monitor", "/tmp/ubag-voice-mic-env-a.pcm")
    # two environments on one host share no device or FIFO name
    assert not set(names["env-a"]) & set(names["env-b"])


def test_explicit_mic_pipe_still_wins(monkeypatch):
    monkeypatch.setenv("UBAG_VOICE_ENV_ID", "env-a")
    monkeypatch.setenv("UBAG_VOICE_MIC_PIPE", "/run/custom.pcm")
    assert load_relay().MIC_PIPE == "/run/custom.pcm"


@pytest.mark.parametrize("bad", ["../x", "a b", "x" * 33, "é", "a;b", "a/b"])
def test_invalid_env_id_refuses_instead_of_sharing_names(monkeypatch, bad):
    monkeypatch.setenv("UBAG_VOICE_ENV_ID", bad)
    with pytest.raises(ValueError):
        load_relay()

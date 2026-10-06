"""Pure-logic tests for bench_relay.py: the pre-registered stop rule, the py-spy
stack classifier and the PulseAudio sample-spec check. No libopus, no relay."""

import bench_relay as b


def session(cpu=1.0, libopus=0.4, syscall=0.2, pct=2.0):
    return {"process_cpu_s": cpu, "libopus_cpu_s": libopus, "syscall_cpu_s": syscall, "cpu_pct_of_core": pct}


def runs(*sessions, codec="libopus", fifo="fifo", n=1):
    return [{"sessions": n, "codec": codec, "fifo_kind": fifo, "per_session": list(sessions)}]


def test_pctl_matches_relay_indexing():
    assert b.pctl([3, 1, 2], 0.5) == 2
    assert b.pctl([1], 0.95) == 1
    assert b.pctl(list(range(100)), 0.95) == 95


def test_rule_a_fires_when_libopus_and_syscalls_dominate():
    v = b.evaluate_stop_rule(runs(session(libopus=0.5, syscall=0.25)))
    assert v["A"]["fired"] and v["verdict"] == "stop"


def test_rule_b_fires_when_relay_is_small_in_the_container():
    v = b.evaluate_stop_rule(runs(session(pct=2.0)), container_cpu_pct=100.0)  # 2% of container
    assert v["B"]["fired"] and v["verdict"] == "stop"


def test_continue_needs_both_rules_evaluated_and_clear():
    v = b.evaluate_stop_rule(runs(session(pct=20.0)), container_cpu_pct=100.0)
    assert not v["A"]["fired"] and v["B"]["fired"] is False and v["verdict"] == "continue"
    assert v["plan_gate"]["overhead_ge_20pct"] and v["plan_gate"]["material_in_mixed_workload"]
    assert v["plan_gate"]["verdict"] == "met"


def test_without_container_cpu_the_verdict_is_inconclusive_not_continue():
    v = b.evaluate_stop_rule(runs(session()))
    assert v["verdict"] == "inconclusive" and v["B"]["fired"] is None
    assert v["plan_gate"]["verdict"] == "unevaluated"


def test_fake_codec_or_non_fifo_run_is_invalid():
    assert b.evaluate_stop_rule(runs(session(), codec="fake"))["verdict"] == "invalid"
    assert b.evaluate_stop_rule(runs(session(), fifo="file"))["verdict"] == "invalid"
    assert b.evaluate_stop_rule([])["verdict"] == "invalid"


def test_stack_classifier():
    assert b.classify_stack(["_pump_mic (audio-relay.py:660)", "decode (opus_bridge.py:126)",
                             "opus_decode (libopus.so.0.8.0)", "celt_decode_with_ec (libopus.so.0.8.0)"]) == "libopus"
    assert b.classify_stack(["_pump_mic (audio-relay.py:666)", "__GI___libc_write (libc.so.6)"]) == "syscall"
    assert b.classify_stack(["read_frame (audio-relay.py:191)", "__libc_recv (libc.so.6)"]) == "syscall"
    assert b.classify_stack(["_pump_mic (audio-relay.py:646)", "read_frame (audio-relay.py:191)"]) == "other"


def test_parse_pyspy_raw_shares():
    raw = ("a;opus_decode (libopus.so.0) 6\n"
           "a;__libc_write (libc.so.6) 2\n"
           "a;b (audio-relay.py:1) 2\n"
           "garbage line without count\n")
    out = b.parse_pyspy_raw(raw)
    assert out == {"samples": 10, "libopus": 6, "syscall": 2, "other": 2, "libopus_syscall_share": 0.8}
    assert b.parse_pyspy_raw("")["libopus_syscall_share"] is None


def test_pulse_check_flags_resampling():
    sinks = b.parse_pactl_short("0\tubag_provider_sink\tmodule-null-sink.c\ts16le 2ch 44100Hz\tIDLE\n")
    sources = b.parse_pactl_short(
        "0\tubag_provider_sink.monitor\tmodule-null-sink.c\ts16le 2ch 44100Hz\tIDLE\n"
        "1\tubag_virtual_mic\tmodule-pipe-source.c\ts16le 1ch 48000Hz\tIDLE\n")
    out = b.pulse_findings(sinks, sources)
    assert out["hidden_resampling_suspected"]
    assert {m["name"] for m in out["mismatches"]} == {"ubag_provider_sink", "ubag_provider_sink.monitor"}
    clean = b.pulse_findings([{"name": "ubag_provider_sink", "spec": ("s16le", 1, 48000)}],
                             [{"name": "ubag_provider_sink.monitor", "spec": ("s16le", 1, 48000)},
                              {"name": "ubag_virtual_mic", "spec": ("s16le", 1, 48000)}])
    assert not clean["hidden_resampling_suspected"]

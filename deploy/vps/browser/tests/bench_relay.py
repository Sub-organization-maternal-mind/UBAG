#!/usr/bin/env python3
"""Relay baseline profile and pre-registered stop-rule check (P7.5).

Measures what the Python audio relay (audio-relay.py) costs per active call and
evaluates the roadmap's stop rule BEFORE any Rust rewrite is considered:

    STOP (close the experiment, P7.6-P7.8) if
      A. >= 70% of relay CPU is inside libopus + syscalls, or
      B. the relay is < 5% of the container's CPU.
    The plan's own gate (>= 20% UBAG-controlled overhead, or >= 25% CPU/mem,
    material in the mixed workload) is reported next to it, never instead of it.

Not a pytest module (no test_ prefix). Run it INSIDE the browser image (real
libopus), on an isolated container, NEVER on the shared VPS:

    docker run --rm --cap-add SYS_PTRACE -v "$PWD":/w -w /w/deploy/vps/browser \\
      <browser-image-or-debian:bookworm-slim+python3+libopus0+pulseaudio-utils> \\
      python3 tests/bench_relay.py --sessions 1,5,10 --duration 30 \\
        --pulse-check --pyspy --host-class "<where this ran>" --out relay-baseline.json

Model: the relay serves ONE session per audio environment, so N sessions are N
relay processes (N concurrent audio environments competing for the host's
cores), each fed by a paced 20 ms real-libopus client and a fake parec that
emits 20 ms PCM on the same schedule. The fake parec, this client and real
PulseAudio/Chrome CPU are NOT in the relay's numbers; use the container's own
CPU (docker stats during a live call) for rule B via --container-cpu-pct.

Per relay it reports CPU per call-minute, RSS, thread wake lag, mic FIFO-full
drops, and p95 frame turnaround. CPU is attributed in-process (thread CPU time
around the libopus calls and the socket/FIFO/pipe syscalls) which needs no
ptrace; --pyspy adds py-spy --native and --native --gil as corroboration
(ctypes releases the GIL during libopus calls, so --gil by design shows who
holds the GIL, not the codec time).

Numbers from a laptop or any non-lab host are NON-AUTHORITATIVE. A run with
the fake codec or without a real FIFO is harness smoke only: verdict "invalid".
stdlib only.
"""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
import platform
import re
import select
import shutil
import socket
import subprocess
import sys
import tempfile
import threading
import time

HERE = os.path.dirname(os.path.abspath(__file__))
BROWSER_DIR = os.path.dirname(HERE)
if BROWSER_DIR not in sys.path:
    sys.path.insert(0, BROWSER_DIR)

SECRET = "bench-relay-secret"
FRAME_S = 0.02
FRAME_SAMPLES = 960
PCM_BYTES = FRAME_SAMPLES * 2
CORPUS_FRAMES = 50
TYPE_AUDIO = 0x01

STOP_LIBOPUS_SYSCALL_SHARE = 0.70   # rule A (roadmap-proposed)
STOP_RELAY_OF_CONTAINER = 0.05      # rule B (roadmap-proposed)
PLAN_OVERHEAD_SHARE = 0.20          # plan gate: UBAG-controlled overhead
PLAN_CPU_SAVING = 0.25              # plan gate: CPU (or memory) saving
EXPECTED_SPEC = ("s16le", 1, 48000)  # what the relay's parec asks for


# --- pure helpers (unit tested in test_bench_relay_rules.py) ---------------

def pctl(values, q):
    """q-quantile (0..1) of a non-empty list, same indexing as the relay stats."""
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, int(q * len(ordered)))]


def _ms(values):
    if not values:
        return {"n": 0, "p50_ms": None, "p95_ms": None, "max_ms": None}
    return {"n": len(values), "p50_ms": round(pctl(values, 0.5) * 1000, 3),
            "p95_ms": round(pctl(values, 0.95) * 1000, 3), "max_ms": round(max(values) * 1000, 3)}


_OPUS_RE = re.compile(r"(?:^|[\s;(_])(?:opus_|celt_|silk_)|libopus", re.I)
_SYSCALL_RE = re.compile(
    r"(?:^|[\s_])(?:read|write|recv|recvfrom|recvmsg|send|sendto|sendmsg|sendall|poll|select|epoll_wait|syscall)(?:\s|\(|$)")


def classify_stack(frames):
    """Bucket one sampled stack: any libopus frame -> 'libopus'; else a syscall
    wrapper at the leaf -> 'syscall'; else 'other'. Heuristic on frame names."""
    if any(_OPUS_RE.search(f) for f in frames):
        return "libopus"
    if frames and _SYSCALL_RE.search(frames[-1]):
        return "syscall"
    return "other"


def parse_pyspy_raw(text):
    """py-spy `-f raw` lines are `frame;frame;...;leaf <count>`."""
    totals = {"libopus": 0, "syscall": 0, "other": 0}
    for line in text.splitlines():
        stack, _, count = line.rstrip().rpartition(" ")
        if not stack or not count.isdigit():
            continue
        totals[classify_stack(stack.split(";"))] += int(count)
    samples = sum(totals.values())
    out = {"samples": samples, **totals}
    out["libopus_syscall_share"] = round((totals["libopus"] + totals["syscall"]) / samples, 4) if samples else None
    return out


_SPEC_RE = re.compile(r"^(\S+)\s+(\d+)ch\s+(\d+)Hz")


def parse_pactl_short(text):
    """`pactl list short sinks|sources` -> [{name, spec:(fmt,ch,rate)|None}]."""
    rows = []
    for line in text.splitlines():
        parts = line.split("\t")
        if len(parts) < 4:
            continue
        m = _SPEC_RE.match(parts[3].strip())
        rows.append({"name": parts[1], "spec": (m.group(1), int(m.group(2)), int(m.group(3))) if m else None})
    return rows


def pulse_findings(sinks, sources):
    """Flag devices whose native sample spec differs from the relay's 48 kHz mono
    s16le stream: PulseAudio then converts inside the server (hidden CPU that
    belongs to the container, not to the relay process)."""
    wanted = [("sink", "ubag_provider_sink", sinks), ("source", "ubag_provider_sink.monitor", sources),
              ("source", "ubag_virtual_mic", sources)]
    checked, mismatches = [], []
    for kind, name, rows in wanted:
        row = next((r for r in rows if r["name"] == name), None)
        item = {"kind": kind, "name": name, "spec": list(row["spec"]) if row and row["spec"] else None,
                "found": row is not None}
        checked.append(item)
        if row and row["spec"] and row["spec"] != EXPECTED_SPEC:
            mismatches.append(item)
    return {"expected": list(EXPECTED_SPEC), "checked": checked, "mismatches": mismatches,
            "hidden_resampling_suspected": bool(mismatches)}


def evaluate_stop_rule(runs, container_cpu_pct=None):
    """Pre-registered stop rule over all measured relay sessions.

    A run needs real libopus and a real FIFO to count; otherwise the verdict is
    'invalid'. Rule A pools CPU attribution over every session. Rule B needs the
    container's CPU (percent of one core during one live call, measured
    separately); without it, B is unevaluated and the verdict is 'inconclusive'
    unless A already fires. The plan gate is reported alongside, never mixed in.
    """
    sessions = [s for r in runs for s in r["per_session"]]
    out = {"rule": {"A": f"libopus+syscalls >= {STOP_LIBOPUS_SYSCALL_SHARE:.0%} of relay CPU",
                    "B": f"relay < {STOP_RELAY_OF_CONTAINER:.0%} of container CPU"}}
    if not runs or not sessions or any(r.get("codec") != "libopus" or r.get("fifo_kind") != "fifo" for r in runs):
        out.update(verdict="invalid", reason="needs real libopus and a real FIFO (Linux, browser image)")
        return out
    cpu = sum(s["process_cpu_s"] for s in sessions)
    attributed = sum(s["libopus_cpu_s"] + s["syscall_cpu_s"] for s in sessions)
    if cpu <= 0:
        out.update(verdict="invalid", reason="no relay CPU measured")
        return out
    share = attributed / cpu
    first = min(runs, key=lambda r: r["sessions"])["per_session"]  # smallest N = the per-call reference
    mean_pct = sum(s["cpu_pct_of_core"] for s in first) / len(first)
    a_fired = share >= STOP_LIBOPUS_SYSCALL_SHARE
    ratio = (mean_pct / container_cpu_pct) if container_cpu_pct else None
    b_fired = None if ratio is None else ratio < STOP_RELAY_OF_CONTAINER
    out["A"] = {"libopus_syscall_share": round(share, 4), "fired": a_fired}
    out["B"] = {"relay_cpu_pct_of_core_n1": round(mean_pct, 3), "container_cpu_pct_per_call": container_cpu_pct,
                "relay_share_of_container": None if ratio is None else round(ratio, 4), "fired": b_fired}
    if a_fired or b_fired:
        out["verdict"] = "stop"
    elif b_fired is None:
        out["verdict"] = "inconclusive"
        out["reason"] = "rule A did not fire; rule B needs --container-cpu-pct from a real container"
    else:
        out["verdict"] = "continue"
    overhead = 1.0 - share
    material = None if ratio is None else ratio >= STOP_RELAY_OF_CONTAINER
    cpu_met = overhead >= PLAN_OVERHEAD_SHARE
    if cpu_met and material:
        gate = "met"
    elif not cpu_met:
        gate = "not_met_on_cpu_memory_unevaluated"
    else:
        gate = "unevaluated"
    out["plan_gate"] = {
        "python_overhead_share": round(overhead, 4), "overhead_ge_20pct": cpu_met,
        "cpu_saving_upper_bound_ge_25pct": overhead >= PLAN_CPU_SAVING,
        "memory_saving": "unevaluated: needs a Rust prototype's RSS",
        "material_in_mixed_workload": material, "verdict": gate,
        "note": "overhead = relay CPU outside libopus+syscalls, the part a rewrite could remove",
    }
    return out


# --- signal corpus ----------------------------------------------------------

def _tri(i, period, amp):
    half = period // 2
    phase = i % period
    ramp = phase if phase < half else period - phase
    return ramp * 2 * amp // half - amp


def pcm_corpus(frames=CORPUS_FRAMES):
    """1 s of deterministic voiced-ish audio (two tones + LCG noise), integer math."""
    x, out = 12345, []
    for f in range(frames):
        buf = bytearray()
        for i in range(FRAME_SAMPLES):
            x = (x * 1103515245 + 12345) & 0x7FFFFFFF
            n = f * FRAME_SAMPLES + i
            s = _tri(n, 96, 6000) + _tri(n, 37, 2500) + (x >> 16) % 1001 - 500
            buf += s.to_bytes(2, "little", signed=True)
        out.append(bytes(buf))
    return out


def load_relay():
    spec = importlib.util.spec_from_file_location("audio_relay", os.path.join(BROWSER_DIR, "audio-relay.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


# --- child: one instrumented relay process ---------------------------------

class _Acc:
    """Per-thread CPU buckets (each pump thread writes only its own dict)."""

    def __init__(self):
        self._local, self._all, self._lock = threading.local(), [], threading.Lock()

    def add(self, bucket, seconds):
        d = getattr(self._local, "d", None)
        if d is None:
            d = self._local.d = {}
            with self._lock:
                self._all.append(d)
        d[bucket] = d.get(bucket, 0.0) + seconds

    def snapshot(self):
        with self._lock:
            dicts = list(self._all)
        out = {}
        for d in dicts:
            for k, v in list(d.items()):
                out[k] = out.get(k, 0.0) + v
        return out


def _timed(acc, bucket, fn):
    def wrapper(*a, **kw):
        t = time.thread_time()
        try:
            return fn(*a, **kw)
        finally:
            acc.add(bucket, time.thread_time() - t)
    return wrapper


class _TimedStdout:
    def __init__(self, real, acc):
        self._real = real
        self.read = _timed(acc, "syscall", real.read)

    def __getattr__(self, name):
        return getattr(self._real, name)


class _TimedOs:
    def __init__(self, real, acc):
        self._real = real
        self.write = _timed(acc, "syscall", real.write)

    def __getattr__(self, name):
        return getattr(self._real, name)


class _WakeProbe(threading.Thread):
    """Sleeps 10 ms in a loop and records the overshoot: the relay process's
    scheduler + GIL wake latency under the real load."""

    def __init__(self):
        super().__init__(daemon=True)
        self.lock, self.samples = threading.Lock(), []

    def run(self):
        while True:
            t = time.perf_counter()
            time.sleep(0.01)
            lag = max(time.perf_counter() - t - 0.01, 0.0)
            with self.lock:
                if len(self.samples) < 200000:
                    self.samples.append(lag)

    def reset(self):
        with self.lock:
            self.samples = []

    def snapshot(self):
        with self.lock:
            return list(self.samples)


def fake_parec(t0_file):
    """Stand-in for `parec` on the provider monitor: one 20 ms PCM frame every
    20 ms on an absolute schedule; records its start (monotonic) for turnaround."""
    corpus = pcm_corpus()
    t0 = time.monotonic()
    with open(t0_file, "w") as fh:
        fh.write(repr(t0))
    out, k = sys.stdout.buffer, 0
    try:
        while True:
            wait = t0 + k * FRAME_S - time.monotonic()
            if wait > 0:
                time.sleep(wait)
            out.write(corpus[k % len(corpus)])
            out.flush()
            k += 1
    except (BrokenPipeError, OSError, KeyboardInterrupt):
        return


def serve_relay_child(args):
    os.environ.update({"UBAG_VOICE_RELAY_SECRET": SECRET, "UBAG_VOICE_RELAY_STATS": "1",
                       "UBAG_VOICE_MIC_PIPE": args.mic_pipe})
    import opus_bridge
    if args.fake_codec:
        from conftest import FakeLib
        fake = FakeLib()
        opus_bridge._load_libopus = lambda: fake
    relay = load_relay()
    acc = _Acc()
    real_load = opus_bridge._load_libopus

    class Proxy:  # times only the native codec calls
        def __init__(self, lib):
            self._lib = lib
            self.opus_decode = _timed(acc, "libopus", lib.opus_decode)
            self.opus_encode = _timed(acc, "libopus", lib.opus_encode)

        def __getattr__(self, name):
            return getattr(self._lib, name)

    opus_bridge._load_libopus = lambda: Proxy(real_load())
    relay.read_frame = _timed(acc, "syscall", relay.read_frame)
    relay.write_frame = _timed(acc, "syscall", relay.write_frame)
    relay.os = _TimedOs(os, acc)
    if os.name == "nt":  # smoke on Windows: regular file as the "FIFO"
        relay.open_mic_fifo = lambda timeout_s=2.0: os.open(args.mic_pipe, os.O_WRONLY | os.O_BINARY)

    def spawn():
        proc = subprocess.Popen([sys.executable, os.path.abspath(__file__), "--fake-parec", "--t0-file", args.t0_file],
                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        proc.stdout = _TimedStdout(proc.stdout, acc)
        return proc

    relay.spawn_monitor = spawn
    relay._devices_ready.set()
    probe = _WakeProbe()
    probe.start()

    def control():
        base = None
        for line in sys.stdin:
            cmd = line.strip()
            if cmd == "start":
                base = (acc.snapshot(), time.process_time())
                probe.reset()
            elif cmd == "dump" and base:
                snap, cpu = acc.snapshot(), time.process_time() - base[1]
                rec = {"process_cpu_s": cpu, "libopus_cpu_s": snap.get("libopus", 0.0) - base[0].get("libopus", 0.0),
                       "syscall_cpu_s": snap.get("syscall", 0.0) - base[0].get("syscall", 0.0),
                       "wake_lag": _ms(probe.snapshot())}
                with open(args.out + ".tmp", "w") as fh:
                    json.dump(rec, fh)
                os.replace(args.out + ".tmp", args.out)
        os._exit(0)  # parent closed stdin

    threading.Thread(target=control, daemon=True).start()
    server = relay.make_server("127.0.0.1:0")
    with open(args.ready_file + ".tmp", "w") as fh:
        fh.write(str(server.getsockname()[1]))
    os.replace(args.ready_file + ".tmp", args.ready_file)
    relay.serve_forever(server)


# --- parent: load generator --------------------------------------------------

def read_proc(pid):
    """(rss_kib, threads) from /proc, or (None, None) off Linux."""
    try:
        with open(f"/proc/{pid}/status") as fh:
            text = fh.read()
        rss = int(re.search(r"VmRSS:\s+(\d+)", text).group(1))
        thr = int(re.search(r"Threads:\s+(\d+)", text).group(1))
        return rss, thr
    except (OSError, AttributeError):
        return None, None


class Load:
    """One paced client: sends 20 ms Opus packets, reads the speaker frames and
    the mic FIFO the relay writes into."""

    def __init__(self, relay, port, packets, fifo_path, sid):
        self.relay, self.port, self.packets, self.fifo_path, self.sid = relay, port, packets, fifo_path, sid
        self.stop = threading.Event()
        self.send_ts, self.speaker_ts, self.fifo_ts, self.errors = [], [], [], []
        self.sock = None
        self.threads = []

    def open(self):
        r = self.relay
        self._open_reader()
        self.sock = socket.create_connection(("127.0.0.1", self.port), timeout=10)
        exp = int(time.time()) + 60
        r.write_control(self.sock, {"op": "hello", "session_id": self.sid, "exp": exp,
                                    "token": r.relay_token(SECRET.encode(), self.sid, exp)})
        ftype, payload = r.read_frame(self.sock)
        if ftype != 2 or json.loads(payload) != {"op": "ready"}:
            raise RuntimeError(f"relay refused: {payload!r}")
        self.sock.settimeout(1.0)
        for fn in (self._send, self._recv, self._fifo):
            t = threading.Thread(target=fn, daemon=True)
            t.start()
            self.threads.append(t)

    def _send(self):
        t0, k = time.monotonic(), 0
        try:
            while not self.stop.is_set():
                wait = t0 + k * FRAME_S - time.monotonic()
                if wait > 0:
                    time.sleep(wait)
                self.send_ts.append(time.monotonic())
                self.relay.write_frame(self.sock, TYPE_AUDIO, self.packets[k % len(self.packets)])
                k += 1
        except OSError as exc:
            if not self.stop.is_set():
                self.errors.append(f"send: {exc}")

    def _recv(self):
        while not self.stop.is_set():
            try:
                ftype, payload = self.relay.read_frame(self.sock)
            except socket.timeout:
                continue
            except (ConnectionError, OSError) as exc:
                if not self.stop.is_set():
                    self.errors.append(f"recv: {exc}")
                return
            if ftype == TYPE_AUDIO:
                self.speaker_ts.append(time.monotonic())
            else:
                self.errors.append(f"control: {payload!r}")

    def _open_reader(self):
        """Reader end first: the relay's non-blocking FIFO open fails (ENXIO)
        until a reader exists."""
        if hasattr(os, "mkfifo"):
            self.fd = os.open(self.fifo_path, os.O_RDONLY | os.O_NONBLOCK)
        else:  # regular file stand-in (Windows smoke): tail it
            self.fh = open(self.fifo_path, "rb")

    def _read_some(self):
        if hasattr(os, "mkfifo"):
            if not select.select([self.fd], [], [], 0.1)[0]:
                return b""
            try:
                return os.read(self.fd, 65536)
            except BlockingIOError:
                return b""
        time.sleep(0.002)
        return self.fh.read()

    def _fifo(self):
        buf = bytearray()
        while not self.stop.is_set():
            data = self._read_some()
            if not data:
                continue
            buf += data
            while len(buf) >= PCM_BYTES:
                del buf[:PCM_BYTES]
                self.fifo_ts.append(time.monotonic())

    def close(self):
        self.stop.set()
        try:
            self.sock.close()
        except OSError:
            pass
        for t in self.threads:
            t.join(2)


def parse_stats_lines(path):
    """Merge the relay's `audio-relay: stats {json}` lines: last counters, worst p95."""
    merged = {}
    try:
        with open(path, encoding="utf-8", errors="replace") as fh:
            lines = [l for l in fh if l.startswith("audio-relay: stats ")]
    except OSError:
        return merged
    for line in lines:
        rec = json.loads(line[len("audio-relay: stats "):])
        for k, v in rec.items():
            if k.endswith(("_p95_ms", "_max_ms")) and isinstance(v, (int, float)):
                merged[k] = max(merged.get(k, 0), v)
            elif isinstance(v, (int, float)) and not isinstance(v, bool):
                merged[k] = v
    return merged


def start_pyspy(pid, seconds, workdir):
    exe = shutil.which("py-spy")
    if not exe:
        return {"skipped": "py-spy not on PATH (pip install py-spy; needs --cap-add SYS_PTRACE)"}
    jobs = {}
    for label, extra in (("native", ["--native"]), ("native_gil", ["--native", "--gil"])):
        path = os.path.join(workdir, f"pyspy-{label}.raw")
        jobs[label] = (path, subprocess.Popen([exe, "record", *extra, "-r", "100", "-d", str(int(seconds)), "-f", "raw",
                                               "-o", path, "-p", str(pid)],
                                              stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True))
    return jobs


def finish_pyspy(jobs, seconds):
    if "skipped" in jobs:
        return jobs
    out = {}
    for label, (path, proc) in jobs.items():
        _, err = proc.communicate(timeout=seconds + 30)
        if proc.returncode != 0 or not os.path.exists(path):
            out[label] = {"error": (err or "py-spy failed").strip()[:300]}
            continue
        with open(path, encoding="utf-8", errors="replace") as fh:
            out[label] = parse_pyspy_raw(fh.read())
    return out


def run_n(n, args, relay, packets):
    codec = "fake" if args.fake_codec else "libopus"
    tmp = tempfile.mkdtemp(prefix="relay-bench-")
    children, loads = [], []
    try:
        for i in range(n):
            base = os.path.join(tmp, f"r{i}")
            fifo = base + ".mic"
            if hasattr(os, "mkfifo"):
                os.mkfifo(fifo, 0o600)
            else:
                open(fifo, "wb").close()
            paths = {k: base + "." + k for k in ("out", "ready", "t0", "err")}
            cmd = [sys.executable, os.path.abspath(__file__), "--serve-relay", "--mic-pipe", fifo,
                   "--out", paths["out"], "--ready-file", paths["ready"], "--t0-file", paths["t0"]]
            if args.fake_codec:
                cmd.append("--fake-codec")
            proc = subprocess.Popen(cmd, stdin=subprocess.PIPE, stdout=subprocess.DEVNULL,
                                    stderr=open(paths["err"], "w"), text=True)
            children.append((proc, paths, fifo))
        for proc, paths, _ in children:
            deadline = time.monotonic() + 15
            while not os.path.exists(paths["ready"]):
                if proc.poll() is not None or time.monotonic() > deadline:
                    raise RuntimeError("relay child did not start: " + open(paths["err"]).read()[-500:])
                time.sleep(0.05)
        for i, (proc, paths, fifo) in enumerate(children):
            load = Load(relay, int(open(paths["ready"]).read()), packets, fifo, f"bench-{n}-{i}")
            load.open()
            loads.append(load)
        time.sleep(args.warmup)
        win_start = time.monotonic()
        before = [read_proc(p.pid) for p, _, _ in children]
        for proc, _, _ in children:
            proc.stdin.write("start\n")
            proc.stdin.flush()
        spy_s = max(args.duration - 1, 2)
        spy_jobs = start_pyspy(children[0][0].pid, spy_s, tmp) if args.pyspy else None
        rss_peak = [b[0] for b in before]
        end = win_start + args.duration
        while time.monotonic() < end:
            time.sleep(min(1.0, max(end - time.monotonic(), 0)))
            for i, (proc, _, _) in enumerate(children):
                rss = read_proc(proc.pid)[0]
                if rss is not None and (rss_peak[i] is None or rss > rss_peak[i]):
                    rss_peak[i] = rss
        win_end = time.monotonic()
        spy = finish_pyspy(spy_jobs, spy_s) if spy_jobs else None
        for proc, _, _ in children:
            proc.stdin.write("dump\n")
            proc.stdin.flush()
        for _, paths, _ in children:
            deadline = time.monotonic() + 10
            while not os.path.exists(paths["out"]) and time.monotonic() < deadline:
                time.sleep(0.05)
        for load in loads:
            load.close()
        time.sleep(0.3)  # let each relay write its final stats line
        per = []
        for i, (proc, paths, _) in enumerate(children):
            rec = json.load(open(paths["out"]))
            stats = parse_stats_lines(paths["err"])
            t0 = float(open(paths["t0"]).read())
            load = loads[i]
            spk = [ts - (t0 + k * FRAME_S) for k, ts in enumerate(load.speaker_ts) if win_start <= ts <= win_end]
            clean = stats.get("mic_fifo_dropped", 0) == 0 and stats.get("mic_decode_errors", 0) == 0
            mic = [f - s for s, f in zip(load.send_ts, load.fifo_ts) if win_start <= s <= win_end] if clean else []
            window = win_end - win_start
            per.append({
                "process_cpu_s": round(rec["process_cpu_s"], 4),
                "cpu_pct_of_core": round(100 * rec["process_cpu_s"] / window, 3),
                "cpu_s_per_call_minute": round(rec["process_cpu_s"] / (window / 60), 3),
                "libopus_cpu_s": round(rec["libopus_cpu_s"], 4), "syscall_cpu_s": round(rec["syscall_cpu_s"], 4),
                "other_cpu_s": round(max(rec["process_cpu_s"] - rec["libopus_cpu_s"] - rec["syscall_cpu_s"], 0), 4),
                "libopus_syscall_share": round((rec["libopus_cpu_s"] + rec["syscall_cpu_s"]) / rec["process_cpu_s"], 4)
                if rec["process_cpu_s"] > 0 else None,
                "rss_peak_kib": rss_peak[i], "threads": read_proc(proc.pid)[1],
                "wake_lag": rec["wake_lag"],
                "mic_turnaround": _ms(mic) if clean else {"skipped": "FIFO drops or decode errors; indices unaligned"},
                "speaker_turnaround": _ms(spk),
                "mic_fifo_dropped": stats.get("mic_fifo_dropped"), "mic_decode_errors": stats.get("mic_decode_errors"),
                "speaker_encode_errors": stats.get("speaker_encode_errors"),
                "relay_stats_mic_p95_ms": stats.get("mic_p95_ms"), "relay_stats_speaker_p95_ms": stats.get("speaker_p95_ms"),
                "client_errors": load.errors[:3],
            })
        return {"sessions": n, "window_s": round(win_end - win_start, 2), "codec": codec,
                "fifo_kind": "fifo" if hasattr(os, "mkfifo") else "file", "per_session": per, "pyspy": spy}
    finally:
        for load in loads:
            load.close()
        for proc, _, _ in children:
            try:
                proc.stdin.close()
                proc.terminate()
                proc.wait(5)
            except Exception:
                proc.kill()
        shutil.rmtree(tmp, ignore_errors=True)


def pulse_check():
    pactl = os.environ.get("UBAG_VOICE_PACTL") or "pactl"
    try:
        sinks, sources = (subprocess.run([pactl, "list", "short", k], capture_output=True, text=True, timeout=5).stdout
                          for k in ("sinks", "sources"))
    except (OSError, subprocess.SubprocessError) as exc:
        return {"skipped": f"pactl unavailable: {exc}"}
    return pulse_findings(parse_pactl_short(sinks), parse_pactl_short(sources))


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--sessions", default="1,5,10", help="comma list of concurrent relay processes")
    p.add_argument("--duration", type=float, default=30.0, help="measurement window per N (s)")
    p.add_argument("--warmup", type=float, default=2.0)
    p.add_argument("--out", help="write the JSON report here (default: stdout)")
    p.add_argument("--host-class", default="unspecified", help="free text: where this ran")
    p.add_argument("--lab-host", action="store_true", help="isolated lab host: numbers are authoritative")
    p.add_argument("--container-cpu-pct", type=float, help="whole real container CPU (percent of one core) per live call, for rule B")
    p.add_argument("--pulse-check", action="store_true", help="pactl list short sinks/sources: hidden resampling check")
    p.add_argument("--pyspy", action="store_true", help="py-spy --native and --native --gil on the first relay of each run")
    p.add_argument("--fake-codec", action="store_true", help="harness smoke with FakeLib (verdict invalid)")
    for flag in ("--serve-relay", "--fake-parec"):
        p.add_argument(flag, action="store_true", help=argparse.SUPPRESS)
    for flag in ("--mic-pipe", "--ready-file", "--t0-file"):
        p.add_argument(flag, help=argparse.SUPPRESS)
    args = p.parse_args(argv)
    if args.fake_parec:
        return fake_parec(args.t0_file)
    if args.serve_relay:
        # the child reuses --out as its dump path
        return serve_relay_child(args)

    import opus_bridge
    if args.fake_codec:
        packets, version = [bytes([20, 7])] * CORPUS_FRAMES, "FakeLib"
    else:
        try:
            version = opus_bridge.opus_version()
        except (OSError, AttributeError) as exc:
            print(f"libopus not available ({exc}); run inside the browser image", file=sys.stderr)
            return 2
        with opus_bridge.OpusEncoder() as enc:
            packets = [enc.encode(f, FRAME_SAMPLES) for f in pcm_corpus()]
    relay = load_relay()
    runs = []
    for n in (int(x) for x in args.sessions.split(",")):
        print(f"bench_relay: N={n} for {args.duration:.0f}s ...", file=sys.stderr)
        runs.append(run_n(n, args, relay, packets))
    report = {
        "schema": "ubag.relay_baseline/1", "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "authoritative": bool(args.lab_host), "host_class": args.host_class,
        "host": {"platform": platform.platform(), "cpu_count": os.cpu_count(), "python": platform.python_version()},
        "libopus_version": version, "runs": runs,
        "pulse": pulse_check() if args.pulse_check else None,
        "stop_rule": evaluate_stop_rule(runs, args.container_cpu_pct),
    }
    text = json.dumps(report, indent=2)
    if args.out:
        with open(args.out, "w", encoding="utf-8", newline="\n") as fh:
            fh.write(text + "\n")
    else:
        print(text)
    verdict = report["stop_rule"]["verdict"]
    print(f"bench_relay: verdict={verdict} authoritative={report['authoritative']}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    sys.exit(main())

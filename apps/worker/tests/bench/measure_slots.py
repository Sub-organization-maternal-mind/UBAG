#!/usr/bin/env python3
"""Worker slot-cost measurement (perf-fleet P0.8). Manual tool, not run in CI.

Spawns N daemon processes speaking the real stdin/stdout framing
(ubag_worker.live.daemon_protocol.serve) and reports, per N in --slots:
  * RSS of the process tree: idle, warm (after one job), mid-stream
  * CPU seconds consumed per phase
  * first-event latency (request written -> first stdout line)
  * optional py-spy CPU split (if `py-spy` is on PATH)

Modes:
  default       protocol-overhead only: a stub daemon (no browser) emits
                --events events with --event-sleep-ms between them. Measures
                Python interpreter + framing cost, NOT Chrome.
  --real        spawns apps/worker/run_worker_daemon.py (needs Chrome/CDP and a
                real session; provider timings are only meaningful there).
  --playwright  additionally reports RSS of a bare sync_playwright().start()
                (the Node driver cost each real slot pays), if playwright is
                installed.

RSS/CPU use psutil when available, else /proc (Linux). Without either the
columns print n/a. Numbers taken on a laptop are NON-AUTHORITATIVE.

Usage: python apps/worker/tests/bench/measure_slots.py --slots 1,2,4 --json
Expectation recorded for the Rust gate: worker time is CDP round-trips plus
0.1-0.4s sleeps, so there is no worker-runtime Rust candidate.
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import time
from pathlib import Path

WORKER_DIR = Path(__file__).resolve().parents[2]
try:
    import psutil  # type: ignore
except ImportError:  # optional
    psutil = None


def _tree_pids(pid):
    if psutil is None:
        return [pid]
    try:
        p = psutil.Process(pid)
        return [pid] + [c.pid for c in p.children(recursive=True)]
    except psutil.Error:
        return []


def rss_mib(pids):
    total, ok = 0, False
    for pid in pids:
        try:
            if psutil is not None:
                total += psutil.Process(pid).memory_info().rss
                ok = True
            else:
                with open("/proc/%d/status" % pid) as f:
                    for line in f:
                        if line.startswith("VmRSS:"):
                            total += int(line.split()[1]) * 1024
                            ok = True
        except Exception:  # noqa: BLE001 - n/a is the honest answer
            pass
    return round(total / 1048576, 1) if ok else None


def cpu_s(pids):
    total, ok = 0.0, False
    for pid in pids:
        try:
            if psutil is not None:
                t = psutil.Process(pid).cpu_times()
                total += t.user + t.system
                ok = True
            else:
                with open("/proc/%d/stat" % pid) as f:
                    parts = f.read().rsplit(")", 1)[1].split()
                total += (int(parts[11]) + int(parts[12])) / os.sysconf("SC_CLK_TCK")
                ok = True
        except Exception:  # noqa: BLE001
            pass
    return round(total, 3) if ok else None


STUB_CHILD = r"""
import sys, time, os
sys.path.insert(0, os.environ["UBAG_BENCH_WORKER_DIR"])
from ubag_worker.live.daemon_protocol import serve
N = int(os.environ["UBAG_BENCH_EVENTS"]); S = float(os.environ["UBAG_BENCH_SLEEP_S"])
class Stub:
    def run_job(self, payload):
        for i in range(N):
            time.sleep(S)
            yield {"type": "token", "sequence": i + 1, "data": {"text": "x"}}
        yield {"type": "completed", "sequence": N + 1, "data": {"status": "completed"}}
    def close(self): pass
raise SystemExit(serve(sys.stdin, sys.stdout, Stub()))
"""


class Slot:
    def __init__(self, args):
        env = dict(os.environ)
        env.update(
            UBAG_BENCH_WORKER_DIR=str(WORKER_DIR),
            UBAG_BENCH_EVENTS=str(args.events),
            UBAG_BENCH_SLEEP_S=str(args.event_sleep_ms / 1000.0),
        )
        cmd = (
            [sys.executable, str(WORKER_DIR / "run_worker_daemon.py")]
            if args.real
            else [sys.executable, "-c", STUB_CHILD]
        )
        self.p = subprocess.Popen(
            cmd, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, bufsize=1, env=env
        )
        self.n = 0

    def send(self, target):
        self.n += 1
        req = {"job_id": "bench_%d_%d" % (self.p.pid, self.n), "deadline_s": 60,
               "payload": {"job_id": "bench", "job": {"target": target}}}
        self.t0 = time.perf_counter()
        self.p.stdin.write(json.dumps(req) + "\n")
        self.p.stdin.flush()

    def first_event(self):
        self.p.stdout.readline()
        return (time.perf_counter() - self.t0) * 1000

    def drain(self):
        while True:
            line = self.p.stdout.readline()
            if not line or '"__ubag_job_end__"' in line:
                return

    def close(self):
        try:
            self.p.stdin.close()
            self.p.wait(timeout=10)
        except Exception:  # noqa: BLE001
            self.p.kill()


def pyspy_top(pid, seconds):
    exe = shutil.which("py-spy")
    if not exe:
        return None
    try:
        out = subprocess.run(
            [exe, "record", "-d", str(seconds), "-f", "raw", "-o", "-", "-p", str(pid)],
            capture_output=True, text=True, timeout=seconds + 15,
        ).stdout
    except Exception:  # noqa: BLE001
        return None
    counts = {}
    for line in out.splitlines():
        stack, _, n = line.rpartition(" ")
        if n.isdigit():
            leaf = stack.split(";")[-1]
            counts[leaf] = counts.get(leaf, 0) + int(n)
    total = sum(counts.values()) or 1
    return {k: round(100 * v / total, 1) for k, v in sorted(counts.items(), key=lambda kv: -kv[1])[:5]}


def measure(n, args):
    slots = [Slot(args) for _ in range(n)]
    pids = [pid for s in slots for pid in _tree_pids(s.p.pid)]
    time.sleep(1.0)  # let interpreters settle
    res = {"slots": n, "mode": "real" if args.real else "stub"}
    res["rss_mib_idle"], c0 = rss_mib(pids), cpu_s(pids)
    for s in slots:
        s.send(args.target)
    lat = [s.first_event() for s in slots]
    res["first_event_ms"] = [round(x, 1) for x in lat]
    pids = [pid for s in slots for pid in _tree_pids(s.p.pid)]
    res["rss_mib_midstream"] = rss_mib(pids)
    res["pyspy_top_leaf_pct"] = pyspy_top(slots[0].p.pid, 2) if args.pyspy else None
    for s in slots:
        s.drain()
    pids = [pid for s in slots for pid in _tree_pids(s.p.pid)]
    res["rss_mib_warm"] = rss_mib(pids)
    c1 = cpu_s(pids)
    res["cpu_s_first_job"] = None if c0 is None or c1 is None else round(c1 - c0, 3)
    for s in slots:
        s.close()
    return res


def playwright_rss():
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        return None
    pw = sync_playwright().start()
    try:
        return rss_mib(_tree_pids(os.getpid()))
    finally:
        pw.stop()


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--slots", default="1,2,4")
    ap.add_argument("--events", type=int, default=20)
    ap.add_argument("--event-sleep-ms", type=float, default=100.0)
    ap.add_argument("--target", default="mock")
    ap.add_argument("--real", action="store_true")
    ap.add_argument("--playwright", action="store_true")
    ap.add_argument("--pyspy", action="store_true")
    ap.add_argument("--json", action="store_true")
    args = ap.parse_args()

    out = {"non_authoritative": True, "psutil": psutil is not None,
           "results": [measure(int(x), args) for x in args.slots.split(",")]}
    if args.playwright:
        out["bare_playwright_driver_rss_mib"] = playwright_rss()
    if args.json:
        print(json.dumps(out, indent=1))
    else:
        for r in out["results"]:
            print(r)
        if args.playwright:
            print("bare sync_playwright rss MiB:", out["bare_playwright_driver_rss_mib"])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

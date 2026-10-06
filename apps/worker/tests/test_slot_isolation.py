"""Slot mode (UBAG_WORKER_SLOT_ID): per-slot tab registries and the physical-session lock.

Unset slot id must stay byte-identical to today; with it, slots never close each
other's tabs, orphan registries of vanished slots are reaped, and one physical
session is held by one slot process at a time (flock, freed on SIGKILL).
"""
import json
import os
import signal
import subprocess
import sys

import pytest
from ubag_worker.live import daemon as daemon_mod
from ubag_worker.live import identity_lock, page_driver

WORKER_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
POSIX = hasattr(signal, "SIGKILL") and identity_lock.fcntl is not None


@pytest.fixture
def slot_env(tmp_path, monkeypatch):
    monkeypatch.setenv("UBAG_CHAT_LEDGER_PATH", str(tmp_path / "ledger.jsonl"))
    monkeypatch.delenv("UBAG_WORKER_SLOT_ID", raising=False)
    monkeypatch.delenv("UBAG_WORKER_IDENTITY_LOCK", raising=False)
    page_driver._LIVE_TARGET_IDS.clear()
    return tmp_path


class _Resp:
    def read(self):
        return b""


def test_two_slots_have_distinct_registries_and_never_close_each_other(slot_env, monkeypatch):
    monkeypatch.setenv("UBAG_WORKER_SLOT_ID", "0")
    p0 = page_driver._page_registry_path()
    page_driver._registry_update(add="SLOT0TAB0001")
    monkeypatch.setenv("UBAG_WORKER_SLOT_ID", "1")
    p1 = page_driver._page_registry_path()
    assert p0 != p1 and p0.endswith(".slot0.json") and p1.endswith(".slot1.json")

    closed = []
    page_driver._LIVE_TARGET_IDS.clear()  # slot 1 is a fresh process
    page_driver._close_stale_pages("http://b:9223", opener=lambda u, timeout: closed.append(u) or _Resp())
    assert closed == []  # slot 0's live tab is invisible to slot 1
    monkeypatch.setenv("UBAG_WORKER_SLOT_ID", "0")
    assert page_driver._registry_read() == ["SLOT0TAB0001"]


def test_slot_id_is_sanitised(monkeypatch):
    monkeypatch.setenv("UBAG_WORKER_SLOT_ID", "a/b c")
    assert identity_lock.slot_id() == "a_b_c"
    monkeypatch.setenv("UBAG_WORKER_SLOT_ID", "  ")
    assert identity_lock.slot_id() is None


def test_reap_orphan_registries_closes_tabs_of_vanished_slots(slot_env, monkeypatch):
    monkeypatch.setenv("UBAG_WORKER_SLOT_ID", "0")
    d = os.path.dirname(page_driver._page_registry_path())
    role = os.path.basename(page_driver._page_registry_path()).split(".slot")[0]
    for n, ids in (("0", ["KEEP00000001"]), ("1", ["KEEP00000002"]), ("2", ["ORPHAN000002"]), ("3", [])):
        with open(os.path.join(d, "%s.slot%s.json" % (role, n)), "w") as fh:
            json.dump(ids, fh)
    closed = []
    removed = page_driver.reap_orphan_registries(
        2, "http://b:9223", opener=lambda u, timeout: closed.append(u) or _Resp()
    )
    assert removed == 2
    assert closed == ["http://b:9223/json/close/ORPHAN000002"]
    assert sorted(os.listdir(d)) == ["%s.slot%s.json" % (role, n) for n in ("0", "1")]


def test_reap_keeps_registry_when_browser_unreachable(slot_env, monkeypatch):
    monkeypatch.setenv("UBAG_WORKER_SLOT_ID", "0")
    path = page_driver._page_registry_path().replace(".slot0.", ".slot5.")
    with open(path, "w") as fh:
        json.dump(["ORPHAN000005"], fh)

    def boom(url, timeout):
        raise ConnectionRefusedError()

    assert page_driver.reap_orphan_registries(2, "http://b:9223", opener=boom) == 0
    assert os.path.exists(path)


def test_physical_key_ignores_tenant_and_distinguishes_target_and_dir(tmp_path):
    key = identity_lock.physical_session_key
    a = key(endpoint="", user_data_dir=str(tmp_path / "p"), target="chatgpt_web")
    assert a == key(endpoint="", user_data_dir=str(tmp_path / "p" / ".." / "p"), target="chatgpt_web")
    assert a != key(endpoint="", user_data_dir=str(tmp_path / "q"), target="chatgpt_web")
    assert a != key(endpoint="", user_data_dir=str(tmp_path / "p"), target="gemini_web")
    assert key(endpoint="http://B:9223/", user_data_dir="x", target="t") == key(
        endpoint="http://b:9223", user_data_dir="y", target="t"
    )


def test_lock_enabled_only_in_slot_mode_unless_overridden(monkeypatch):
    monkeypatch.delenv("UBAG_WORKER_SLOT_ID", raising=False)
    monkeypatch.delenv("UBAG_WORKER_IDENTITY_LOCK", raising=False)
    assert not identity_lock.identity_lock_enabled()
    monkeypatch.setenv("UBAG_WORKER_SLOT_ID", "0")
    assert identity_lock.identity_lock_enabled()
    monkeypatch.setenv("UBAG_WORKER_IDENTITY_LOCK", "0")
    assert not identity_lock.identity_lock_enabled()


class _RecordingLock:
    log = []

    def __init__(self, key):
        self.key = key

    def __enter__(self):
        _RecordingLock.log.append(("enter", self.key))

    def __exit__(self, *exc):
        _RecordingLock.log.append(("exit", self.key))


def _payload(tenant):
    return {
        "job_id": "job-1",
        "tenant_id": tenant,
        "target": "mock",
        "input": {"prompt": "hi"},
        "options": {"user_data_dir": "profiles/shared"},
    }


def test_daemon_holds_lock_only_in_slot_mode_and_same_key_across_tenants(slot_env, monkeypatch):
    monkeypatch.setattr(daemon_mod, "IdentityLock", _RecordingLock)
    monkeypatch.setattr(daemon_mod.WarmWorkerDaemon, "_run_job", lambda self, payload: iter([{"type": "x"}]))
    d = daemon_mod.WarmWorkerDaemon()

    _RecordingLock.log.clear()
    assert list(d.run_job(_payload("a"))) == [{"type": "x"}]
    assert _RecordingLock.log == []  # not slot mode: no lock

    monkeypatch.setenv("UBAG_WORKER_SLOT_ID", "0")
    list(d.run_job(_payload("a")))
    list(d.run_job(_payload("b")))
    keys = {k for _, k in _RecordingLock.log}
    assert len(keys) == 1  # two tenants, one physical session
    assert [e for e, _ in _RecordingLock.log] == ["enter", "exit", "enter", "exit"]


_HOLDER = """
import sys, time
from ubag_worker.live.identity_lock import IdentityLock
with IdentityLock(sys.argv[1], lock_dir=sys.argv[2]):
    print("locked", flush=True)
    time.sleep(60)
"""


def _spawn(key, lock_dir):
    env = dict(os.environ, UBAG_ADAPTER_OFFLINE="1", PYTHONPATH=WORKER_ROOT)
    return subprocess.Popen(
        [sys.executable, "-c", _HOLDER, key, str(lock_dir)],
        stdout=subprocess.PIPE, text=True, env=env,
    )


def _got_lock(proc, wait_s):
    """True if the child printed 'locked' within wait_s."""
    import select

    ready, _, _ = select.select([proc.stdout], [], [], wait_s)
    return bool(ready) and proc.stdout.readline().strip() == "locked"


@pytest.mark.skipif(not POSIX, reason="flock identity lock is POSIX-only (no-op on Windows)")
def test_flock_blocks_same_key_passes_other_key_and_frees_on_sigkill(tmp_path):
    holder = _spawn("keyA", tmp_path)
    procs = [holder]
    try:
        assert _got_lock(holder, 15)
        waiter = _spawn("keyA", tmp_path)
        procs.append(waiter)
        other = _spawn("keyB", tmp_path)
        procs.append(other)
        assert _got_lock(other, 15)  # a different key proceeds
        assert not _got_lock(waiter, 1.5)  # same key blocks while the holder lives
        holder.send_signal(signal.SIGKILL)
        holder.wait(timeout=10)
        assert _got_lock(waiter, 15)  # kernel released the lock on SIGKILL
    finally:
        for p in procs:
            if p.poll() is None:
                p.kill()
            p.wait()
            p.stdout.close()

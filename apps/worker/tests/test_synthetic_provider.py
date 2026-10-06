"""Tests for the synthetic_chat overlay (P7.1).

The overlay is a loopback-only fake chat (tools/synthetic-provider/server.mjs) registered as an extra live
target when UBAG_SYNTHETIC_PROVIDER=1, so scenarios drive a real browser through the warm daemon.

Layers:

* offline: gating, loopback enforcement, registry/manifest tolerance, engine + daemon behaviour with a
  MockPageDriver (no browser, no node);
* local origin: the selectors are checked against the HTML a real fixture server serves (needs node, no browser);
* browser: the real PlaywrightPageDriver through WarmWorkerDaemon against the fixture. Skipped unless
  Playwright and a launchable Chromium are present (CI has neither).
"""

import json
import os
import queue
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import urllib.request
from pathlib import Path

import pytest
from ubag_worker.adapter_registry import (
    events_for_payload,
    instantiate_adapter,
    load_manifest,
    load_manifests,
    load_registry,
)
from ubag_worker.live.daemon import WarmWorkerDaemon
from ubag_worker.live.engine import LiveSessionEngine
from ubag_worker.live.page_driver import MockPageDriver, create_default_driver
from ubag_worker.live.synthetic import (
    DEFAULT_URL,
    SYNTHETIC_PROVIDER_ID,
    register_synthetic_provider,
    synthetic_provider_enabled,
    validated_synthetic_url,
)

REPO_ROOT = Path(__file__).resolve().parents[3]
WORKER_ROOT = REPO_ROOT / "apps" / "worker"
ADAPTERS = REPO_ROOT / "adapters"
FIXTURE = REPO_ROOT / "tools" / "synthetic-provider" / "server.mjs"
NODE = shutil.which("node")

_CONTEXT = {
    "account_binding_id": "acct_synthetic",
    "consent_ref": "consent_synthetic",
    "automation_scope": ["manual_login", "submit_prompt", "read_response"],
}


def _job(prompt="hello", job_id="job_syn_1", **options):
    return {
        "api_version": "2026-05-22",
        "job_id": job_id,
        "trace_id": "trace_" + job_id,
        "job": {
            "target": SYNTHETIC_PROVIDER_ID,
            "command_type": "chat.prompt",
            "input": {"prompt": prompt},
            "options": options,
            "context": dict(_CONTEXT),
        },
    }


def _selectors(monkeypatch, url=None):
    monkeypatch.setenv("UBAG_SYNTHETIC_PROVIDER", "1")
    if url is None:
        monkeypatch.delenv("UBAG_SYNTHETIC_PROVIDER_URL", raising=False)
    else:
        monkeypatch.setenv("UBAG_SYNTHETIC_PROVIDER_URL", url)
    registry = {}
    assert register_synthetic_provider(registry)
    return registry[SYNTHETIC_PROVIDER_ID]


def _types(events):
    return [event["type"] for event in events]


# --------------------------------------------------------------------------- offline: gating


def test_overlay_is_absent_unless_explicitly_enabled(monkeypatch):
    monkeypatch.delenv("UBAG_SYNTHETIC_PROVIDER", raising=False)
    registry = {}
    assert register_synthetic_provider(registry) is False
    assert registry == {}
    for off in ("", "0", "false", "no", "off", "maybe"):
        monkeypatch.setenv("UBAG_SYNTHETIC_PROVIDER", off)
        assert not synthetic_provider_enabled(), off
    for on in ("1", "true", "YES", "on"):
        monkeypatch.setenv("UBAG_SYNTHETIC_PROVIDER", on)
        assert synthetic_provider_enabled(), on


def test_enabled_registers_a_loopback_target_with_every_core_group(monkeypatch):
    selectors = _selectors(monkeypatch)
    assert selectors.provider_id == "synthetic_chat"
    assert selectors.target_url == DEFAULT_URL
    for group in selectors.all_groups():
        assert group.candidates and all("data-synthetic" in c for c in group.candidates), group.name
    assert selectors.file_input is not None and selectors.new_chat is not None
    # Nothing that could touch a real account: no settings to click, no permanent-delete flow, no voice entry.
    assert not selectors.settings
    assert selectors.delete_chat is None
    assert selectors.voice_control is None
    assert selectors.reasoning is False


def test_configured_origin_is_used(monkeypatch):
    assert _selectors(monkeypatch, "http://localhost:5123/x?signed_out=1").target_url == "http://localhost:5123/x?signed_out=1"


@pytest.mark.parametrize(
    "url",
    [
        "https://127.0.0.1:4799/",
        "http://example.com/",
        "http://127.0.0.1.evil.example:4799/",
        "http://10.0.0.5:4799/",
        "http://0.0.0.0:4799/",
        "http://user@127.0.0.1:4799/",
        "http://127.0.0.1:notaport/",
        "http://:4799/",
        "file:///etc/passwd",
        "about:blank",
    ],
)
def test_non_loopback_origin_fails_closed_and_stays_unregistered(monkeypatch, capsys, url):
    with pytest.raises(ValueError):
        validated_synthetic_url(url)
    monkeypatch.setenv("UBAG_SYNTHETIC_PROVIDER", "1")
    monkeypatch.setenv("UBAG_SYNTHETIC_PROVIDER_URL", url)
    registry = {}
    assert register_synthetic_provider(registry) is False
    assert registry == {}
    assert "not registered" in capsys.readouterr().err


def _providers_in_fresh_process(**env):
    child_env = {k: v for k, v in os.environ.items() if not k.startswith("UBAG_SYNTHETIC")}
    child_env.update(env)
    child_env["PYTHONPATH"] = str(WORKER_ROOT)
    out = subprocess.run(
        [sys.executable, "-c", "import json; from ubag_worker.live.selectors import PROVIDER_SELECTORS as P; print(json.dumps(sorted(P)))"],
        env=child_env, capture_output=True, text=True, timeout=60, check=True,
    ).stdout
    return json.loads(out.strip().splitlines()[-1])


def test_selectors_import_wires_the_overlay_only_when_enabled():
    default = _providers_in_fresh_process()
    assert "synthetic_chat" not in default
    assert {"chatgpt_web", "deepseek_web", "gemini_web", "mistral_lechat", "duckai_web"} <= set(default)
    assert _providers_in_fresh_process(UBAG_SYNTHETIC_PROVIDER="1") == sorted(default + ["synthetic_chat"])
    assert _providers_in_fresh_process(UBAG_SYNTHETIC_PROVIDER="1", UBAG_SYNTHETIC_PROVIDER_URL="http://example.com/") == default


# --------------------------------------------------------------------------- offline: registry tolerance


def test_overlay_registry_is_separate_and_the_default_registry_is_untouched():
    overlay = json.loads((ADAPTERS / "registry.synthetic.json").read_text(encoding="utf-8"))
    assert overlay["extends"] == "registry.json"
    assert [entry["id"] for entry in overlay["adapters"]] == ["synthetic_chat"]
    assert "synthetic_chat" not in {entry["id"] for entry in load_registry()["adapters"]}
    assert "synthetic_chat" not in load_manifests()
    assert "synthetic_chat" not in (ADAPTERS / "registry.json").read_text(encoding="utf-8")


def test_manifest_passes_the_safe_mode_validator_and_resolves_its_entrypoint():
    overlay = json.loads((ADAPTERS / "registry.synthetic.json").read_text(encoding="utf-8"))
    manifest = load_manifest((ADAPTERS / overlay["adapters"][0]["manifest"]).resolve())
    assert manifest["id"] == "synthetic_chat"
    assert manifest["safe_mode"]["manual_login_required"] is True
    assert manifest["synthetic_overlay"]["origin"] == "loopback_only"
    assert manifest["resource_policy"]["network"] == "loopback_only"
    with pytest.raises(NotImplementedError):
        instantiate_adapter(manifest).run({})


def test_default_registry_dispatch_still_refuses_the_target():
    with pytest.raises(NotImplementedError, match="no adapter manifest registered"):
        events_for_payload({"job": {"target": "synthetic_chat", "input": {"prompt": "x"}}})


# --------------------------------------------------------------------------- offline: engine + daemon (no browser)


def test_engine_runs_the_target_with_new_chat_and_no_settings(monkeypatch):
    selectors = _selectors(monkeypatch)
    driver = MockPageDriver(response_text="all good")
    events = LiveSessionEngine(selectors).run(_job("hi"), driver=driver)
    assert _types(events)[0] == "queued" and _types(events)[-1] == "completed"
    assert "session.new_chat" in _types(events) and "session.configured" not in _types(events)
    assert events[-1]["data"]["metadata"]["adapter"] == "synthetic_chat"
    assert driver.submitted_prompt == "hi"


def test_signed_out_page_fails_closed_without_typing_anything(monkeypatch):
    selectors = _selectors(monkeypatch)
    driver = MockPageDriver(authenticated=False, login_after_wait=False)
    events = LiveSessionEngine(selectors).run(_job("hi"), driver=driver)
    types = _types(events)
    assert types[-2:] == ["session.manual_action_required", "blocked"]
    assert "completed" not in types
    assert events[-1]["data"]["reason"] == "manual_login_required"
    assert driver.submitted_prompt is None


def test_deadline_cut_stream_ends_timed_out_with_partial_text_under_strict_flag(monkeypatch):
    selectors = _selectors(monkeypatch)
    monkeypatch.setenv("UBAG_WORKER_STRICT_STREAM_END", "1")
    driver = MockPageDriver(tokens=["partial ", "answer"], scripted_end_reason="deadline")
    events = LiveSessionEngine(selectors).run(_job("hi"), driver=driver)
    assert _types(events)[-1] == "timed_out" and "completed" not in _types(events)
    assert events[-1]["data"]["partial"]["text"] == "partial answer"


def test_warm_daemon_reuses_one_driver_for_the_target(monkeypatch):
    selectors = _selectors(monkeypatch)
    built = []

    def factory(options):
        built.append(MockPageDriver())
        return built[-1]

    daemon = WarmWorkerDaemon(driver_factory=factory, selectors_by_target={"synthetic_chat": selectors})
    for job_id in ("job_a", "job_b"):
        assert _types(list(daemon.run_job(_job("hi", job_id))))[-1] == "completed"
    assert len(built) == 1, "the second job must reuse the warm page"
    daemon.close()
    assert built[0].closed


def test_warm_daemon_never_keeps_a_signed_out_page(monkeypatch):
    selectors = _selectors(monkeypatch)
    built = []

    def factory(options):
        built.append(MockPageDriver(authenticated=False, login_after_wait=False))
        return built[-1]

    daemon = WarmWorkerDaemon(driver_factory=factory, selectors_by_target={"synthetic_chat": selectors})
    assert _types(list(daemon.run_job(_job("hi", "job_c"))))[-1] == "blocked"
    assert built[0].closed
    assert _types(list(daemon.run_job(_job("hi", "job_d"))))[-1] == "blocked"
    assert len(built) == 2, "a blocked page must not be reused"


# --------------------------------------------------------------------------- local origin (node, no browser)


def _start_fixture(*args):
    proc = subprocess.Popen([NODE, str(FIXTURE), "--port", "0", *args], stdout=subprocess.PIPE, text=True)
    lines = queue.Queue()
    threading.Thread(target=lambda: lines.put(proc.stdout.readline()), daemon=True).start()
    try:
        return proc, json.loads(lines.get(timeout=20))["listening"]
    except (queue.Empty, ValueError):
        proc.kill()
        raise AssertionError("synthetic fixture server did not start")


@pytest.fixture(scope="module")
def origin():
    if NODE is None:
        pytest.skip("node is not on PATH")
    proc, url = _start_fixture()
    try:
        yield url
    finally:
        proc.terminate()
        proc.wait(timeout=10)


def _get(url):
    with urllib.request.urlopen(url, timeout=10) as response:
        return response.read().decode("utf-8")


def _markers(html):
    static = set(re.findall(r'data-synthetic="([a-z-]+)"', html))
    created_by_script = set(re.findall(r"'([a-z]+-turn)'", html))
    return static | created_by_script


def _wanted(group):
    return {m for candidate in group.candidates for m in re.findall(r"data-synthetic='([a-z-]+)'", candidate)}


def test_selectors_match_the_markup_the_fixture_actually_serves(monkeypatch, origin):
    selectors = _selectors(monkeypatch, origin)
    signed_in, signed_out = _markers(_get(origin)), _markers(_get(origin + "?signed_out=1"))
    for group in (selectors.prompt_input, selectors.submit_button, selectors.response_container,
                  selectors.authenticated_signal, selectors.streaming_indicator, selectors.file_input, selectors.new_chat):
        assert _wanted(group) and _wanted(group) <= signed_in, group.name
    assert _wanted(selectors.login_signal) <= signed_out and not _wanted(selectors.login_signal) & signed_in
    assert not _wanted(selectors.authenticated_signal) & signed_out
    assert not _wanted(selectors.prompt_input) & signed_out
    # A fresh page must hold no assistant turn, or warm reuse could never prove it empty.
    assert 'data-synthetic="assistant-turn"' not in _get(origin)


def test_served_pages_carry_no_login_credential_or_captcha_mechanics(origin):
    for html in (_get(origin), _get(origin + "?signed_out=1")):
        assert not re.search(r"<form|type=[\"']?password|captcha|turnstile", html, re.I)
        assert not re.search(r"https?://", html)


# --------------------------------------------------------------------------- browser (Playwright + Chromium)


def _browser_unavailable():
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        return "playwright is not installed"
    try:
        with sync_playwright() as pw:
            pw.chromium.launch(headless=True).close()
    except Exception as exc:  # noqa: BLE001 - any launch failure means "no browser here"
        return "chromium is not launchable (%s)" % type(exc).__name__
    return ""


class _Lab:
    """A WarmWorkerDaemon on the real PlaywrightPageDriver, pointed at the running fixture."""

    def __init__(self, monkeypatch, base):
        self.base = base
        self.drivers = []
        self._monkeypatch = monkeypatch
        self._daemons = []

    def _factory(self, options):
        self.drivers.append(create_default_driver(options))
        return self.drivers[-1]

    def daemon(self, url):
        self._monkeypatch.setenv("UBAG_SYNTHETIC_PROVIDER", "1")
        self._monkeypatch.setenv("UBAG_SYNTHETIC_PROVIDER_URL", url)
        registry = {}
        assert register_synthetic_provider(registry)
        self._daemons.append(WarmWorkerDaemon(driver_factory=self._factory, selectors_by_target=registry))
        return self._daemons[-1]

    def control(self, **body):
        request = urllib.request.Request(self.base + "__control", data=json.dumps(body).encode(), method="POST")
        urllib.request.urlopen(request, timeout=10).read()

    def stats(self):
        return json.loads(_get(self.base + "__stats"))

    def close(self):
        for daemon in self._daemons:
            daemon.close()
        self.control(signed_out=False, attach_ms=0)


@pytest.fixture
def lab(monkeypatch, tmp_path, origin):
    reason = _browser_unavailable()
    if reason:
        pytest.skip(reason)
    for name in ("UBAG_ADAPTER_OFFLINE", "UBAG_WORKER_OFFLINE", "UBAG_REMOTE_BROWSER_ENDPOINT", "UBAG_BROWSER_ENGINE"):
        monkeypatch.delenv(name, raising=False)
    monkeypatch.setenv("UBAG_PROFILE_DIR", str(tmp_path / "profiles"))
    fixture = _Lab(monkeypatch, origin)
    fixture.control(signed_out=False, attach_ms=0)
    yield fixture
    fixture.close()


def test_browser_two_jobs_share_one_warm_page(lab):
    daemon = lab.daemon(lab.base)
    for index, prompt in enumerate(("first", "second")):
        events = list(daemon.run_job(_job(prompt, "job_e2e_%d" % index, headless=True)))
        assert _types(events)[-1] == "completed", _types(events)
        assert events[-1]["data"]["result"]["text"] == "Synthetic answer: %s" % prompt
    assert len(lab.drivers) == 1, "the second job must reuse the warm browser page"


def test_browser_signed_out_page_fails_closed(lab, monkeypatch):
    monkeypatch.setenv("UBAG_LOGIN_READY_GRACE_S", "0.3")
    monkeypatch.setenv("UBAG_LOGIN_READY_EXTENDED_S", "0.3")
    daemon = lab.daemon(lab.base + "?signed_out=1")
    events = list(daemon.run_job(_job("hi", "job_e2e_out", headless=True, manual_login_timeout_s=1)))
    assert _types(events)[-2:] == ["session.manual_action_required", "blocked"], _types(events)
    assert events[-1]["data"]["reason"] == "manual_login_required"
    assert lab.stats()["chat_requests"] == 0


def test_browser_truncated_stream_ends_timed_out_with_partial_text(lab, monkeypatch):
    monkeypatch.setenv("UBAG_WORKER_STRICT_STREAM_END", "1")
    daemon = lab.daemon(lab.base)
    prompt = "[[synthetic scenario=truncated truncate_after=80 stall_ms=30000]] cut"
    events = list(daemon.run_job(_job(prompt, "job_e2e_cut", headless=True, response_timeout_s=4)))
    assert _types(events)[-1] == "timed_out", _types(events)
    assert "completed" not in _types(events)
    assert len(events[-1]["data"]["partial"]["text"]) == 80


def test_browser_send_waits_for_the_slow_attachment_accept(lab):
    lab.control(attach_ms=800)
    daemon = lab.daemon(lab.base)
    folder = tempfile.mkdtemp(prefix="ubag-attach-")
    path = os.path.join(folder, "note.txt")
    with open(path, "w", encoding="utf-8") as handle:
        handle.write("synthetic attachment")
    payload = _job("summarise", "job_e2e_att", headless=True)
    payload["job"]["input"]["attachments"] = [
        {"key": "note", "filename": "note.txt", "content_type": "text/plain", "kind": "document"}
    ]
    payload["job"]["input"]["attachment_local_paths"] = [path]
    try:
        events = list(daemon.run_job(payload))
    finally:
        shutil.rmtree(folder, ignore_errors=True)
    assert "file.attached" in _types(events) and _types(events)[-1] == "completed", _types(events)
    assert events[-1]["data"]["result"]["text"] == "Synthetic answer: summarise [attachments: 1]"
    assert lab.stats()["uploads"] >= 1

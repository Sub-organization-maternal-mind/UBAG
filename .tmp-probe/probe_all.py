"""Probe every provider page for login state + selector-group drift.

Reads the live selector config from the installed ubag_worker package, so the
report reflects exactly what the running worker uses.
"""
import json
import sys
import traceback

sys.path.insert(0, "/app/apps/worker")

from playwright.sync_api import sync_playwright

CDP = "http://172.28.0.10:9223"

from ubag_worker.live import selectors as S

PROVIDERS = [
    ("chatgpt_web", S.CHATGPT_WEB),
    ("claude_web", S.CLAUDE_WEB),
    ("gemini_web", S.GEMINI_WEB),
    ("deepseek_web", S.DEEPSEEK_WEB),
    ("mistral_lechat", S.MISTRAL_LECHAT),
    ("perplexity_web", S.PERPLEXITY_WEB),
    ("duckai_web", S.DUCKAI_WEB),
]


def group_names(p):
    out = [
        ("prompt_input", p.prompt_input),
        ("submit_button", p.submit_button),
        ("response_container", p.response_container),
        ("authenticated_signal", p.authenticated_signal),
        ("login_signal", p.login_signal),
        ("streaming_indicator", p.streaming_indicator),
    ]
    if p.file_input is not None:
        out.append(("file_input", p.file_input))
    if p.new_chat is not None:
        out.append(("new_chat", p.new_chat))
    return out


lines = []


def log(msg=""):
    lines.append(str(msg))
    print(msg, flush=True)


with sync_playwright() as p:
    browser = p.chromium.connect_over_cdp(CDP)
    ctx = browser.contexts[0] if browser.contexts else browser.new_context()

    for pid, prov in PROVIDERS:
        log("")
        log("=" * 78)
        log("PROVIDER %s  version=%s  url=%s" % (pid, prov.selector_version, prov.target_url))
        log("=" * 78)
        page = None
        try:
            page = ctx.new_page()
            page.set_default_timeout(15000)
            try:
                page.goto(prov.target_url, wait_until="domcontentloaded", timeout=45000)
            except Exception as exc:
                log("  goto warn: %s" % str(exc)[:120])
            page.wait_for_timeout(7000)
            log("  final URL: %s" % page.url)
            log("  title: %s" % page.title()[:90])

            for name, grp in group_names(prov):
                log("  --- %s (baseline=%s) ---" % (name, grp.baseline_version))
                for sel in grp.candidates:
                    try:
                        n = page.locator(sel).count()
                    except Exception as exc:
                        n = "ERR(%s)" % str(exc)[:60]
                    log("      %-72s -> %s" % (sel[:72], n))

            if prov.settings:
                log("  --- settings ---")
                for st in prov.settings:
                    log("      key=%s desired=%r required=%s" % (st.key, st.desired, getattr(st, "required", None)))
                    for step_i, step in enumerate(st.open_steps):
                        log("        open_step[%d]:" % step_i)
                        for sel in step:
                            try:
                                n = page.locator(sel).count()
                            except Exception as exc:
                                n = "ERR(%s)" % str(exc)[:60]
                            log("           %-68s -> %s" % (sel[:68], n))
                    try:
                        n = page.locator(st.satisfied_when.replace("{value}", st.desired)).count()
                    except Exception as exc:
                        n = "ERR(%s)" % str(exc)[:60]
                    log("        satisfied_when(desired) -> %s" % n)
        except Exception:
            log("  PROBE ERROR:\n" + traceback.format_exc())
        finally:
            if page is not None:
                try:
                    page.close()
                except Exception:
                    pass

    browser.close()

with open("/tmp/probe_all.out", "w") as fh:
    fh.write("\n".join(lines))
print("\nWROTE /tmp/probe_all.out", flush=True)

"""Probe chatgpt.com live DOM via CDP to diagnose selector drift."""
import json
import sys
import time

CDP = "http://172.28.0.10:9223"

from playwright.sync_api import sync_playwright

SELECTOR_PROBES = {
    "prompt_input": [
        "#prompt-textarea",
        "textarea[data-id='root']",
        "div[contenteditable='true'][data-virtualkeyboard='true']",
        "textarea[placeholder*='Message']",
    ],
    "submit_button": [
        "button[data-testid='send-button']",
        "button[aria-label*='Send']",
        "button[type='submit']",
    ],
    "response_container": [
        "div[data-message-author-role='assistant']",
        "div.markdown.prose",
        "[data-testid^='conversation-turn'] .markdown",
    ],
    "authenticated_signal": [
        "#prompt-textarea",
        "nav[aria-label='Chat history']",
        "button[data-testid='profile-button']",
    ],
    "login_signal": [
        "button[data-testid='login-button']",
        "a[href*='auth/login']",
        "text=Log in",
    ],
    "model_open_1": [
        "button.__composer-pill[aria-haspopup='menu']",
        "button[class*='composer-pill'][aria-haspopup='menu']",
    ],
    "model_open_2": [
        "[role='menuitem'][aria-label='Show advanced options']",
        "[role='menuitem'][aria-expanded='false']:has-text(\"Advanced\")",
    ],
    "model_open_3": [
        "[role='menuitem'][aria-haspopup='menu']:has-text(\"Model\")",
    ],
}

with sync_playwright() as p:
    browser = p.chromium.connect_over_cdp(CDP)
    ctx = browser.contexts[0] if browser.contexts else browser.new_context()
    page = ctx.new_page()
    page.set_default_timeout(20000)
    print("navigating to https://chatgpt.com/ ...", flush=True)
    try:
        page.goto("https://chatgpt.com/", wait_until="domcontentloaded", timeout=45000)
    except Exception as exc:
        print("goto error:", exc, flush=True)
    page.wait_for_timeout(6000)
    print("URL:", page.url, flush=True)
    print("TITLE:", page.title(), flush=True)

    print("\n=== SELECTOR PROBE (count) ===", flush=True)
    for group, sels in SELECTOR_PROBES.items():
        row = []
        for s in sels:
            try:
                n = page.locator(s).count()
            except Exception as exc:
                n = "ERR:%s" % str(exc)[:60]
            row.append("%s -> %s" % (s, n))
        print("[%s]" % group, flush=True)
        for r in row:
            print("   ", r, flush=True)

    # Dump composer-area markup for manual re-baselining.
    print("\n=== FORM BUTTONS ===", flush=True)
    try:
        info = page.evaluate(
            """() => {
                const out = [];
                document.querySelectorAll('form button, main button').forEach(b => {
                    out.push({
                        cls: (b.className || '').toString().slice(0, 160),
                        testid: b.getAttribute('data-testid'),
                        aria: b.getAttribute('aria-label'),
                        haspopup: b.getAttribute('aria-haspopup'),
                        expanded: b.getAttribute('aria-expanded'),
                        text: (b.innerText || '').trim().slice(0, 60),
                    });
                });
                return out.slice(0, 40);
            }"""
        )
        print(json.dumps(info, indent=1)[:6000], flush=True)
    except Exception as exc:
        print("form button dump failed:", exc, flush=True)

    print("\n=== COMPOSER PILL SEARCH ===", flush=True)
    try:
        pill = page.evaluate(
            """() => {
                const out = [];
                document.querySelectorAll('button').forEach(b => {
                    const cls = (b.className || '').toString();
                    if (/pill|composer/i.test(cls) || b.getAttribute('aria-haspopup') === 'menu') {
                        out.push({cls: cls.slice(0,200), testid: b.getAttribute('data-testid'),
                                  aria: b.getAttribute('aria-label'), popup: b.getAttribute('aria-haspopup'),
                                  text: (b.innerText||'').trim().slice(0,80)});
                    }
                });
                return out.slice(0, 30);
            }"""
        )
        print(json.dumps(pill, indent=1)[:6000], flush=True)
    except Exception as exc:
        print("pill dump failed:", exc, flush=True)

    page.close()
    browser.close()
print("\nDONE", flush=True)

"""Gemini: precise mode-menu selection state + send-button-after-fill proof."""
import json
import sys

sys.path.insert(0, "/app/apps/worker")

from playwright.sync_api import sync_playwright

CDP = "http://172.28.0.10:9223"
lines = []


def log(m=""):
    lines.append(str(m))
    print(m, flush=True)


with sync_playwright() as p:
    b = p.chromium.connect_over_cdp(CDP)
    ctx = b.contexts[0] if b.contexts else b.new_context()
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto("https://gemini.google.com/app", wait_until="domcontentloaded", timeout=45000)
    page.wait_for_timeout(8000)

    log("=== legacy gem-menu-item check ===")
    for sel in ["gem-menu-item", "gem-menu-item.selected", "gem-menu", "bard-mode-switcher"]:
        try:
            log("  %-26s -> %d" % (sel, page.locator(sel).count()))
        except Exception as exc:
            log("  %-26s -> ERR %.50s" % (sel, str(exc)))

    log("")
    log("=== open the mode picker ===")
    page.locator("button[data-test-id='bard-mode-menu-button']").first.click(timeout=10000)
    page.wait_for_timeout(2000)

    rows = page.evaluate(
        """() => Array.from(document.querySelectorAll("[role='menuitem']")).map(m => {
            const cs = getComputedStyle(m);
            const r = m.getBoundingClientRect();
            return {
                testid: m.getAttribute('data-test-id'),
                aria: m.getAttribute('aria-label'),
                checked: m.getAttribute('aria-checked'),
                state: m.getAttribute('data-state'),
                selected: m.getAttribute('aria-selected'),
                cls: (m.className||'').toString().slice(0,120),
                box: [Math.round(r.width), Math.round(r.height)],
                text: (m.innerText||'').trim().replace(/\\s+/g,' ').slice(0,70),
            };
        })"""
    )
    log("  ROLE=MENUITEM rows: %d" % len(rows))
    for r in rows:
        log("    " + json.dumps(r))

    log("")
    log("=== identify the currently-selected entry ===")
    sel_probe = page.evaluate(
        """() => {
            const out = {selected_class: [], data_state_checked: [], aria_checked: []};
            document.querySelectorAll("[role='menuitem']").forEach(m => {
                const t = (m.innerText||'').trim().replace(/\\s+/g,' ').slice(0,40);
                if ((m.className||'').toString().includes('selected')) out.selected_class.push(t);
                if (m.getAttribute('data-state') === 'checked') out.data_state_checked.push(t);
                if (m.getAttribute('aria-checked') === 'true') out.aria_checked.push(t);
            });
            return out;
        }"""
    )
    log("  " + json.dumps(sel_probe))

    log("")
    log("=== test a text matcher against each label ===")
    for label in ["3.8 Flash", "3.5 Flash-Lite", "3.1 Pro", "Extended thinking"]:
        sel = "[role='menuitem']:has-text(\"%s\")" % label
        try:
            n = page.locator(sel).count()
        except Exception as exc:
            n = "ERR %.40s" % str(exc)
        log("  %-34s -> %s" % (sel, n))

    log("")
    log("=== selected detection via [data-state=checked] ===")
    for label in ["3.8 Flash", "3.5 Flash-Lite"]:
        sel = "[role='menuitem'][data-state='checked']:has-text(\"%s\")" % label
        try:
            n = page.locator(sel).count()
        except Exception as exc:
            n = "ERR %.40s" % str(exc)
        log("  %-56s -> %s" % (sel[:56], n))

    page.keyboard.press("Escape")
    page.wait_for_timeout(800)
    log("  picker text after escape: %r" % page.locator("button[data-test-id='bard-mode-menu-button']").first.inner_text().strip()[:40])

    log("")
    log("=== send button: empty vs filled (the real drift cause) ===")
    for sel in ["button[aria-label*='Send message']", "button[aria-label*='Send']"]:
        log("  EMPTY  %-42s -> %d" % (sel, page.locator(sel).count()))
    box = page.locator("div.ql-editor[contenteditable='true']").first
    box.click(timeout=10000)
    box.fill("Reply with the single word OK.")
    page.wait_for_timeout(1200)
    for sel in ["button[aria-label*='Send message']", "button[aria-label*='Send']"]:
        log("  FILLED %-42s -> %d" % (sel, page.locator(sel).count()))
    # clear it back out
    page.keyboard.press("Control+A")
    page.keyboard.press("Delete")
    page.wait_for_timeout(800)
    for sel in ["button[aria-label*='Send message']"]:
        log("  CLEARED %-41s -> %d" % (sel, page.locator(sel).count()))

    page.close()
    b.close()

open("/tmp/probe_gemini2.out", "w").write("\n".join(lines))
print("\nWROTE")

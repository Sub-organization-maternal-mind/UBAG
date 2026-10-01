"""Deep probe of gemini.google.com: submit button, model picker, response container."""
import json
import sys

sys.path.insert(0, "/app/apps/worker")

from playwright.sync_api import sync_playwright

CDP = "http://172.28.0.10:9223"
DO_SEND = "--send" in sys.argv
lines = []


def log(m=""):
    lines.append(str(m))
    print(m, flush=True)


def dump_buttons(page, label):
    rows = page.evaluate(
        """() => {
            const out = [];
            document.querySelectorAll('button, [role="button"]').forEach(b => {
                const cls = (b.className || '').toString();
                out.push({
                    tag: b.tagName.toLowerCase(),
                    cls: cls.slice(0, 110),
                    aria: b.getAttribute('aria-label'),
                    testid: b.getAttribute('data-test-id') || b.getAttribute('data-testid'),
                    tooltip: b.getAttribute('mattooltip'),
                    disabled: b.disabled || b.getAttribute('aria-disabled'),
                    text: (b.innerText||'').trim().slice(0, 35),
                });
            });
            return out;
        }"""
    )
    log("  BUTTONS [%s]: %d" % (label, len(rows)))
    for r in rows:
        log("    " + json.dumps(r))


with sync_playwright() as p:
    b = p.chromium.connect_over_cdp(CDP)
    ctx = b.contexts[0] if b.contexts else b.new_context()
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto("https://gemini.google.com/app", wait_until="domcontentloaded", timeout=45000)
    page.wait_for_timeout(8000)
    log("URL=%s TITLE=%s" % (page.url, page.title()))

    log("")
    log("=== submit-button candidates (EMPTY composer) ===")
    for sel in [
        "button[aria-label*='Send message']",
        "button.send-button",
        "button[mattooltip*='Send']",
        "button[aria-label='Send message']",
        "button[aria-label*='Send']",
        "button[aria-label*='Submit']",
        "div[class*='send-button'] button",
        "button[class*='send']",
        "rich-textarea ~ div button",
        "button mat-icon[fonticon='send']",
    ]:
        try:
            n = page.locator(sel).count()
        except Exception as exc:
            n = "ERR(%s)" % str(exc)[:45]
        log("  %-52s -> %s" % (sel[:52], n))

    dump_buttons(page, "all")

    log("")
    log("=== mode picker ===")
    for sel in [
        "button[data-test-id='bard-mode-menu-button']",
        "button[aria-label*='mode picker']",
        "button.input-area-switch",
        "bard-mode-switcher",
    ]:
        try:
            n = page.locator(sel).count()
        except Exception as exc:
            n = "ERR(%s)" % str(exc)[:45]
        log("  %-52s -> %s" % (sel[:52], n))

    pk = page.locator("button[data-test-id='bard-mode-menu-button']")
    if pk.count():
        log("  picker text: %r" % pk.first.inner_text().strip()[:70])
        try:
            pk.first.click(timeout=8000)
            page.wait_for_timeout(1800)
            rows = page.evaluate(
                """() => {
                    const out = [];
                    document.querySelectorAll("[role='menuitemradio'],[role='menuitem'],[role='option'],[role='radio'],mat-radio-button,[data-test-id*='mode']").forEach(m => {
                        out.push({
                            role: m.getAttribute('role'),
                            checked: m.getAttribute('aria-checked'),
                            testid: m.getAttribute('data-test-id'),
                            text: (m.innerText||'').trim().replace(/\\s+/g,' ').slice(0,70),
                        });
                    });
                    return out;
                }"""
            )
            log("  MODE MENU (%d):" % len(rows))
            for r in rows:
                log("    " + json.dumps(r))
        except Exception as exc:
            log("  picker click failed: %.130s" % str(exc))
        page.keyboard.press("Escape")
        page.wait_for_timeout(600)

    if DO_SEND:
        log("")
        log("=== fill + send ===")
        box = page.locator("div.ql-editor[contenteditable='true']").first
        box.click(timeout=10000)
        box.fill("Reply with the single word OK.")
        page.wait_for_timeout(1500)
        log("  -- buttons after fill --")
        for sel in [
            "button[aria-label*='Send message']",
            "button[aria-label*='Send']",
            "button.send-button",
            "button[mattooltip*='Send']",
            "button[class*='send']",
        ]:
            try:
                n = page.locator(sel).count()
            except Exception as exc:
                n = "ERR(%s)" % str(exc)[:45]
            log("  %-50s -> %s" % (sel[:50], n))
        dump_buttons(page, "after fill")
        for sel in [
            "button[aria-label*='Send message']",
            "button.send-button",
            "button[mattooltip*='Send']",
            "button[aria-label*='Send']",
        ]:
            loc = page.locator(sel)
            try:
                if loc.count() == 0:
                    continue
                loc.first.click(timeout=8000)
                log("  SENT via %s" % sel)
                break
            except Exception as exc:
                log("  send via %s failed: %.100s" % (sel, str(exc)))
        page.wait_for_timeout(3000)
        log("  -- stop/streaming candidates --")
        for sel in ["button[aria-label*='Stop']", "div.blinking-cursor", "progress-bar"]:
            try:
                n = page.locator(sel).count()
            except Exception as exc:
                n = "ERR(%s)" % str(exc)[:45]
            log("  %-50s -> %s" % (sel[:50], n))
        log("  -- response container candidates --")
        for sel in [
            "message-content",
            "div.markdown",
            ".markdown",
            "model-response",
            "message-content.model-response-text",
            "div.model-response-text",
            "div[data-response-index]",
            "model-response message-content",
        ]:
            try:
                n = page.locator(sel).count()
            except Exception as exc:
                n = "ERR(%s)" % str(exc)[:45]
            log("  %-50s -> %s" % (sel[:50], n))
        log("  waiting 40s for settle...")
        page.wait_for_timeout(40000)
        try:
            txt = page.locator("message-content").last.inner_text()
            log("  answer (first 200): %s" % txt.strip()[:200].replace("\n", " | "))
        except Exception as exc:
            log("  read failed: %.130s" % str(exc))
        log("  FINAL URL: %s" % page.url)

    page.close()
    b.close()

open("/tmp/probe_gemini.out", "w").write("\n".join(lines))
print("\nWROTE")

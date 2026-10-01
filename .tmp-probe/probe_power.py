"""Probe the new chatgpt.com 'Power' control that replaced the Effort submenu."""
import json
import sys

sys.path.insert(0, "/app/apps/worker")

from playwright.sync_api import sync_playwright

CDP = "http://172.28.0.10:9223"
lines = []


def log(m=""):
    lines.append(str(m))
    print(m, flush=True)


def menus(page, label):
    rows = page.evaluate(
        """() => Array.from(document.querySelectorAll("[role='menu'],[role='menuitem'],[role='menuitemradio'],[role='slider'],[role='dialog'],[role='group']")).map(m => ({
            role: m.getAttribute('role'),
            aria: m.getAttribute('aria-label'),
            checked: m.getAttribute('aria-checked'),
            expanded: m.getAttribute('aria-expanded'),
            value: m.getAttribute('aria-valuenow'),
            min: m.getAttribute('aria-valuemin'),
            max: m.getAttribute('aria-valuemax'),
            valuetext: m.getAttribute('aria-valuetext'),
            testid: m.getAttribute('data-testid'),
            text: (m.innerText||'').trim().replace(/\\s+/g,' ').slice(0,80),
        }))"""
    )
    log("  MENUS [%s]: %d" % (label, len(rows)))
    for r in rows:
        log("    " + json.dumps(r))


with sync_playwright() as p:
    b = p.chromium.connect_over_cdp(CDP)
    ctx = b.contexts[0] if b.contexts else b.new_context()
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto("https://chatgpt.com/", wait_until="domcontentloaded", timeout=45000)
    page.wait_for_timeout(7000)

    log("=== click composer pill ===")
    page.locator("button.__composer-pill[aria-haspopup='menu']").first.click(timeout=10000)
    page.wait_for_timeout(1500)
    menus(page, "compact")

    log("")
    log("=== attempt: click Power menuitem ===")
    for sel in [
        "[role='menuitem'][aria-label='Power']",
        "[role='menuitem']:has-text('Power')",
    ]:
        n = page.locator(sel).count()
        log("  %-50s -> %d" % (sel[:50], n))
        if n:
            try:
                page.locator(sel).first.click(timeout=6000)
                page.wait_for_timeout(1800)
                log("  CLICKED %s" % sel)
                break
            except Exception as exc:
                log("  click failed: %.150s" % str(exc))

    menus(page, "after Power click")

    log("")
    log("=== attempt: click 'Select model' opener then dump ===")
    page.keyboard.press("Escape")
    page.wait_for_timeout(700)
    page.locator("button.__composer-pill[aria-haspopup='menu']").first.click(timeout=10000)
    page.wait_for_timeout(1200)
    opener = page.locator("[role='menuitem'][aria-label='Select model']")
    if opener.count():
        opener.first.click(timeout=6000)
        page.wait_for_timeout(1800)
        menus(page, "after Select model click")
        log("")
        log("  hover attempt on 'Select model':")
        try:
            opener.first.hover(timeout=5000)
            page.wait_for_timeout(1500)
            menus(page, "after hover Select model")
        except Exception as exc:
            log("  hover failed: %.120s" % str(exc))

    log("")
    log("=== full ancestor markup of the pill menu ===")
    try:
        html = page.evaluate(
            """() => {
                const m = document.querySelector("[role='menu']");
                return m ? m.outerHTML.slice(0, 6000) : 'no menu';
            }"""
        )
        log(html)
    except Exception as exc:
        log("markup dump failed: %.120s" % str(exc))

    # Leave the page as we found it.
    page.keyboard.press("Escape")
    b.close()

open("/tmp/probe_power.out", "w").write("\n".join(lines))
print("\nWROTE")

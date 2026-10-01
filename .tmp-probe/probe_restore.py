"""Read + restore the chatgpt.com model/power state, and probe power adjustment."""
import json
import sys

sys.path.insert(0, "/app/apps/worker")

from playwright.sync_api import sync_playwright

CDP = "http://172.28.0.10:9223"
TARGET_MODEL = sys.argv[1] if len(sys.argv) > 1 else "Latest"

lines = []


def log(m=""):
    lines.append(str(m))
    print(m, flush=True)


def dump(page, label):
    rows = page.evaluate(
        """() => Array.from(document.querySelectorAll("[role='menuitemradio'],[role='menuitem'],[role='slider']")).map(m => ({
            role: m.getAttribute('role'),
            aria: m.getAttribute('aria-label'),
            checked: m.getAttribute('aria-checked'),
            now: m.getAttribute('aria-valuenow'),
            disabled: m.disabled || m.getAttribute('aria-disabled'),
            text: (m.innerText||'').trim().replace(/\\s+/g,' ').slice(0,45),
        }))"""
    )
    log("  [%s]" % label)
    for r in rows:
        log("    " + json.dumps(r))


PILL = "button.__composer-pill[aria-haspopup='menu']"

with sync_playwright() as p:
    b = p.chromium.connect_over_cdp(CDP)
    ctx = b.contexts[0] if b.contexts else b.new_context()
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto("https://chatgpt.com/", wait_until="domcontentloaded", timeout=45000)
    page.wait_for_timeout(7000)

    log("=== current state ===")
    page.locator(PILL).first.click(timeout=10000)
    page.wait_for_timeout(1200)
    dump(page, "open picker")
    log("  pill text: %r" % page.locator(PILL).first.inner_text().strip()[:60])

    # --- power slider keyboard behaviour ---
    log("")
    log("=== power slider: click then arrow keys ===")
    power = page.locator("[role='menuitem'][aria-label='Power']")
    log("  Power menuitem: count=%d aria-disabled=%r" % (
        power.count(), power.first.get_attribute("aria-disabled") if power.count() else None))
    slider = page.locator("[role='slider']")
    before = slider.first.get_attribute("aria-valuenow") if slider.count() else None
    log("  slider before: %s" % before)
    if power.count():
        try:
            power.first.click(timeout=6000)
            page.wait_for_timeout(1200)
            dump(page, "after clicking Power")
            log("  pill text: %r" % page.locator(PILL).first.inner_text().strip()[:60])
            for _ in range(2):
                page.keyboard.press("ArrowLeft")
                page.wait_for_timeout(700)
            after = page.locator("[role='slider']").first.get_attribute("aria-valuenow") if page.locator("[role='slider']").count() else None
            log("  slider after 2x ArrowLeft: %s" % after)
            log("  pill text: %r" % page.locator(PILL).first.inner_text().strip()[:60])
            # put it back
            for _ in range(2):
                page.keyboard.press("ArrowRight")
                page.wait_for_timeout(700)
            restored = page.locator("[role='slider']").first.get_attribute("aria-valuenow") if page.locator("[role='slider']").count() else None
            log("  slider after 2x ArrowRight: %s" % restored)
        except Exception as exc:
            log("  power interaction failed: %.150s" % str(exc))

    # --- set the target model ---
    log("")
    log("=== set model to %r ===" % TARGET_MODEL)
    page.keyboard.press("Escape")
    page.wait_for_timeout(900)
    page.locator(PILL).first.click(timeout=10000)
    page.wait_for_timeout(1200)
    sel = "[role='menuitemradio']:has-text(\"%s\")" % TARGET_MODEL
    loc = page.locator(sel)
    log("  target option count=%d" % loc.count())
    if loc.count():
        try:
            loc.first.scroll_into_view_if_needed(timeout=5000)
            loc.first.click(timeout=8000)
            page.wait_for_timeout(2000)
            log("  clicked %r" % TARGET_MODEL)
        except Exception as exc:
            log("  click failed: %.150s" % str(exc))

    # re-open and confirm
    page.keyboard.press("Escape")
    page.wait_for_timeout(900)
    page.locator(PILL).first.click(timeout=10000)
    page.wait_for_timeout(1200)
    dump(page, "FINAL state")
    log("  FINAL pill text: %r" % page.locator(PILL).first.inner_text().strip()[:60])

    page.keyboard.press("Escape")
    page.close()
    b.close()

open("/tmp/probe_restore.out", "w").write("\n".join(lines))
print("\nWROTE")

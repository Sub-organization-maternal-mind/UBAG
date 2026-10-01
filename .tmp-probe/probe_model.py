"""Verify the chatgpt.com model-switch flow the engine will use."""
import json
import sys

sys.path.insert(0, "/app/apps/worker")

from playwright.sync_api import sync_playwright

CDP = "http://172.28.0.10:9223"
lines = []


def log(m=""):
    lines.append(str(m))
    print(m, flush=True)


def state(page, label):
    rows = page.evaluate(
        """() => Array.from(document.querySelectorAll("[role='menuitemradio'],[role='menuitem']")).map(m => ({
            role: m.getAttribute('role'),
            aria: m.getAttribute('aria-label'),
            checked: m.getAttribute('aria-checked'),
            expanded: m.getAttribute('aria-expanded'),
            text: (m.innerText||'').trim().replace(/\\s+/g,' ').slice(0,50),
        }))"""
    )
    log("  STATE [%s]:" % label)
    for r in rows:
        log("    " + json.dumps(r))


PILL = "button.__composer-pill[aria-haspopup='menu']"
OPEN1 = "[role='menuitem'][aria-label='Select model']"
DESIRED = "GPT-5.6 Sol"

with sync_playwright() as p:
    b = p.chromium.connect_over_cdp(CDP)
    ctx = b.contexts[0] if b.contexts else b.new_context()
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto("https://chatgpt.com/", wait_until="domcontentloaded", timeout=45000)
    page.wait_for_timeout(7000)
    log("URL=%s" % page.url)

    # --- exactly what _ensure_setting does ---
    log("")
    log("=== pass 1: open pill, check satisfied_when ===")
    page.locator(PILL).first.click(timeout=10000)
    page.wait_for_timeout(900)
    state(page, "after pill click")

    sat = "[role='menuitemradio'][aria-checked='true']:has-text(\"%s\")" % DESIRED
    log("  satisfied_when(%r) -> %d" % (DESIRED, page.locator(sat).count()))

    log("")
    log("=== pass 1b: click model opener ===")
    n = page.locator(OPEN1).count()
    log("  opener count=%d" % n)
    if n:
        page.locator(OPEN1).first.click(timeout=6000)
        page.wait_for_timeout(1200)
        state(page, "after opener click")
        log("  satisfied_when after opener -> %d" % page.locator(sat).count())

    log("")
    log("=== pass 1c: click desired option ===")
    apply_sel = "[role='menuitemradio']:has-text(\"%s\")" % DESIRED
    cnt = page.locator(apply_sel).count()
    log("  apply_click(%r) -> %d" % (DESIRED, cnt))
    if cnt:
        page.locator(apply_sel).first.click(timeout=6000)
        page.wait_for_timeout(2000)
        log("  CLICKED %s" % DESIRED)
    log("  pill text now: %r" % page.locator(PILL).first.inner_text().strip()[:60])

    log("")
    log("=== pass 2: reopen and re-verify (read->act->re-verify) ===")
    page.keyboard.press("Escape")
    page.wait_for_timeout(800)
    page.locator(PILL).first.click(timeout=10000)
    page.wait_for_timeout(1000)
    sat_n = page.locator(sat).count()
    log("  satisfied_when(%r) -> %d  <-- must be >0 for 'already_set'" % (DESIRED, sat_n))
    state(page, "reopened")

    # --- power slider: can we set it with keyboard? ---
    log("")
    log("=== power slider keyboard test ===")
    slider = page.locator("[role='slider']")
    log("  slider count=%d valuenow=%s" % (slider.count(), slider.first.get_attribute("aria-valuenow") if slider.count() else "n/a"))
    power = page.locator("[role='menuitem'][aria-label='Power']")
    log("  Power menuitem count=%d aria-disabled=%s" % (
        power.count(), power.first.get_attribute("aria-disabled") if power.count() else "n/a"))

    # Restore the account default we found (Latest / High) so we leave no surprise.
    restore = "[role='menuitemradio']:has-text(\"Latest\")"
    if page.locator(restore).count():
        try:
            page.locator(restore).first.click(timeout=6000)
            page.wait_for_timeout(1500)
            log("  restored model to 'Latest'")
        except Exception as exc:
            log("  restore failed: %.100s" % str(exc))
    log("  final pill text: %r" % page.locator(PILL).first.inner_text().strip()[:60])

    page.keyboard.press("Escape")
    page.close()
    b.close()

open("/tmp/probe_model.out", "w").write("\n".join(lines))
print("\nWROTE")

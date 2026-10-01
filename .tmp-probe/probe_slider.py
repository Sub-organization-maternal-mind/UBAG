"""Is the chatgpt.com Power slider settable by any interaction?"""
import json
import sys

sys.path.insert(0, "/app/apps/worker")

from playwright.sync_api import sync_playwright

CDP = "http://172.28.0.10:9223"
PILL = "button.__composer-pill[aria-haspopup='menu']"
lines = []


def log(m=""):
    lines.append(str(m))
    print(m, flush=True)


def slider_state(page, label):
    d = page.evaluate(
        """() => {
            const s = document.querySelector("[role='slider']");
            const cont = document.querySelector("[data-model-reasoning-effort-slider]");
            const pw = document.querySelector("[role='menuitem'][aria-label='Power']");
            return {
                now: s ? s.getAttribute('aria-valuenow') : null,
                min: s ? s.getAttribute('aria-valuemin') : null,
                max: s ? s.getAttribute('aria-valuemax') : null,
                s_hidden: s ? s.getAttribute('aria-hidden') : null,
                s_tabindex: s ? s.getAttribute('tabindex') : null,
                container_locked: cont && cont.querySelector("[data-locked]") ? cont.querySelector("[data-locked]").getAttribute("data-locked") : null,
                container_disabled: cont && cont.querySelector("[aria-disabled]") ? cont.querySelector("[aria-disabled]").getAttribute("aria-disabled") : null,
                power_disabled: pw ? (pw.getAttribute('aria-disabled') || pw.getAttribute('data-disabled')) : null,
                announcements: Array.from(document.querySelectorAll(".d1BZWq_KeyboardAnnouncement")).map(e => e.textContent.trim()),
            };
        }"""
    )
    log("  [%s] %s" % (label, json.dumps(d)))
    return d


with sync_playwright() as p:
    b = p.chromium.connect_over_cdp(CDP)
    ctx = b.contexts[0] if b.contexts else b.new_context()
    page = ctx.new_page()
    page.set_default_timeout(15000)
    page.goto("https://chatgpt.com/", wait_until="domcontentloaded", timeout=45000)
    page.wait_for_timeout(7000)

    page.locator(PILL).first.click(timeout=10000)
    page.wait_for_timeout(1200)
    before = slider_state(page, "picker open")

    log("")
    log("--- click the Power menuitem ---")
    pw = page.locator("[role='menuitem'][aria-label='Power']")
    if pw.count():
        pw.first.click(timeout=6000)
        page.wait_for_timeout(1500)
        slider_state(page, "after Power click")

    log("")
    log("--- press ArrowLeft (keyboard) ---")
    page.keyboard.press("ArrowLeft")
    page.wait_for_timeout(1200)
    after_key = slider_state(page, "after ArrowLeft")

    log("")
    log("--- click the slider thumb ---")
    thumb = page.locator("[role='slider']")
    if thumb.count():
        try:
            thumb.first.click(timeout=5000, force=True)
            page.wait_for_timeout(1000)
            slider_state(page, "after thumb click")
        except Exception as exc:
            log("  thumb click failed: %.110s" % str(exc))

    log("")
    log("--- read the 'Select model' toggle aria-expanded + panel states ---")
    d = page.evaluate(
        """() => ({
            toggle_expanded: document.querySelector("[role='menuitem'][aria-label='Select model']")?.getAttribute('aria-expanded'),
            panels: Array.from(document.querySelectorAll("[data-testid^='composer-model-picker']")).map(e => ({
                testid: e.getAttribute('data-testid'),
                active: e.getAttribute('data-active'),
                inert: e.hasAttribute('inert'),
            })),
        })"""
    )
    log("  " + json.dumps(d))

    log("")
    log("CONCLUSION: slider settable = %s" % (before.get("now") != after_key.get("now")))
    log("  (account currently shows power level %s of %s-%s)" % (before.get("now"), before.get("min"), before.get("max")))

    page.keyboard.press("Escape")
    page.wait_for_timeout(500)
    page.keyboard.press("Escape")
    page.close()
    b.close()

open("/tmp/probe_slider.out", "w").write("\n".join(lines))
print("\nWROTE")

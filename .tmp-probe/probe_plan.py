"""Determine chatgpt.com plan entitlements + when the Power slider is adjustable."""
import json
import sys

sys.path.insert(0, "/app/apps/worker")

from playwright.sync_api import sync_playwright

CDP = "http://172.28.0.10:9223"
lines = []


def log(m=""):
    lines.append(str(m))
    print(m, flush=True)


def snap(page, label):
    data = page.evaluate(
        """() => {
            const slider = document.querySelector("[role='slider']");
            const power = document.querySelector("[role='menuitem'][aria-label='Power']");
            return {
                slider_now: slider ? slider.getAttribute('aria-valuenow') : null,
                power_disabled: power ? (power.getAttribute('aria-disabled') || power.getAttribute('data-disabled')) : null,
                options: Array.from(document.querySelectorAll("[role='menuitemradio']")).map(m => ({
                    checked: m.getAttribute('aria-checked'),
                    disabled: m.getAttribute('aria-disabled') || m.getAttribute('data-disabled'),
                    text: (m.innerText||'').trim().replace(/\\s+/g,' ').slice(0,40),
                })),
            };
        }"""
    )
    log("  [%s] slider=%s power_disabled=%s" % (label, data["slider_now"], data["power_disabled"]))
    for o in data["options"]:
        log("      " + json.dumps(o))


PILL = "button.__composer-pill[aria-haspopup='menu']"
MODEL = "[role='menuitemradio']:has-text(\"%s\")"

with sync_playwright() as p:
    b = p.chromium.connect_over_cdp(CDP)
    ctx = b.contexts[0] if b.contexts else b.new_context()
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto("https://chatgpt.com/", wait_until="domcontentloaded", timeout=45000)
    page.wait_for_timeout(7000)

    log("=== plan hints on the page ===")
    for probe in ["Upgrade", "Free", "Plus", "Pro", "Business"]:
        try:
            n = page.get_by_text(probe, exact=False).count()
        except Exception:
            n = "err"
        log("  text %-10r -> %s" % (probe, n))
    try:
        acct = page.evaluate(
            """() => {
                const t = document.body.innerText || '';
                const idx = t.indexOf('Chat history');
                return t.slice(0, 400).replace(/\\n+/g,' | ');
            }"""
        )
        log("  body head: %s" % acct[:400])
    except Exception as exc:
        log("  body read failed: %.100s" % str(exc))

    log("")
    log("=== state at whatever model is current ===")
    page.locator(PILL).first.click(timeout=10000)
    page.wait_for_timeout(1200)
    snap(page, "current")

    for target in ["GPT-5.6 Sol", "GPT-5.5"]:
        log("")
        log("=== switch to %r and inspect Power ===" % target)
        page.keyboard.press("Escape")
        page.wait_for_timeout(800)
        page.locator(PILL).first.click(timeout=10000)
        page.wait_for_timeout(1200)
        loc = page.locator(MODEL % target)
        if loc.count() == 0:
            log("  option not present")
            continue
        try:
            loc.first.click(timeout=8000)
            page.wait_for_timeout(2500)
            log("  clicked %r" % target)
        except Exception as exc:
            log("  click failed: %.130s" % str(exc))
            continue
        page.keyboard.press("Escape")
        page.wait_for_timeout(900)
        page.locator(PILL).first.click(timeout=10000)
        page.wait_for_timeout(1200)
        snap(page, "after selecting %s" % target)
        # Does clicking Power enable the slider?
        pw = page.locator("[role='menuitem'][aria-label='Power']")
        if pw.count():
            try:
                pw.first.click(timeout=6000)
                page.wait_for_timeout(1500)
                snap(page, "after Power click")
                now1 = page.locator("[role='slider']").first.get_attribute("aria-valuenow")
                page.keyboard.press("ArrowLeft")
                page.wait_for_timeout(900)
                now2 = page.locator("[role='slider']").first.get_attribute("aria-valuenow")
                log("  keyboard: %s -> %s" % (now1, now2))
                if now1 != now2:
                    page.keyboard.press("ArrowRight")
                    page.wait_for_timeout(900)
                    log("  restored to %s" % page.locator("[role='slider']").first.get_attribute("aria-valuenow"))
            except Exception as exc:
                log("  Power click failed: %.130s" % str(exc))

    log("")
    log("=== restore 'Latest' ===")
    page.keyboard.press("Escape")
    page.wait_for_timeout(900)
    page.locator(PILL).first.click(timeout=10000)
    page.wait_for_timeout(1200)
    loc = page.locator(MODEL % "Latest")
    if loc.count():
        try:
            loc.first.click(timeout=8000)
            page.wait_for_timeout(2000)
            log("  restored Latest")
        except Exception as exc:
            log("  restore failed: %.130s" % str(exc))
    page.keyboard.press("Escape")
    page.wait_for_timeout(800)
    page.locator(PILL).first.click(timeout=10000)
    page.wait_for_timeout(1200)
    snap(page, "FINAL")
    page.keyboard.press("Escape")

    page.close()
    b.close()

open("/tmp/probe_plan.out", "w").write("\n".join(lines))
print("\nWROTE")

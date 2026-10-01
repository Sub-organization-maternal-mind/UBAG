"""Mimic the engine's exact read->act->re-verify setting loop three times.

Purpose: find out whether the picker is reliably re-openable after Escape.
"""
import json
import sys

sys.path.insert(0, "/app/apps/worker")

from playwright.sync_api import sync_playwright

CDP = "http://172.28.0.10:9223"
PILL = "button.__composer-pill[aria-haspopup='menu']"
OPENER = "[role='menuitem'][aria-label='Select model']"
DESIRED = "GPT-5.6 Sol"
SAT = "[role='menuitemradio'][aria-checked='true']:has-text(\"%s\")" % DESIRED
APPLY = "[role='menuitemradio']:has-text(\"%s\")" % DESIRED

lines = []


def log(m=""):
    lines.append(str(m))
    print(m, flush=True)


def diag(page, label):
    d = page.evaluate(
        """(sel) => {
            const els = Array.from(document.querySelectorAll(sel));
            return els.map(e => {
                const r = e.getBoundingClientRect();
                const cs = getComputedStyle(e);
                const inertAncestor = e.closest('[inert]') ? true : false;
                return {
                    text: (e.innerText||'').trim().slice(0,30),
                    box: [Math.round(r.x), Math.round(r.y), Math.round(r.width), Math.round(r.height)],
                    display: cs.display, visibility: cs.visibility, opacity: cs.opacity,
                    pointerEvents: cs.pointerEvents,
                    inertAncestor: inertAncestor,
                };
            });
        }""",
        "[role='menuitemradio']",
    )
    log("  DIAG [%s]:" % label)
    for row in d:
        log("    " + json.dumps(row))


def dismiss(page):
    page.keyboard.press("Escape")
    page.wait_for_timeout(200)
    page.keyboard.press("Escape")
    page.wait_for_timeout(150)


with sync_playwright() as p:
    b = p.chromium.connect_over_cdp(CDP)
    ctx = b.contexts[0] if b.contexts else b.new_context()
    page = ctx.new_page()
    page.set_default_timeout(15000)
    page.goto("https://chatgpt.com/", wait_until="domcontentloaded", timeout=45000)
    page.wait_for_timeout(7000)

    for attempt in (1, 2, 3):
        log("")
        log("========== ATTEMPT %d ==========" % attempt)
        # _open_control step 0
        try:
            page.locator(PILL).first.click(timeout=6000)
            log("  pill clicked")
        except Exception as exc:
            log("  PILL CLICK FAILED: %.120s" % str(exc))
            page.reload(wait_until="domcontentloaded")
            page.wait_for_timeout(6000)
            continue
        page.wait_for_timeout(650)

        # open_steps[1] would be next generation: click the model opener
        try:
            n = page.locator(OPENER).count()
            log("  opener count=%d" % n)
            if n:
                page.locator(OPENER).first.click(timeout=6000)
                page.wait_for_timeout(650)
                log("  opener clicked")
        except Exception as exc:
            log("  OPENER CLICK FAILED: %.120s" % str(exc))

        # satisfied?
        sat_n = page.locator(SAT).count()
        log("  satisfied_when -> %d" % sat_n)
        if sat_n:
            log("  => already_set")
        else:
            diag(page, "attempt %d before apply" % attempt)
            try:
                page.locator(APPLY).first.click(timeout=6000)
                page.wait_for_timeout(1800)
                log("  applied %r" % DESIRED)
            except Exception as exc:
                log("  APPLY CLICK FAILED: %.120s" % str(exc))
                diag(page, "attempt %d after failed apply" % attempt)

        dismiss(page)
        log("  dismissed")

    # Leave the account as found.
    log("")
    log("=== restore Latest ===")
    try:
        page.locator(PILL).first.click(timeout=8000)
        page.wait_for_timeout(1000)
        op = page.locator(OPENER)
        if op.count():
            op.first.click(timeout=6000)
            page.wait_for_timeout(800)
        rest = page.locator("[role='menuitemradio']:has-text(\"Latest\")")
        if rest.count():
            rest.first.click(timeout=8000)
            page.wait_for_timeout(1500)
            log("  restored")
        else:
            log("  no Latest option")
    except Exception as exc:
        log("  restore failed: %.130s" % str(exc))
    dismiss(page)
    log("  pill text: %r" % page.locator(PILL).first.inner_text().strip()[:40])

    page.close()
    b.close()

open("/tmp/probe_loop.out", "w").write("\n".join(lines))
print("\nWROTE")

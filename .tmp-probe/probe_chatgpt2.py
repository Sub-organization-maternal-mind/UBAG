"""Deep probe of chatgpt.com: model menu path, submit button, response container.

Read-only apart from optionally submitting one throwaway prompt (--send) so the
submit button and response container can be observed in their real states.
"""
import json
import sys

sys.path.insert(0, "/app/apps/worker")

from playwright.sync_api import sync_playwright

CDP = "http://172.28.0.10:9223"
DO_SEND = "--send" in sys.argv
PROMPT = "Reply with the single word OK."

lines = []


def log(msg=""):
    lines.append(str(msg))
    print(msg, flush=True)


def menu_dump(page, label):
    rows = page.evaluate(
        """() => {
            const out = [];
            document.querySelectorAll("[role='menu'],[role='menuitem'],[role='menuitemradio'],[role='menuitemcheckbox'],[role='dialog']").forEach(m => {
                out.push({
                    role: m.getAttribute('role'),
                    aria: m.getAttribute('aria-label'),
                    checked: m.getAttribute('aria-checked'),
                    expanded: m.getAttribute('aria-expanded'),
                    popup: m.getAttribute('aria-haspopup'),
                    text: (m.innerText || '').trim().replace(/\\s+/g,' ').slice(0, 90),
                });
            });
            return out;
        }"""
    )
    log("  MENU DUMP [%s]: %d nodes" % (label, len(rows)))
    for r in rows:
        log("    " + json.dumps(r))


def composer_dump(page, label):
    rows = page.evaluate(
        """() => {
            const out = [];
            const scope = document.querySelector('form') || document.body;
            scope.querySelectorAll('button').forEach(b => {
                const cls = (b.className || '').toString();
                out.push({
                    cls: cls.slice(0, 150),
                    testid: b.getAttribute('data-testid'),
                    aria: b.getAttribute('aria-label'),
                    state: b.getAttribute('data-state'),
                    disabled: b.disabled || b.getAttribute('aria-disabled'),
                    text: (b.innerText || '').trim().slice(0, 40),
                });
            });
            return out;
        }"""
    )
    log("  COMPOSER BUTTONS [%s]:" % label)
    for r in rows:
        log("    " + json.dumps(r))


with sync_playwright() as p:
    browser = p.chromium.connect_over_cdp(CDP)
    ctx = browser.contexts[0] if browser.contexts else browser.new_context()
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto("https://chatgpt.com/", wait_until="domcontentloaded", timeout=45000)
    page.wait_for_timeout(7000)
    log("URL=%s TITLE=%s" % (page.url, page.title()))

    # --- 1. Open composer pill, inspect the compact menu ---
    log("")
    log("=== STEP 1: click composer pill ===")
    pill = page.locator("button.__composer-pill[aria-haspopup='menu']").first
    pill.click(timeout=10000)
    page.wait_for_timeout(1500)
    menu_dump(page, "compact after pill")

    # --- 2. Try to reach a model list ---
    log("")
    log("=== STEP 2: probe model openers ===")
    for sel in [
        "[role='menuitem'][aria-label='Select model']",
        "[role='menuitem'][aria-label*='model' i]",
        "[role='menuitem']:has-text(\"Select model\")",
        "[role='menuitem'][aria-haspopup='menu']:has-text(\"Model\")",
        "[role='menuitem'][aria-haspopup='menu']",
    ]:
        n = page.locator(sel).count()
        log("  %-60s -> %d" % (sel[:60], n))
        if n:
            try:
                page.locator(sel).first.click(timeout=6000)
                page.wait_for_timeout(1500)
                log("  CLICKED %s" % sel)
                menu_dump(page, "after %s" % sel[:40])
                break
            except Exception as exc:
                log("  click failed: %s" % str(exc)[:100])

    # --- 3. Probe menuitemradio options available ---
    log("")
    log("=== STEP 3: menuitemradio inventory ===")
    for r in page.evaluate(
        """() => Array.from(document.querySelectorAll("[role='menuitemradio']")).map(m => ({
            checked: m.getAttribute('aria-checked'),
            aria: m.getAttribute('aria-label'),
            text: (m.innerText||'').trim().replace(/\\s+/g,' ').slice(0,60),
        }))"""
    ):
        log("  " + json.dumps(r))

    # --- 4. Composer button state: empty vs filled ---
    log("")
    log("=== STEP 4: submit-button states ===")
    page.keyboard.press("Escape")
    page.wait_for_timeout(800)
    composer_dump(page, "EMPTY composer")

    log("")
    log("--- now type a prompt ---")
    box = page.locator("#prompt-textarea").first
    box.click(timeout=10000)
    box.fill(PROMPT)
    page.wait_for_timeout(1200)
    composer_dump(page, "FILLED composer")

    log("")
    log("=== STEP 5: submit selectors (filled) ===")
    for sel in [
        "button[data-testid='send-button']",
        "button[aria-label*='Send']",
        "button[type='submit']",
        "button[data-testid='composer-submit-button']",
        "button.composer-submit-button-color",
        "button[aria-label='Send prompt']",
    ]:
        try:
            n = page.locator(sel).count()
        except Exception as exc:
            n = "ERR(%s)" % str(exc)[:50]
        log("  %-58s -> %s" % (sel[:58], n))

    if DO_SEND:
        log("")
        log("=== STEP 6: SEND + observe response ===")
        for sel in [
            "button[data-testid='send-button']",
            "button[aria-label*='Send']",
            "button.composer-submit-button-color",
            "button[type='submit']",
        ]:
            loc = page.locator(sel)
            try:
                if loc.count() == 0:
                    continue
                loc.first.click(timeout=8000)
                log("  SENT via %s" % sel)
                break
            except Exception as exc:
                log("  send via %s failed: %s" % (sel, str(exc)[:90]))
        page.wait_for_timeout(2500)
        composer_dump(page, "JUST AFTER SEND (look for stop button)")
        log("")
        log("  response_container candidates after send:")
        for sel in [
            "div[data-message-author-role='assistant']",
            "div.markdown.prose",
            "[data-testid^='conversation-turn'] .markdown",
            "[data-message-author-role='assistant'] .markdown",
            "article[data-testid^='conversation-turn']",
            "[data-turn='assistant']",
        ]:
            try:
                n = page.locator(sel).count()
            except Exception as exc:
                n = "ERR(%s)" % str(exc)[:50]
            log("    %-56s -> %s" % (sel[:56], n))

        log("")
        log("  waiting 35s for the answer to settle...")
        page.wait_for_timeout(35000)
        log("  streaming_indicator candidates after settle:")
        for sel in [
            "button[data-testid='stop-button']",
            "button[aria-label*='Stop']",
            ".result-streaming",
        ]:
            try:
                n = page.locator(sel).count()
            except Exception as exc:
                n = "ERR(%s)" % str(exc)[:50]
            log("    %-56s -> %s" % (sel[:56], n))
        log("  assistant text (first 200 chars):")
        try:
            txt = page.locator("div[data-message-author-role='assistant']").last.inner_text()
            log("    " + txt.strip()[:200].replace("\n", " | "))
        except Exception as exc:
            log("    read failed: %s" % str(exc)[:120])
        log("  FINAL URL: %s" % page.url)

    browser.close()

with open("/tmp/probe_chatgpt2.out", "w") as fh:
    fh.write("\n".join(lines))
print("\nWROTE", flush=True)

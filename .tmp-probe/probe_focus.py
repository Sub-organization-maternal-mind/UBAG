"""Focused probe: chatgpt.com composer + advanced model menu, to re-baseline selectors."""
import json

from playwright.sync_api import sync_playwright

CDP = "http://172.28.0.10:9223"

GROUP_PROBES = {
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
    "authenticated_signal": [
        "#prompt-textarea",
        "nav[aria-label='Chat history']",
        "button[data-testid='profile-button']",
    ],
    "model_step1": [
        "button.__composer-pill[aria-haspopup='menu']",
        "button[class*='composer-pill'][aria-haspopup='menu']",
    ],
}


def dump_pill(page):
    return page.evaluate(
        """() => {
            const out = [];
            // Anything clickable in the composer bar that opens a menu.
            const scope = document.querySelector('form') || document.body;
            scope.querySelectorAll('button').forEach(b => {
                const cls = (b.className || '').toString();
                out.push({
                    cls: cls.slice(0, 220),
                    testid: b.getAttribute('data-testid'),
                    aria: b.getAttribute('aria-label'),
                    popup: b.getAttribute('aria-haspopup'),
                    expanded: b.getAttribute('aria-expanded'),
                    state: b.getAttribute('data-state'),
                    text: (b.innerText || '').trim().slice(0, 80),
                });
            });
            return out;
        }"""
    )


def dump_menu(page):
    return page.evaluate(
        """() => {
            const out = [];
            document.querySelectorAll("[role='menuitem'], [role='menuitemradio'], [role='menu']").forEach(m => {
                out.push({
                    role: m.getAttribute('role'),
                    aria: m.getAttribute('aria-label'),
                    checked: m.getAttribute('aria-checked'),
                    expanded: m.getAttribute('aria-expanded'),
                    popup: m.getAttribute('aria-haspopup'),
                    testid: m.getAttribute('data-testid'),
                    text: (m.innerText || '').trim().slice(0, 70),
                });
            });
            return out;
        }"""
    )


with sync_playwright() as p:
    browser = p.chromium.connect_over_cdp(CDP)
    ctx = browser.contexts[0] if browser.contexts else browser.new_context()
    page = ctx.new_page()
    page.set_default_timeout(20000)
    page.goto("https://chatgpt.com/", wait_until="domcontentloaded", timeout=45000)
    page.wait_for_timeout(7000)
    lines = []
    lines.append("URL: %s" % page.url)
    lines.append("TITLE: %s" % page.title())

    lines.append("")
    lines.append("=== GROUP PROBE COUNTS ===")
    for group, sels in GROUP_PROBES.items():
        for s in sels:
            try:
                n = page.locator(s).count()
            except Exception as exc:
                n = "ERR %s" % str(exc)[:70]
            lines.append("  [%s] %-70s -> %s" % (group, s, n))

    lines.append("")
    lines.append("=== COMPOSER FORM BUTTONS ===")
    for row in dump_pill(page):
        lines.append("  " + json.dumps(row))

    # Try to open the composer pill -> advanced -> Model path.
    lines.append("")
    lines.append("=== CLICK: composer pill ===")
    for sel in GROUP_PROBES["model_step1"]:
        loc = page.locator(sel)
        try:
            cnt = loc.count()
        except Exception as exc:
            lines.append("  %s -> ERR %s" % (sel, str(exc)[:60]))
            continue
        lines.append("  %s -> count=%d" % (sel, cnt))
        if cnt:
            try:
                loc.first.click(timeout=8000)
                page.wait_for_timeout(1500)
                lines.append("  CLICKED %s" % sel)
                break
            except Exception as exc:
                lines.append("  click failed: %s" % str(exc)[:120])

    lines.append("")
    lines.append("=== MENU AFTER PILL CLICK ===")
    for row in dump_menu(page):
        lines.append("  " + json.dumps(row))

    browser.close()

print("\n".join(lines))

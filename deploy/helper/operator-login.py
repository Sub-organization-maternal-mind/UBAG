"""Open one provisioned helper profile for a human login; never enter credentials.

Run through an operator SSH session while helper dispatch is drained:
docker exec -it ubag-helper python3 /app/operator-login.py PROVIDER pr_REFERENCE
Close with Ctrl-C after the human completes login through the loopback viewer.
"""
import json
import os
import re
import sys
from pathlib import Path

from playwright.sync_api import sync_playwright

if len(sys.argv) != 3:
    raise SystemExit("usage: operator-login.py PROVIDER pr_REFERENCE")
provider, profile = sys.argv[1:]
if not re.fullmatch(r"[a-z][a-z0-9_]{0,63}", provider) or not re.fullmatch(r"pr_[A-Za-z0-9_-]{1,128}", profile):
    raise SystemExit("invalid provider/profile reference")
manifest = json.loads((Path("/app/adapters") / provider / "manifest.json").read_text())
directory = Path(os.environ["UBAG_HELPER_PROFILE_ROOT"]) / "helper" / profile
directory.mkdir(parents=True, exist_ok=True, mode=0o700)
with sync_playwright() as browser:
    context = browser.chromium.launch_persistent_context(str(directory), headless=False, args=["--no-sandbox", "--disable-dev-shm-usage"])
    try:
        page = context.new_page()
        page.goto(manifest["target_homepage"])
        print("Complete login manually through your SSH-tunnelled browser viewer; press Ctrl-C when done.", flush=True)
        while True:
            page.wait_for_timeout(1000)
    except KeyboardInterrupt:
        pass
    finally:
        context.close()

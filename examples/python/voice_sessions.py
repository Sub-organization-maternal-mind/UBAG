"""Backend voice-session management: create, queue handling, heartbeat, mute, cleanup.

Media (WebRTC) is the client's job: a browser (examples/javascript/voice-browser.html)
or an aiortc app produces the SDP offer and calls connect_voice_session through YOUR backend.

    python examples/python/voice_sessions.py [target]
"""
import sys

from _common import client_from_env
from ubag_client import UbagError

target = sys.argv[1] if len(sys.argv) > 1 else "chatgpt_web"
client = client_from_env()

try:
    created = client.create_voice_session(target)  # mode defaults to "live"
except UbagError as error:
    if error.status == 429:
        # Per-tenant session or queue budget reached (client already waited/retried when the wait was short).
        sys.exit("Voice budget reached (%s); retry in %s ms." % (error.code, error.retry_after_ms))
    raise

print(created["status"], "session", created["session_id"])  # connecting (201) or queued (202)
session_id = created["session_id"]
try:
    # Status values: queued | connecting | connected | terminated. "connected" only once the provider
    # voice UI is verified ready AND the client media is up; failures end the session with
    # last_error such as "activation_failed: <state>".
    session = client.get_voice_session(session_id)["session"]
    print("status:", session["status"], session.get("last_error", ""))

    # During a call send this every ~60s; a lapsed lease frees the provider account.
    client.renew_voice_session(session_id)
    client.mute_voice_session(session_id, True)  # mic off; the provider can still speak

    sessions = client.list_voice_sessions(target=target, limit=10)["data"]
    print("my sessions:", ", ".join("%s:%s" % (s["session_id"], s["status"]) for s in sessions))
finally:
    client.terminate_voice_session(session_id)  # always release the account and media path

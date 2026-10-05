"""Capability discovery: what each target accepts and whether live voice can start now.

    python examples/python/capabilities.py
"""
from _common import client_from_env

client = client_from_env()
targets = client.capabilities()["data"]

for target in targets:
    voice, parts = target["voice"], target["inline_message_parts"]
    print(target["target"])
    print("  images: %s   audio: %s" % (", ".join(parts["image_url"]) or "none", ", ".join(parts["input_audio"]) or "none"))
    # supported = adapter declares live voice; configured = this gateway can serve it;
    # verified = a live acceptance run is recorded; available = a free account exists now.
    print(
        "  voice: supported=%s configured=%s verified=%s available=%s free=%s utterance_jobs=%s"
        % (voice["supported"], voice["configured"], voice["verified"], voice["available"], voice["free_resources"], voice["utterance_jobs"])
    )

ready = [t["target"] for t in targets if t["voice"]["available"]]
print("Live voice can start now on: " + ", ".join(ready) if ready else "No target can start live voice right now.")

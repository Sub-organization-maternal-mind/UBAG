"""Utterance mode: audio in, text out, no live voice leases.

    python examples/python/utterance.py [target] [clip.wav]
"""
import json
import sys
import time

from _common import client_from_env, read_or, silent_wav

TERMINAL = {"completed", "completed_with_warnings", "failed_retryable", "failed_terminal", "dead_letter", "cancelled", "timed_out"}

target = sys.argv[1] if len(sys.argv) > 1 else "chatgpt_web"
audio_path = sys.argv[2] if len(sys.argv) > 2 else None
client = client_from_env()

created = client.create_voice_session(target, mode="utterance")
print("session", created["session_id"], "job", created["job_id"])

# The backing job is held until the audio arrives under the declared key.
client.put_job_artifact(created["job_id"], "utterance.wav", read_or(audio_path, silent_wav), "audio/wav")

while True:
    time.sleep(2)
    job = client.get_job(created["job_id"])
    print("job status:", job["status"])
    if job["status"] in TERMINAL:
        break

print(json.dumps(job.get("result"), indent=2))
client.terminate_voice_session(created["session_id"])

"""Multimodal chat: text + image + audio in one OpenAI-compatible request.

    python examples/python/multimodal_chat.py [target] [image.png] [clip.wav]

Without file arguments a 1x1 PNG and half a second of silence are used.
"""
import sys

from _common import client_from_env, read_or, silent_wav, tiny_png
from ubag_client import UbagError, audio_part, image_part, text_part

target = sys.argv[1] if len(sys.argv) > 1 else "chatgpt_web"
image_path = sys.argv[2] if len(sys.argv) > 2 else None
audio_path = sys.argv[3] if len(sys.argv) > 3 else None
client = client_from_env()

# The gateway rejects parts a target does not accept; check first.
capability = client.capability(target)
if capability is None:
    sys.exit("Unknown target " + target)
if "image/png" not in capability["inline_message_parts"]["image_url"]:
    print(target + " does not accept inline PNG images.", file=sys.stderr)
if "audio/wav" not in capability["inline_message_parts"]["input_audio"]:
    print(target + " does not accept inline WAV audio.", file=sys.stderr)

try:
    reply = client.chat_completion(
        target,
        [
            {
                "role": "user",
                "content": [
                    text_part("Describe the image, then transcribe the audio clip."),
                    image_part(read_or(image_path, tiny_png), "image/png"),
                    audio_part(read_or(audio_path, silent_wav), "wav"),
                ],
            }
        ],
    )
    print(reply["choices"][0]["message"]["content"])
except UbagError as error:
    # Facade errors are OpenAI-shaped; 429/503 were already retried (bounded) using Retry-After.
    sys.exit("%s retry_after_ms=%s" % (error, error.retry_after_ms))

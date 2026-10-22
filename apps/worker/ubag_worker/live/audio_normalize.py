"""Normalize an audio attachment to a container the target's model can decode.

A voice message arrives in whatever the sender's app produced: webm/opus
(WhatsApp, Telegram, Chrome's MediaRecorder), ogg/opus (Telegram), m4a/AAC
(iOS voice memos), mp3 (older apps). The PROVIDER's model decodes a different
set - measured live on 2026-10-21, ``chatgpt_web`` decoded wav and mp3 and
silently dropped mp4, ogg and webm, while ``gemini_web`` decoded webm.

Chrome can decode every one of those containers, and the worker already
drives Chrome. So the recording is converted to 16-bit PCM WAV (the one
container every measured provider decodes) before it is attached: the
provider then receives something it can actually read, and the caller's voice
message works on every provider instead of only on the one whose decoder
happens to like its container.

The conversion happens in a THROWAWAY blank tab of the same browser context
- never in the provider's own page, so the provider's DOM and state are not
touched by it.
"""

from __future__ import annotations

import base64
import os
import tempfile
from typing import Optional

# The container every measured provider decodes. 16-bit PCM WAV is the
# baseline every browser, every audio library and every provider model can
# read, and it keeps the "provider_formats" contract in the adapter manifests
# a subset of this.
NORMALIZED_CONTENT_TYPE = "audio/wav"
NORMALIZED_EXTENSION = ".wav"

# A sample rate that keeps a voice message small: 16 kHz mono is telephone
# quality and roughly a third of the bytes of 48 kHz stereo. The measured
# chatgpt_web recording was 22.05 kHz mono, so anything in this range is
# inside what the providers accept.
NORMALIZED_SAMPLE_RATE = 16000

# Guards: a 24 MiB artifact decoded to PCM is ~24 MB of float samples and
# ~48 MB of 16-bit samples in the page, so the input is bounded.
MAX_INPUT_BYTES = 24 * 1024 * 1024
# The rendering itself is bounded: a 30-minute recording at 16 kHz mono is
# 57.6 MB of PCM, which is more than any provider wants in a chat message.
MAX_OUTPUT_SECONDS = 1800

# The decode+render, executed inside the browser. Kept as one function so
# there is exactly one place where the audio contract lives.
_DECODE_JS = """
async (payload) => {
  const rate = %(rate)d;
  const maxSeconds = %(max_seconds)d;
  const bin = atob(payload.b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) { bytes[i] = bin.charCodeAt(i); }
  const probe = new OfflineAudioContext(1, 1, rate);
  const decoded = await probe.decodeAudioData(bytes.buffer);
  const frames = Math.min(Math.ceil(decoded.duration * rate), maxSeconds * rate);
  const out = new OfflineAudioContext(1, Math.max(frames, 1), rate);
  const source = out.createBufferSource();
  source.buffer = decoded;
  source.connect(out.destination);
  source.start();
  const rendered = await out.startRendering();
  const channel = rendered.getChannelData(0);
  const pcm = new Int16Array(channel.length);
  for (let i = 0; i < channel.length; i++) {
    const s = channel[i];
    pcm[i] = s < 0 ? s * 0x8000 : s * 0x7fff;
  }
  const buffer = new ArrayBuffer(44 + pcm.byteLength);
  const view = new DataView(buffer);
  const write = (offset, text) => {
    for (let i = 0; i < text.length; i++) { view.setUint8(offset + i, text.charCodeAt(i)); }
  };
  write(0, 'RIFF');
  view.setUint32(4, 36 + pcm.byteLength, true);
  write(8, 'WAVE');
  write(12, 'fmt ');
  view.setUint32(16, 16, true);
  view.setUint16(20, 1, true);            // PCM
  view.setUint16(22, 1, true);            // mono
  view.setUint32(24, rate, true);
  view.setUint32(28, rate * 2, true);     // byte rate
  view.setUint16(32, 2, true);            // block align
  view.setUint16(34, 16, true);           // bits per sample
  write(36, 'data');
  view.setUint32(40, pcm.byteLength, true);
  new Int16Array(buffer, 44).set(pcm);
  const raw = new Uint8Array(buffer);
  let binary = '';
  for (let i = 0; i < raw.length; i += 0x8000) {
    binary += String.fromCharCode.apply(null, raw.subarray(i, i + 0x8000));
  }
  return { b64: btoa(binary), frames: pcm.length, rate: rate };
}
""" % {"rate": NORMALIZED_SAMPLE_RATE, "max_seconds": MAX_OUTPUT_SECONDS}


class AudioNormalizationError(RuntimeError):
    """The recording could not be decoded into PCM."""


def needs_normalization(content_type: str) -> bool:
    """True when the container must be converted before it is attached.

    WAV is the normalized form, so it is attached as-is; every other
    container the caller may send (mp3, mp4/m4a, ogg, webm) is converted.
    """
    return (content_type or "").strip().lower() not in ("audio/wav", "audio/x-wav")


def normalized_path_for(original_path: str) -> str:
    """A unique temp file for the converted recording."""
    base = os.path.basename(original_path or "recording")
    stem = base.rsplit(".", 1)[0] if "." in base else base
    handle, path = tempfile.mkstemp(prefix="ubag-audio-", suffix=NORMALIZED_EXTENSION)
    os.close(handle)
    # The provider shows the file name in its composer, so the original stem is
    # kept: "speech1_42.webm" becomes "...-speech1_42.wav", not a random name.
    wanted = "ubag-audio-%s%s" % (stem[:40], NORMALIZED_EXTENSION)
    if os.path.basename(path) != wanted:
        try:
            os.replace(path, os.path.join(os.path.dirname(path), wanted))
            path = os.path.join(os.path.dirname(path), wanted)
        except OSError:
            pass
    return path


def normalize_with_browser(page, path: str) -> Optional[str]:
    """Convert an audio file to 16-bit PCM WAV using the browser's decoder.

    ``page`` is a Playwright page (any tab of the browser context). Returns
    the path of the converted file, or None when the browser could not decode
    the recording (an unsupported codec) - the caller then fails the job with
    a clear reason instead of attaching bytes the provider would drop.
    """
    try:
        size = os.path.getsize(path)
    except OSError:
        return None
    if size <= 0 or size > MAX_INPUT_BYTES:
        return None
    with open(path, "rb") as handle:
        payload = base64.b64encode(handle.read()).decode("ascii")
    try:
        result = page.evaluate(
            _DECODE_JS, {"b64": payload, "rate": NORMALIZED_SAMPLE_RATE,
                         "max_seconds": MAX_OUTPUT_SECONDS}
        )
    except Exception:
        return None
    if not isinstance(result, dict) or not result.get("b64"):
        return None
    try:
        data = base64.b64decode(result["b64"])
    except Exception:
        return None
    if len(data) <= 44:
        return None
    target = normalized_path_for(path)
    try:
        with open(target, "wb") as handle:
            handle.write(data)
    except OSError:
        return None
    return target

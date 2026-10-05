# ubag-client (Python)

Standard-library-only client for the UBAG gateway: capability discovery,
multimodal chat (OpenAI-compatible facade) and voice-session management, with
bounded `Retry-After`-aware retry on 429/503.

```python
from ubag_client import UbagClient, text_part, image_part, audio_part

client = UbagClient("http://127.0.0.1:8080", app_secret="...")  # keep the secret server-side
client.capability("chatgpt_web")["voice"]["available"]
client.chat_completion("chatgpt_web", [{"role": "user", "content": [
    text_part("What is this?"), image_part(png_bytes, "image/png"), audio_part(wav_bytes, "wav")]}])
session = client.create_voice_session("chatgpt_web")
```

Retries: `max_retries` (default 2) extra attempts after 429/503, sleeping for the
server's `retry_after_ms` / `Retry-After`; a wait above `max_retry_wait`
(default 30 s) is raised immediately as `UbagError` (`.retry_after_ms`).

Tests (offline, local `http.server`; not part of `pnpm test:sdk` so Node-only
environments do not need Python):

```bash
python -m pytest -q packages/sdk-python      # or: pnpm test:sdk:python
```

Requires Python 3.9+. Examples: `examples/python/`.

"""Stdlib-only Python client for the UBAG gateway: capabilities, multimodal
chat (OpenAI-compatible facade) and voice-session management.

Auth: pass the app secret / PAT as ``app_secret``. Keep it server-side; never
hand it to a browser (give browsers only the connect response, see
examples/javascript/voice-backend.mjs).
"""
from __future__ import annotations

import base64
import json
import time
import urllib.error
import urllib.parse
import urllib.request
from email.utils import parsedate_to_datetime
from typing import Any, Callable, Dict, List, Optional, Union

DEFAULT_API_VERSION = "2026-05-22"
SDK_VERSION = "0.0.0"
RETRYABLE_STATUSES = (429, 503)

Bytes = Union[bytes, bytearray, memoryview]


class UbagError(Exception):
    """Non-2xx gateway answer. ``retry_after_ms`` is the server's retry
    guidance (``retry_after_ms`` in the body wins over the Retry-After header)."""

    def __init__(self, status: int, body: Any, headers: Dict[str, str]):
        error = body.get("error") if isinstance(body, dict) and isinstance(body.get("error"), dict) else {}
        self.status = status
        self.body = body
        self.headers = headers
        self.code: Optional[str] = error.get("code")
        self.message: str = error.get("message") or "UBAG API request failed with HTTP %d" % status
        self.retryable: bool = bool(error.get("retryable", status in RETRYABLE_STATUSES))
        self.retry_after_ms: Optional[int] = _retry_after_ms(error, headers)
        super().__init__("%s (HTTP %d%s)" % (self.message, status, ", %s" % self.code if self.code else ""))


def _retry_after_ms(error: Dict[str, Any], headers: Dict[str, str]) -> Optional[int]:
    if isinstance(error.get("retry_after_ms"), (int, float)):
        return int(error["retry_after_ms"])
    value = headers.get("retry-after")
    if not value:
        return None
    try:
        return max(0, int(float(value) * 1000))
    except ValueError:
        pass
    try:
        delta = parsedate_to_datetime(value).timestamp() - time.time()
    except (TypeError, ValueError):
        return None
    return max(0, int(delta * 1000))


# ── multimodal content parts ────────────────────────────────────────────────


def text_part(text: str) -> Dict[str, Any]:
    return {"type": "text", "text": text}


def image_part(source: Union[str, Bytes], mime: Optional[str] = None) -> Dict[str, Any]:
    """Image part from a ``data:`` URL or from bytes plus a MIME type.
    Remote URLs are rejected by the gateway, so they are rejected here too."""
    if isinstance(source, str):
        if not source.startswith("data:"):
            raise ValueError("image_part: pass a data: URL or bytes with a MIME type; remote URLs are not supported")
        return {"type": "image_url", "image_url": {"url": source}}
    if not mime:
        raise ValueError("image_part: a MIME type (for example image/png) is required with bytes")
    encoded = base64.b64encode(bytes(source)).decode("ascii")
    return {"type": "image_url", "image_url": {"url": "data:%s;base64,%s" % (mime, encoded)}}


def audio_part(data: Bytes, fmt: str) -> Dict[str, Any]:
    """Audio part; ``fmt`` is ``"wav"`` or ``"mp3"``."""
    if fmt not in ("wav", "mp3"):
        raise ValueError('audio_part: fmt must be "wav" or "mp3"')
    return {"type": "input_audio", "input_audio": {"data": base64.b64encode(bytes(data)).decode("ascii"), "format": fmt}}


# ── client ──────────────────────────────────────────────────────────────────


class UbagClient:
    def __init__(
        self,
        base_url: str,
        app_secret: Optional[str] = None,
        *,
        api_version: str = DEFAULT_API_VERSION,
        timeout: float = 300.0,
        max_retries: int = 2,
        max_retry_wait: float = 30.0,
        sleep: Callable[[float], None] = time.sleep,
    ):
        """``max_retries`` bounds extra attempts after 429/503; a server that
        asks for a wait longer than ``max_retry_wait`` seconds is not retried
        (the UbagError carries ``retry_after_ms`` for the caller to act on)."""
        self.base_url = base_url.rstrip("/") + "/"
        self.app_secret = app_secret
        self.api_version = api_version
        self.timeout = timeout
        self.max_retries = max_retries
        self.max_retry_wait = max_retry_wait
        self._sleep = sleep

    # capabilities

    def capabilities(self) -> Dict[str, Any]:
        return self.request("GET", "/v1/capabilities")

    def capability(self, target: str) -> Optional[Dict[str, Any]]:
        for entry in self.capabilities().get("data", []):
            if entry.get("target") == target:
                return entry
        return None

    # multimodal chat

    def chat_completion(self, model: str, messages: List[Dict[str, Any]], **extra: Any) -> Dict[str, Any]:
        """``messages[i]["content"]`` is a string or a list of text/image/audio parts.
        Facade errors are OpenAI-shaped; they still raise UbagError."""
        return self.request("POST", "/v1/openai/chat/completions", {"model": model, "messages": messages, **extra})

    # voice sessions

    def create_voice_session(
        self,
        target: str,
        *,
        identity_ref: Optional[str] = None,
        ttl_seconds: Optional[int] = None,
        mode: Optional[str] = None,
    ) -> Dict[str, Any]:
        body = {"target": target, "identity_ref": identity_ref, "ttl_seconds": ttl_seconds, "mode": mode}
        return self.request("POST", "/v1/voice/sessions", {k: v for k, v in body.items() if v is not None})

    def list_voice_sessions(self, *, limit: Optional[int] = None, cursor: Optional[str] = None, target: Optional[str] = None) -> Dict[str, Any]:
        query = {"limit": limit, "cursor": cursor, "target": target}
        qs = urllib.parse.urlencode({k: v for k, v in query.items() if v not in (None, "")})
        return self.request("GET", "/v1/voice/sessions" + ("?" + qs if qs else ""))

    def get_voice_session(self, session_id: str) -> Dict[str, Any]:
        return self.request("GET", self._voice_path(session_id))

    def connect_voice_session(self, session_id: str, sdp_offer: str) -> Dict[str, Any]:
        return self.request("POST", self._voice_path(session_id, "/connect"), {"sdp_offer": sdp_offer})

    def mute_voice_session(self, session_id: str, muted: bool) -> Dict[str, Any]:
        return self.request("POST", self._voice_path(session_id, "/mute"), {"muted": muted})

    def renew_voice_session(self, session_id: str) -> Dict[str, Any]:
        return self.request("POST", self._voice_path(session_id, "/renew"))

    def terminate_voice_session(self, session_id: str) -> Dict[str, Any]:
        return self.request("POST", self._voice_path(session_id, "/terminate"))

    # jobs (utterance mode: upload the audio, poll the job)

    def get_job(self, job_id: str) -> Dict[str, Any]:
        return self.request("GET", "/v1/jobs/" + urllib.parse.quote(job_id, safe=""))

    def put_job_artifact(self, job_id: str, key: str, data: Bytes, content_type: str) -> Dict[str, Any]:
        path = "/v1/jobs/%s/artifacts/%s" % (urllib.parse.quote(job_id, safe=""), urllib.parse.quote(key, safe=""))
        return self.request("PUT", path, raw=bytes(data), content_type=content_type)

    @staticmethod
    def _voice_path(session_id: str, action: str = "") -> str:
        return "/v1/voice/sessions/" + urllib.parse.quote(session_id, safe="") + action

    # transport

    def request(
        self,
        method: str,
        path: str,
        body: Optional[Dict[str, Any]] = None,
        *,
        raw: Optional[bytes] = None,
        content_type: Optional[str] = None,
    ) -> Dict[str, Any]:
        attempt = 0
        while True:
            try:
                return self._send(method, path, body, raw, content_type)
            except UbagError as error:
                wait = (error.retry_after_ms or 1000) / 1000.0
                if error.status not in RETRYABLE_STATUSES or attempt >= self.max_retries or wait > self.max_retry_wait:
                    raise
                self._sleep(wait)
                attempt += 1

    def _send(self, method: str, path: str, body: Optional[Dict[str, Any]], raw_body: Optional[bytes], content_type: Optional[str]) -> Dict[str, Any]:
        headers = {
            "Accept": "application/json",
            "Ubag-Api-Version": self.api_version,
            "Ubag-Sdk-Name": "ubag-python",
            "Ubag-Sdk-Version": SDK_VERSION,
        }
        data = raw_body
        if raw_body is not None:
            headers["Content-Type"] = content_type or "application/octet-stream"
        elif body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        if self.app_secret:
            headers["Authorization"] = "Bearer " + self.app_secret
        request = urllib.request.Request(urllib.parse.urljoin(self.base_url, path.lstrip("/")), data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(request, timeout=self.timeout) as response:
                raw = response.read()
        except urllib.error.HTTPError as http_error:
            raw = http_error.read()
            try:
                parsed: Any = json.loads(raw) if raw else None
            except ValueError:
                parsed = raw.decode("utf-8", "replace")
            raise UbagError(http_error.code, parsed, {k.lower(): v for k, v in http_error.headers.items()}) from None
        return json.loads(raw) if raw else {}

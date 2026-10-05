import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from ubag_client import UbagClient, UbagError, audio_part, image_part, text_part


def _decode(raw, content_type):
    if not raw:
        return None
    return json.loads(raw) if content_type == "application/json" else raw


class Gateway:
    """Local fake gateway: scripted (status, headers, body) answers, recorded calls."""

    def __init__(self, answers):
        self.answers = list(answers)
        self.calls = []
        gateway = self

        class Handler(BaseHTTPRequestHandler):
            def _handle(self):
                length = int(self.headers.get("Content-Length") or 0)
                raw = self.rfile.read(length) if length else b""
                gateway.calls.append({
                    "method": self.command,
                    "path": self.path,
                    "auth": self.headers.get("Authorization"),
                    "body": _decode(raw, self.headers.get("Content-Type")),
                    "content_type": self.headers.get("Content-Type"),
                })
                status, headers, body = gateway.answers.pop(0) if len(gateway.answers) > 1 else gateway.answers[0]
                payload = json.dumps(body).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                for key, value in headers.items():
                    self.send_header(key, value)
                self.end_headers()
                self.wfile.write(payload)

            do_GET = do_POST = do_PUT = _handle

            def log_message(self, *args):
                pass

        self.server = HTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = "http://127.0.0.1:%d" % self.server.server_address[1]

    def close(self):
        self.server.shutdown()
        self.server.server_close()


@pytest.fixture
def gateway_factory():
    created = []

    def make(*answers):
        gateway = Gateway(answers)
        created.append(gateway)
        return gateway

    yield make
    for gateway in created:
        gateway.close()


def ok(body, status=200):
    return (status, {}, body)


def test_capabilities_and_lookup(gateway_factory):
    gateway = gateway_factory(ok({"kind": "capabilities", "data": [{"target": "chatgpt_web", "voice": {"supported": True, "available": False}}]}))
    client = UbagClient(gateway.url, "s3cret")
    assert client.capability("chatgpt_web")["voice"]["supported"] is True
    assert client.capability("missing") is None
    assert gateway.calls[0]["path"] == "/v1/capabilities"
    assert gateway.calls[0]["auth"] == "Bearer s3cret"


def test_multimodal_chat_parts(gateway_factory):
    gateway = gateway_factory(ok({"choices": [{"message": {"role": "assistant", "content": "a cat"}}]}))
    client = UbagClient(gateway.url, "s3cret")
    reply = client.chat_completion(
        "chatgpt_web",
        [{"role": "user", "content": [text_part("what is this?"), image_part(b"\x01\x02\x03", "image/png"), audio_part(b"\x04\x05", "wav")]}],
    )
    assert reply["choices"][0]["message"]["content"] == "a cat"
    sent = gateway.calls[0]["body"]
    assert gateway.calls[0]["path"] == "/v1/openai/chat/completions"
    assert sent["messages"][0]["content"] == [
        {"type": "text", "text": "what is this?"},
        {"type": "image_url", "image_url": {"url": "data:image/png;base64,AQID"}},
        {"type": "input_audio", "input_audio": {"data": "BAU=", "format": "wav"}},
    ]


def test_part_builders_validate():
    assert image_part("data:image/png;base64,AAAA")["image_url"]["url"].startswith("data:")
    with pytest.raises(ValueError):
        image_part("https://example.com/cat.png")
    with pytest.raises(ValueError):
        image_part(b"x")
    with pytest.raises(ValueError):
        audio_part(b"x", "ogg")


def test_voice_session_lifecycle_routes(gateway_factory):
    gateway = gateway_factory(
        ok({"kind": "voice_session", "session_id": "voice_1", "status": "queued", "session": {}}, 202),
        ok({"kind": "voice_session_connection", "session_id": "voice_1", "sdp_answer": "v=0", "media_credential": "c", "ice_servers": [{"urls": ["stun:x"]}]}),
        ok({"kind": "voice_session_control", "muted": True}),
        ok({"kind": "voice_session_control", "lease_expires_at": "2026-10-05T00:10:00Z"}),
        ok({"kind": "voice_session", "session": {"status": "connected"}}),
        ok({"kind": "voice_sessions", "data": []}),
        ok({"kind": "voice_session", "session": {"status": "terminated"}}),
    )
    client = UbagClient(gateway.url, "s3cret")
    assert client.create_voice_session("chatgpt_web", ttl_seconds=60, mode="live")["status"] == "queued"
    assert client.connect_voice_session("voice_1", "offer")["ice_servers"][0]["urls"] == ["stun:x"]
    assert client.mute_voice_session("voice_1", True)["muted"] is True
    assert "lease_expires_at" in client.renew_voice_session("voice_1")
    assert client.get_voice_session("voice_1")["session"]["status"] == "connected"
    client.list_voice_sessions(limit=5, target="chatgpt_web")
    assert client.terminate_voice_session("voice_1")["session"]["status"] == "terminated"

    routes = [(c["method"], c["path"], c["body"]) for c in gateway.calls]
    assert routes == [
        ("POST", "/v1/voice/sessions", {"target": "chatgpt_web", "ttl_seconds": 60, "mode": "live"}),
        ("POST", "/v1/voice/sessions/voice_1/connect", {"sdp_offer": "offer"}),
        ("POST", "/v1/voice/sessions/voice_1/mute", {"muted": True}),
        ("POST", "/v1/voice/sessions/voice_1/renew", None),
        ("GET", "/v1/voice/sessions/voice_1", None),
        ("GET", "/v1/voice/sessions?limit=5&target=chatgpt_web", None),
        ("POST", "/v1/voice/sessions/voice_1/terminate", None),
    ]


QUEUE_FULL = {"error": {"code": "UBAG-VOICE-QUEUE-FULL-004", "category": "voice", "message": "queue full", "retryable": True, "trace_id": "t"}}


def test_retry_after_header_is_honoured_then_succeeds(gateway_factory):
    gateway = gateway_factory((429, {"Retry-After": "2"}, QUEUE_FULL), ok({"kind": "voice_session", "session": {}}, 201))
    slept = []
    client = UbagClient(gateway.url, "s3cret", sleep=slept.append)
    assert client.create_voice_session("chatgpt_web")["kind"] == "voice_session"
    assert slept == [2.0]
    assert len(gateway.calls) == 2


def test_retry_after_ms_body_wins_over_header(gateway_factory):
    body = {"error": {**QUEUE_FULL["error"], "retry_after_ms": 1500}}
    gateway = gateway_factory((503, {"Retry-After": "9"}, body), ok({"kind": "voice_session", "session": {}}))
    slept = []
    UbagClient(gateway.url, "s", sleep=slept.append).create_voice_session("chatgpt_web")
    assert slept == [1.5]


def test_retries_are_bounded(gateway_factory):
    gateway = gateway_factory((429, {"Retry-After": "1"}, QUEUE_FULL))
    slept = []
    client = UbagClient(gateway.url, "s", max_retries=2, sleep=slept.append)
    with pytest.raises(UbagError) as caught:
        client.create_voice_session("chatgpt_web")
    assert len(gateway.calls) == 3 and slept == [1.0, 1.0]
    assert caught.value.status == 429
    assert caught.value.code == "UBAG-VOICE-QUEUE-FULL-004"
    assert caught.value.retry_after_ms == 1000


def test_long_retry_after_raises_without_sleeping(gateway_factory):
    gateway = gateway_factory((429, {"Retry-After": "600"}, QUEUE_FULL))
    slept = []
    client = UbagClient(gateway.url, "s", max_retry_wait=30, sleep=slept.append)
    with pytest.raises(UbagError) as caught:
        client.create_voice_session("chatgpt_web")
    assert slept == [] and len(gateway.calls) == 1
    assert caught.value.retry_after_ms == 600000


def test_other_errors_are_not_retried(gateway_factory):
    gateway = gateway_factory((409, {}, {"error": {"code": "UBAG-VOICE-SESSION-STATE-005", "category": "voice", "message": "expired", "retryable": False, "trace_id": "t"}}))
    slept = []
    with pytest.raises(UbagError) as caught:
        UbagClient(gateway.url, "s", sleep=slept.append).renew_voice_session("voice_1")
    assert caught.value.status == 409 and not caught.value.retryable
    assert slept == [] and len(gateway.calls) == 1


def test_openai_shaped_facade_error_parses(gateway_factory):
    gateway = gateway_factory((400, {}, {"error": {"message": "bad part", "type": "invalid_request_error", "code": "invalid_request"}}))
    with pytest.raises(UbagError) as caught:
        UbagClient(gateway.url, "s").chat_completion("chatgpt_web", [])
    assert caught.value.code == "invalid_request" and caught.value.message == "bad part"


def test_utterance_upload_and_poll(gateway_factory):
    gateway = gateway_factory(ok({"key": "utterance.wav"}), ok({"job_id": "job_1", "status": "completed"}))
    client = UbagClient(gateway.url, "s3cret")
    client.put_job_artifact("job_1", "utterance.wav", b"RIFF", "audio/wav")
    assert client.get_job("job_1")["status"] == "completed"
    put, get = gateway.calls
    assert (put["method"], put["path"], put["body"], put["content_type"]) == ("PUT", "/v1/jobs/job_1/artifacts/utterance.wav", b"RIFF", "audio/wav")
    assert (get["method"], get["path"]) == ("GET", "/v1/jobs/job_1")

from __future__ import annotations

import json
import math
import secrets
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass
from typing import Any, Callable, Dict, Iterable, Mapping, Optional, Tuple

from .errors import UbagApiError, UbagTransportError, is_ubag_error_envelope

UBAG_DEFAULT_API_VERSION = "2026-05-22"
UBAG_SDK_NAME = "ubag-python"
UBAG_SDK_VERSION = "0.0.0"

JSON_CONTENT_TYPE = "application/json"
CROCKFORD_BASE32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"


@dataclass
class UbagResponse:
    status: int
    headers: Mapping[str, str]
    body: bytes = b""
    reason: str = ""


UbagTransport = Callable[
    [str, str, Mapping[str, str], Optional[bytes], Optional[float]],
    UbagResponse,
]


class UbagClient:
    def __init__(
        self,
        base_url: str,
        *,
        app_secret: Optional[str] = None,
        api_version: str = UBAG_DEFAULT_API_VERSION,
        transport: Optional[UbagTransport] = None,
        headers: Optional[Mapping[str, str]] = None,
        timeout: Optional[float] = None,
    ) -> None:
        self.base_url = _normalize_base_url(base_url)
        self.app_secret = app_secret
        self.api_version = api_version
        self.transport = transport or _urllib_transport
        self.default_headers = dict(headers or {})
        self.timeout = timeout

    def health(self, **options: Any) -> Dict[str, Any]:
        return self._request("GET", "/v1/health", **options)

    def ready(self, **options: Any) -> Dict[str, Any]:
        return self._request("GET", "/v1/ready", **options)

    def version(self, **options: Any) -> Dict[str, Any]:
        options.pop("api_version", None)
        options.pop("idempotency_key", None)
        return self._request("GET", "/v1/version", **options)

    def create_job(self, request: Mapping[str, Any], **options: Any) -> Dict[str, Any]:
        body = _clone_json_object(request)
        api_version = body.get("api_version") or options.get("api_version") or self.api_version
        idempotency_key = (
            body.get("idempotency_key")
            or options.get("idempotency_key")
            or generate_idempotency_key()
        )

        body["api_version"] = api_version
        body["idempotency_key"] = idempotency_key
        client_metadata = body.setdefault("client", {})
        if isinstance(client_metadata, dict) and "sdk" not in client_metadata:
            client_metadata["sdk"] = {
                "name": UBAG_SDK_NAME,
                "version": UBAG_SDK_VERSION,
            }

        options = dict(options)
        options["api_version"] = api_version
        options["idempotency_key"] = idempotency_key
        options["body"] = body
        return self._request("POST", "/v1/jobs", **options)

    def get_job(self, job_id: str, **options: Any) -> Dict[str, Any]:
        return self._request("GET", "/v1/jobs/" + urllib.parse.quote(job_id, safe=""), **options)

    def list_jobs(
        self,
        *,
        cursor: Optional[str] = None,
        limit: Optional[int] = None,
        status: Optional[str] = None,
        target: Optional[str] = None,
        sort: Optional[str] = None,
        fields: Optional[Iterable[str]] = None,
        include: Optional[Iterable[str]] = None,
        **options: Any,
    ) -> Dict[str, Any]:
        query_items = []
        _add_optional_query(query_items, "cursor", cursor)
        _add_optional_query(query_items, "limit", limit)
        _add_optional_query(query_items, "filter[status]", status)
        _add_optional_query(query_items, "filter[target]", target)
        _add_optional_query(query_items, "sort", sort)
        _add_optional_query(query_items, "fields", ",".join(fields) if fields else None)
        _add_optional_query(query_items, "include", ",".join(include) if include else None)

        suffix = "?" + urllib.parse.urlencode(query_items) if query_items else ""
        return self._request("GET", "/v1/jobs" + suffix, **options)

    def list_workflows(self, **options: Any) -> Dict[str, Any]:
        return self._request("GET", "/v1/workflows", **options)

    def list_templates(self, **options: Any) -> Dict[str, Any]:
        return self._request("GET", "/v1/templates", **options)

    def list_targets(
        self,
        *,
        cursor: Optional[str] = None,
        limit: Optional[int] = None,
        **options: Any,
    ) -> Dict[str, Any]:
        return self._request("GET", "/v1/targets" + _build_list_query(cursor=cursor, limit=limit), **options)

    def list_adapters(
        self,
        *,
        cursor: Optional[str] = None,
        limit: Optional[int] = None,
        **options: Any,
    ) -> Dict[str, Any]:
        return self._request("GET", "/v1/adapters" + _build_list_query(cursor=cursor, limit=limit), **options)

    def list_apps(
        self,
        *,
        cursor: Optional[str] = None,
        limit: Optional[int] = None,
        **options: Any,
    ) -> Dict[str, Any]:
        return self._request("GET", "/v1/apps" + _build_list_query(cursor=cursor, limit=limit), **options)

    def list_devices(
        self,
        *,
        cursor: Optional[str] = None,
        limit: Optional[int] = None,
        **options: Any,
    ) -> Dict[str, Any]:
        return self._request("GET", "/v1/devices" + _build_list_query(cursor=cursor, limit=limit), **options)

    def list_audit_events(
        self,
        *,
        cursor: Optional[str] = None,
        limit: Optional[int] = None,
        **options: Any,
    ) -> Dict[str, Any]:
        return self._request("GET", "/v1/audit" + _build_list_query(cursor=cursor, limit=limit), **options)

    def list_webhooks(
        self,
        *,
        cursor: Optional[str] = None,
        limit: Optional[int] = None,
        **options: Any,
    ) -> Dict[str, Any]:
        return self._request("GET", "/v1/webhooks" + _build_list_query(cursor=cursor, limit=limit), **options)

    def list_events(
        self,
        *,
        cursor: Optional[str] = None,
        limit: Optional[int] = None,
        **options: Any,
    ) -> Dict[str, Any]:
        return self._request("GET", "/v1/events" + _build_list_query(cursor=cursor, limit=limit), **options)

    def list_job_events(
        self,
        job_id: str,
        *,
        cursor: Optional[str] = None,
        after_sequence: Optional[int] = None,
        limit: Optional[int] = None,
        **options: Any,
    ) -> Dict[str, Any]:
        query_items = []
        _add_optional_query(query_items, "cursor", cursor)
        _add_optional_query(query_items, "after_sequence", after_sequence)
        _add_optional_query(query_items, "limit", limit)
        suffix = "?" + urllib.parse.urlencode(query_items) if query_items else ""
        return self._request(
            "GET",
            "/v1/jobs/{}/events{}".format(urllib.parse.quote(job_id, safe=""), suffix),
            **options,
        )

    def list_job_artifacts(self, job_id: str, **options: Any) -> Dict[str, Any]:
        return self._request(
            "GET",
            "/v1/jobs/{}/artifacts".format(urllib.parse.quote(job_id, safe="")),
            **options,
        )

    def put_job_artifact(
        self,
        job_id: str,
        key: str,
        body: Any,
        *,
        content_type: str = "application/octet-stream",
        **options: Any,
    ) -> Dict[str, Any]:
        opts = dict(options)
        opts["idempotency_key"] = opts.get("idempotency_key") or generate_idempotency_key()
        if isinstance(body, bytes):
            body_bytes = body
        elif isinstance(body, bytearray):
            body_bytes = bytes(body)
        else:
            body_bytes = str(body).encode("utf-8")
        return self._request(
            "PUT",
            "/v1/jobs/{}/artifacts/{}".format(urllib.parse.quote(job_id, safe=""), urllib.parse.quote(key, safe="")),
            body_bytes=body_bytes,
            content_type=content_type,
            **opts,
        )

    def get_job_artifact(self, job_id: str, key: str, **options: Any) -> UbagResponse:
        return self._request(
            "GET",
            "/v1/jobs/{}/artifacts/{}".format(urllib.parse.quote(job_id, safe=""), urllib.parse.quote(key, safe="")),
            return_response=True,
            **options,
        )

    def delete_job_artifact(self, job_id: str, key: str, **options: Any) -> None:
        opts = dict(options)
        opts["idempotency_key"] = opts.get("idempotency_key") or generate_idempotency_key()
        self._request(
            "DELETE",
            "/v1/jobs/{}/artifacts/{}".format(urllib.parse.quote(job_id, safe=""), urllib.parse.quote(key, safe="")),
            **opts,
        )
        return None

    def replay_webhook_delivery(
        self,
        request: Optional[Mapping[str, Any]] = None,
        **options: Any,
    ) -> Dict[str, Any]:
        body = _clone_json_object(request or {})
        api_version = body.get("api_version") or options.get("api_version") or self.api_version
        idempotency_key = (
            body.get("idempotency_key")
            or options.get("idempotency_key")
            or generate_idempotency_key()
        )
        body["api_version"] = api_version
        body["idempotency_key"] = idempotency_key
        opts = dict(options)
        opts["api_version"] = api_version
        opts["idempotency_key"] = idempotency_key
        opts["body"] = body
        return self._request("POST", "/v1/webhooks/replay", **opts)

    def cache_status(self, **options: Any) -> Dict[str, Any]:
        return self._request("GET", "/v1/cache", **options)

    def get_metrics(self, **options: Any) -> str:
        opts = dict(options)
        headers = dict(opts.pop("headers", {}) or {})
        headers["Accept"] = "text/plain"
        response = self._request("GET", "/v1/metrics", headers=headers, return_response=True, **opts)
        return response.body.decode("utf-8")

    def stream_job_events_sse(self, job_id: str, **options: Any) -> UbagResponse:
        opts = dict(options)
        headers = dict(opts.pop("headers", {}) or {})
        headers["Accept"] = "text/event-stream"
        return self._request(
            "GET",
            "/v1/sse/jobs/{}".format(urllib.parse.quote(job_id, safe="")),
            headers=headers,
            return_response=True,
            **opts,
        )

    def stream_events_websocket(self, **options: Any) -> UbagResponse:
        opts = dict(options)
        headers = dict(opts.pop("headers", {}) or {})
        headers["Upgrade"] = "websocket"
        return self._request("GET", "/v1/stream", headers=headers, return_response=True, **opts)

    def cancel_job(
        self,
        job_id: str,
        request: Optional[Mapping[str, Any]] = None,
        **options: Any,
    ) -> Dict[str, Any]:
        return self._mutate_job(job_id, "cancel", request or {}, **options)

    def retry_job(
        self,
        job_id: str,
        request: Optional[Mapping[str, Any]] = None,
        **options: Any,
    ) -> Dict[str, Any]:
        return self._mutate_job(job_id, "retry", request or {}, **options)

    def _mutate_job(
        self,
        job_id: str,
        operation: str,
        request: Mapping[str, Any],
        **options: Any,
    ) -> Dict[str, Any]:
        body = _clone_json_object(request)
        api_version = body.get("api_version") or options.get("api_version") or self.api_version
        idempotency_key = (
            body.get("idempotency_key")
            or options.get("idempotency_key")
            or generate_idempotency_key()
        )

        body["api_version"] = api_version
        body["idempotency_key"] = idempotency_key

        options = dict(options)
        options["api_version"] = api_version
        options["idempotency_key"] = idempotency_key
        options["body"] = body
        path = "/v1/jobs/{}/{}".format(urllib.parse.quote(job_id, safe=""), operation)
        return self._request("POST", path, **options)

    def _request(
        self,
        method: str,
        path: str,
        *,
        api_version: Optional[str] = None,
        idempotency_key: Optional[str] = None,
        headers: Optional[Mapping[str, str]] = None,
        body: Any = None,
        body_bytes: Optional[bytes] = None,
        content_type: Optional[str] = None,
        return_response: bool = False,
        timeout: Optional[float] = None,
    ) -> Any:
        url = urllib.parse.urljoin(self.base_url, path)
        request_headers: Dict[str, str] = {
            "Accept": JSON_CONTENT_TYPE,
            "Ubag-Api-Version": api_version or self.api_version,
            "Ubag-Sdk-Name": UBAG_SDK_NAME,
            "Ubag-Sdk-Version": UBAG_SDK_VERSION,
        }
        request_headers.update(self.default_headers)
        request_headers.update(headers or {})

        if self.app_secret is not None and not _has_header(request_headers, "Authorization"):
            request_headers["Authorization"] = "Bearer " + self.app_secret

        if idempotency_key is not None:
            request_headers["Idempotency-Key"] = idempotency_key

        if body is not None and body_bytes is not None:
            raise ValueError("body and body_bytes cannot both be supplied")
        if body is not None:
            request_headers["Content-Type"] = JSON_CONTENT_TYPE
            body_bytes = json.dumps(body, separators=(",", ":")).encode("utf-8")
        elif body_bytes is not None:
            request_headers["Content-Type"] = content_type or "application/octet-stream"

        try:
            response = self.transport(
                method,
                url,
                request_headers,
                body_bytes,
                timeout if timeout is not None else self.timeout,
            )
        except UbagTransportError:
            raise
        except Exception as exc:
            raise UbagTransportError(
                "UBAG API request could not be sent.",
                url=url,
                method=method,
                cause=exc,
            ) from exc

        if response.status < 200 or response.status >= 300:
            raise _build_api_error(response, url, method)

        if return_response:
            return response
        if response.status == 204 or len(response.body) == 0:
            return None

        return json.loads(response.body.decode("utf-8"))


def create_ubag_client(*args: Any, **kwargs: Any) -> UbagClient:
    return UbagClient(*args, **kwargs)


def generate_idempotency_key(now_ms: Optional[int] = None) -> str:
    timestamp = int(time.time() * 1000 if now_ms is None else now_ms)
    return _encode_base32(timestamp, 10) + _encode_random_base32(10)


def _urllib_transport(
    method: str,
    url: str,
    headers: Mapping[str, str],
    body: Optional[bytes],
    timeout: Optional[float],
) -> UbagResponse:
    request = urllib.request.Request(url, data=body, headers=dict(headers), method=method)
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return UbagResponse(
                status=response.status,
                reason=response.reason,
                headers=dict(response.headers.items()),
                body=response.read(),
            )
    except urllib.error.HTTPError as exc:
        return UbagResponse(
            status=exc.code,
            reason=exc.reason,
            headers=dict(exc.headers.items()),
            body=exc.read(),
        )


def _build_api_error(response: UbagResponse, url: str, method: str) -> UbagApiError:
    text = response.body.decode("utf-8", errors="replace")
    body = _parse_json(text)
    envelope = body if is_ubag_error_envelope(body) else None
    return UbagApiError(
        status=response.status,
        status_text=response.reason,
        url=url,
        method=method,
        headers={key.lower(): value for key, value in response.headers.items()},
        envelope=envelope,
        body=body if body is not None else text,
    )


def _normalize_base_url(base_url: str) -> str:
    if not base_url:
        raise ValueError("base_url is required")
    return base_url.rstrip("/") + "/"


def _clone_json_object(value: Mapping[str, Any]) -> Dict[str, Any]:
    return json.loads(json.dumps(value))


def _parse_json(text: str) -> Any:
    if text.strip() == "":
        return None
    try:
        return json.loads(text)
    except json.JSONDecodeError:
        return None


def _add_optional_query(items: list[Tuple[str, Any]], key: str, value: Any) -> None:
    if value is not None and value != "":
        items.append((key, value))


def _build_list_query(*, cursor: Optional[str] = None, limit: Optional[int] = None) -> str:
    query_items = []
    _add_optional_query(query_items, "cursor", cursor)
    _add_optional_query(query_items, "limit", limit)
    return "?" + urllib.parse.urlencode(query_items) if query_items else ""


def _has_header(headers: Mapping[str, str], key: str) -> bool:
    wanted = key.lower()
    return any(existing.lower() == wanted for existing in headers)


def _encode_base32(value: int, length: int) -> str:
    output = ""
    remaining = max(0, int(value))
    for _ in range(length):
        output = CROCKFORD_BASE32[remaining % 32] + output
        remaining = math.floor(remaining / 32)
    return output


def _encode_random_base32(byte_length: int) -> str:
    output = ""
    buffer = 0
    bits = 0
    for byte in secrets.token_bytes(byte_length):
        buffer = (buffer << 8) | byte
        bits += 8
        while bits >= 5:
            output += CROCKFORD_BASE32[(buffer >> (bits - 5)) & 31]
            bits -= 5
    return output

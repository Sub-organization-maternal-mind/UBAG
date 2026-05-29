from __future__ import annotations

from datetime import datetime, timezone
from email.utils import parsedate_to_datetime
from typing import Any, Mapping, Optional


class UbagApiError(Exception):
    name = "UbagApiError"

    def __init__(
        self,
        *,
        status: int,
        status_text: str,
        url: str,
        method: str,
        headers: Mapping[str, str],
        envelope: Optional[Mapping[str, Any]] = None,
        body: Any = None,
    ) -> None:
        error = envelope.get("error") if isinstance(envelope, Mapping) else None
        message = (
            error.get("message")
            if isinstance(error, Mapping) and isinstance(error.get("message"), str)
            else "UBAG API request failed with HTTP {} {}".format(status, status_text)
        )
        super().__init__(message)
        self.status = status
        self.status_text = status_text
        self.url = url
        self.method = method
        self.headers = {key.lower(): value for key, value in headers.items()}
        self.envelope = envelope
        self.body = body

    @property
    def error(self) -> Optional[Mapping[str, Any]]:
        if not isinstance(self.envelope, Mapping):
            return None
        value = self.envelope.get("error")
        return value if isinstance(value, Mapping) else None

    @property
    def code(self) -> Optional[str]:
        value = self.error.get("code") if self.error else None
        return value if isinstance(value, str) else None

    @property
    def category(self) -> Optional[str]:
        value = self.error.get("category") if self.error else None
        return value if isinstance(value, str) else None

    @property
    def retryable(self) -> bool:
        value = self.error.get("retryable") if self.error else None
        return bool(value) if isinstance(value, bool) else False

    @property
    def retry_after_ms(self) -> Optional[int]:
        value = self.error.get("retry_after_ms") if self.error else None
        if isinstance(value, (int, float)):
            return max(0, int(value))

        retry_after = self.headers.get("retry-after")
        if retry_after is None:
            return None

        try:
            seconds = float(retry_after)
            return max(0, int(seconds * 1000))
        except ValueError:
            pass

        try:
            parsed = parsedate_to_datetime(retry_after)
            if parsed.tzinfo is None:
                parsed = parsed.replace(tzinfo=timezone.utc)
            delta = parsed - datetime.now(timezone.utc)
            return max(0, int(delta.total_seconds() * 1000))
        except (TypeError, ValueError):
            return None

    @property
    def trace_id(self) -> Optional[str]:
        if self.error and isinstance(self.error.get("trace_id"), str):
            return self.error["trace_id"]
        return self.headers.get("ubag-trace-id") or self.headers.get("x-request-id")


class UbagTransportError(Exception):
    name = "UbagTransportError"

    def __init__(
        self,
        message: str,
        *,
        url: str,
        method: str,
        cause: Optional[BaseException] = None,
    ) -> None:
        super().__init__(message)
        self.url = url
        self.method = method
        self.__cause__ = cause


def is_ubag_error_envelope(value: Any) -> bool:
    if not isinstance(value, Mapping):
        return False
    error = value.get("error")
    if not isinstance(error, Mapping):
        return False
    code = error.get("code")
    return (
        isinstance(code, str)
        and code.startswith("UBAG-")
        and isinstance(error.get("category"), str)
        and isinstance(error.get("message"), str)
        and isinstance(error.get("retryable"), bool)
        and isinstance(error.get("trace_id"), str)
    )

"""Credential provider for Antigravity SDK adapter.

Single-credential model: one API key, one project. No multi-account
quota rotation (Gemini API quotas are per-project, not per-key).
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from typing import Optional


@dataclass(frozen=True)
class CredentialConfig:
    api_key: str
    vertex: bool = False
    project: str = ""
    location: str = "us-central1"


class CredentialProvider:
    def __init__(self) -> None:
        self._api_key: Optional[str] = None
        self._vertex = False
        self._project = ""
        self._location = "us-central1"

    @classmethod
    def from_env(cls) -> "CredentialProvider":
        provider = cls()
        provider._api_key = os.environ.get("GEMINI_API_KEY", "")
        provider._vertex = os.environ.get("GOOGLE_GENAI_USE_VERTEXAI", "").strip().lower() in ("1", "true", "yes")
        provider._project = os.environ.get("GOOGLE_CLOUD_PROJECT", "")
        provider._location = os.environ.get("GOOGLE_CLOUD_LOCATION", "us-central1")
        return provider

    @property
    def is_configured(self) -> bool:
        return bool(self._api_key)

    @property
    def masked_key(self) -> str:
        if not self._api_key:
            return ""
        if len(self._api_key) <= 8:
            return "****"
        return self._api_key[:4] + "*" * (len(self._api_key) - 8) + self._api_key[-4:]

    def get_config(self) -> CredentialConfig:
        if not self._api_key:
            raise RuntimeError("GEMINI_API_KEY not configured")
        return CredentialConfig(
            api_key=self._api_key,
            vertex=self._vertex,
            project=self._project,
            location=self._location,
        )

    def to_dict(self) -> dict:
        return {
            "masked_key": self.masked_key,
            "vertex": self._vertex,
            "project": self._project,
            "location": self._location,
            "configured": self.is_configured,
        }

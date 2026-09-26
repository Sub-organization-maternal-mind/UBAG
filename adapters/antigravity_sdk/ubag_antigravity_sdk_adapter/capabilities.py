"""Provider capability registry and dynamic model discovery."""

from __future__ import annotations

import time
from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional


@dataclass
class ModelInfo:
    model_id: str
    display_name: str
    status: str = "enabled"
    capabilities: List[str] = field(default_factory=list)
    max_input_tokens: int = 0
    max_output_tokens: int = 0
    thinking_levels: List[str] = field(default_factory=list)

    def to_dict(self) -> Dict[str, Any]:
        return {
            "model_id": self.model_id,
            "display_name": self.display_name,
            "status": self.status,
            "capabilities": self.capabilities,
            "max_input_tokens": self.max_input_tokens,
            "max_output_tokens": self.max_output_tokens,
            "thinking_levels": self.thinking_levels,
        }


@dataclass
class ProviderCapabilities:
    provider_id: str
    auth_modes: List[str]
    default_model: str
    default_thinking: str
    thinking_levels: List[str]
    capabilities: List[str]
    models: List[ModelInfo] = field(default_factory=list)

    def to_dict(self) -> Dict[str, Any]:
        return {
            "provider_id": self.provider_id,
            "auth_modes": self.auth_modes,
            "default_model": self.default_model,
            "default_thinking": self.default_thinking,
            "thinking_levels": self.thinking_levels,
            "capabilities": self.capabilities,
            "models": [m.to_dict() for m in self.models],
        }


def get_sdk_capabilities() -> ProviderCapabilities:
    return ProviderCapabilities(
        provider_id="antigravity_sdk",
        auth_modes=["api_key", "vertex"],
        default_model="gemini-3.8-flash",
        default_thinking="high",
        thinking_levels=["low", "medium", "high"],
        capabilities=[
            "streaming",
            "tool_calls",
            "structured_output",
            "multimodal",
            "compaction",
            "budgets",
            "cancellation",
        ],
        models=[
            ModelInfo(
                model_id="gemini-3.8-flash",
                display_name="Gemini 3.8 Flash",
                status="enabled",
                capabilities=["streaming", "tool_calls", "structured_output", "multimodal"],
                max_input_tokens=1048576,
                max_output_tokens=65536,
                thinking_levels=["low", "medium", "high"],
            ),
            ModelInfo(
                model_id="gemini-3.7-flash",
                display_name="Gemini 3.7 Flash",
                status="enabled",
                capabilities=["streaming", "tool_calls", "structured_output"],
                max_input_tokens=1048576,
                max_output_tokens=65536,
                thinking_levels=["low", "medium", "high"],
            ),
            ModelInfo(
                model_id="gemini-3.6-flash",
                display_name="Gemini 3.6 Flash",
                status="enabled",
                capabilities=["streaming", "tool_calls", "structured_output"],
                max_input_tokens=1048576,
                max_output_tokens=65536,
                thinking_levels=["low", "medium", "high"],
            ),
            ModelInfo(
                model_id="gemini-3.5-flash-lite",
                display_name="Gemini 3.5 Flash Lite",
                status="enabled",
                capabilities=["streaming", "structured_output"],
                max_input_tokens=1024000,
                max_output_tokens=8192,
                thinking_levels=["low", "medium"],
            ),
        ],
    )


def get_cli_capabilities() -> ProviderCapabilities:
    return ProviderCapabilities(
        provider_id="antigravity_cli",
        auth_modes=["google_oauth"],
        default_model="gemini-3.8-flash",
        default_thinking="high",
        thinking_levels=["low", "medium", "high"],
        capabilities=[
            "streaming",
            "tool_calls",
            "structured_output",
            "json_schema",
        ],
        models=[
            ModelInfo(
                model_id="gemini-3.8-flash",
                display_name="Gemini 3.8 Flash",
                status="enabled",
                capabilities=["streaming", "tool_calls", "structured_output"],
                thinking_levels=["low", "medium", "high"],
            ),
        ],
    )


def get_provider_capabilities(provider_id: str) -> Optional[ProviderCapabilities]:
    if provider_id == "antigravity_sdk":
        return get_sdk_capabilities()
    if provider_id == "antigravity_cli":
        return get_cli_capabilities()
    return None


class ModelDiscovery:
    def __init__(self, cache_ttl_seconds: int = 3600) -> None:
        self._cache_ttl = cache_ttl_seconds
        self._cache: Dict[str, Any] = {}
        self._cache_at: float = 0.0

    def discover_models(self, api_key: str) -> List[ModelInfo]:
        now = time.monotonic()
        if self._cache and (now - self._cache_at) < self._cache_ttl:
            return self._cache.get("models", [])

        models = self._fetch_models(api_key)
        self._cache = {"models": models}
        self._cache_at = now
        return models

    def _fetch_models(self, api_key: str) -> List[ModelInfo]:
        return get_sdk_capabilities().models

    def discover_with_api(self, api_key: str) -> List[Dict[str, Any]]:
        models = self.discover_models(api_key)
        return [m.to_dict() for m in models]


__all__ = [
    "ModelInfo",
    "ProviderCapabilities",
    "get_sdk_capabilities",
    "get_cli_capabilities",
    "get_provider_capabilities",
    "ModelDiscovery",
]

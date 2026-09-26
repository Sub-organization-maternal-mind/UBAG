"""Tool policies and safety configuration for Antigravity SDK adapter."""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from typing import Dict, Set


@dataclass
class ToolPolicy:
    allowed_tools: Set[str] = field(default_factory=set)
    denied_tools: Set[str] = field(default_factory=set)
    default_mode: str = "deny"
    sandbox_enabled: bool = True
    max_tool_output_chars: int = 10000
    confirm_destructive: bool = True

    @classmethod
    def production_defaults(cls) -> "ToolPolicy":
        return cls(
            allowed_tools={
                "read_file", "list_directory", "search_files",
                "run_command", "write_file", "edit_file",
            },
            denied_tools={
                "delete_file", "move_file", "change_permissions",
                "install_package", "system_config", "network_request",
            },
            default_mode="deny",
            sandbox_enabled=True,
            max_tool_output_chars=10000,
            confirm_destructive=True,
        )

    @classmethod
    def read_only(cls) -> "ToolPolicy":
        return cls(
            allowed_tools={"read_file", "list_directory", "search_files"},
            denied_tools=set(),
            default_mode="deny",
            sandbox_enabled=True,
            max_tool_output_chars=10000,
            confirm_destructive=True,
        )

    def is_tool_allowed(self, tool_name: str) -> bool:
        if tool_name in self.denied_tools:
            return False
        if self.default_mode == "allow":
            return True
        return tool_name in self.allowed_tools

    def to_dict(self) -> Dict:
        return {
            "allowed_tools": sorted(self.allowed_tools),
            "denied_tools": sorted(self.denied_tools),
            "default_mode": self.default_mode,
            "sandbox_enabled": self.sandbox_enabled,
            "max_tool_output_chars": self.max_tool_output_chars,
            "confirm_destructive": self.confirm_destructive,
        }


@dataclass
class SafetyConfig:
    tool_policy: ToolPolicy = field(default_factory=ToolPolicy.production_defaults)
    max_concurrent_conversations: int = 3
    max_requests_per_minute: int = 60
    max_tokens_per_hour: int = 100000
    enable_audit_log: bool = True
    secrets_redaction_enabled: bool = True

    @classmethod
    def from_env(cls) -> "SafetyConfig":
        policy_mode = os.environ.get("UBAG_ANTIGRAVITY_TOOL_POLICY", "production")
        if policy_mode == "read_only":
            policy = ToolPolicy.read_only()
        else:
            policy = ToolPolicy.production_defaults()
        return cls(
            tool_policy=policy,
            max_concurrent_conversations=int(os.environ.get("UBAG_ANTIGRAVITY_MAX_CONCURRENT", "3")),
            max_requests_per_minute=int(os.environ.get("UBAG_ANTIGRAVITY_MAX_RPM", "60")),
            max_tokens_per_hour=int(os.environ.get("UBAG_ANTIGRAVITY_MAX_TPH", "100000")),
            enable_audit_log=os.environ.get("UBAG_ANTIGRAVITY_AUDIT_LOG", "true").lower() not in ("0", "false", "no"),
            secrets_redaction_enabled=True,
        )


__all__ = ["ToolPolicy", "SafetyConfig"]

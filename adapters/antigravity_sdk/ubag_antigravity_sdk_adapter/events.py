"""Canonical agent event schema for Antigravity SDK adapter.

Extends the basic worker event contract with tool calls, reasoning,
usage, compaction, structured output, and cancellation.
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from datetime import datetime, timedelta
from typing import Any, Dict, Mapping

JsonObject = Dict[str, Any]
BASE_CLOCK = datetime(2026, 1, 1, 0, 0, 0)


@dataclass(frozen=True)
class AgentEventContext:
    request_id: str
    provider: str
    model: str
    conversation_id: str = ""
    turn_id: str = ""
    step_id: str = ""
    trace_id: str = ""
    api_version: str = "2026-05-22"

    def to_dict(self) -> Dict[str, str]:
        return {
            "request_id": self.request_id,
            "provider": self.provider,
            "model": self.model,
            "conversation_id": self.conversation_id,
            "turn_id": self.turn_id,
            "step_id": self.step_id,
        }


@dataclass
class UsageMetrics:
    input_tokens: int = 0
    output_tokens: int = 0
    thinking_tokens: int = 0
    cached_tokens: int = 0
    total_tokens: int = 0

    def to_dict(self) -> Dict[str, int]:
        return {
            "input_tokens": self.input_tokens,
            "output_tokens": self.output_tokens,
            "thinking_tokens": self.thinking_tokens,
            "cached_tokens": self.cached_tokens,
            "total_tokens": self.total_tokens,
        }


@dataclass
class ToolCall:
    tool_call_id: str
    tool_name: str
    arguments: str = ""
    status: str = "pending"
    result: str = ""

    def to_dict(self) -> Dict[str, str]:
        return {
            "tool_call_id": self.tool_call_id,
            "tool_name": self.tool_name,
            "arguments": self.arguments,
            "status": self.status,
            "result": self.result,
        }


def make_event(
    ctx: AgentEventContext,
    sequence: int,
    event_type: str,
    data: Mapping[str, Any],
) -> JsonObject:
    body: JsonObject = {
        "api_version": ctx.api_version,
        "event_id": "evt_" + _digest("%s:%s" % (ctx.request_id, sequence))[:16],
        "job_id": ctx.request_id,
        "trace_id": ctx.trace_id or "trace_" + _digest(ctx.request_id)[:16],
        "type": event_type,
        "sequence": sequence,
        "created_at": _timestamp(sequence - 1),
        "data": dict(data),
    }
    body["data"].update(ctx.to_dict())
    return body


def make_agent_event(
    ctx: AgentEventContext,
    sequence: int,
    event_type: str,
    data: Mapping[str, Any],
    *,
    conversation_id: str = "",
    turn_id: str = "",
    step_id: str = "",
) -> JsonObject:
    full_ctx = AgentEventContext(
        request_id=ctx.request_id,
        provider=ctx.provider,
        model=ctx.model,
        conversation_id=conversation_id or ctx.conversation_id,
        turn_id=turn_id or ctx.turn_id,
        step_id=step_id or ctx.step_id,
        trace_id=ctx.trace_id,
        api_version=ctx.api_version,
    )
    body: JsonObject = {
        "api_version": full_ctx.api_version,
        "event_id": "evt_" + _digest("%s:%s" % (ctx.request_id, sequence))[:16],
        "job_id": ctx.request_id,
        "trace_id": full_ctx.trace_id or "trace_" + _digest(ctx.request_id)[:16],
        "type": event_type,
        "sequence": sequence,
        "created_at": _timestamp(sequence - 1),
        "data": dict(data),
    }
    body["data"].update(full_ctx.to_dict())
    return body


def make_text_delta_event(
    ctx: AgentEventContext,
    sequence: int,
    text: str,
    *,
    conversation_id: str = "",
    turn_id: str = "",
) -> JsonObject:
    return make_agent_event(
        ctx, sequence, "text_delta",
        {"delta": {"text": text}},
        conversation_id=conversation_id,
        turn_id=turn_id,
    )


def make_tool_call_event(
    ctx: AgentEventContext,
    sequence: int,
    tool_call: ToolCall,
    *,
    conversation_id: str = "",
    step_id: str = "",
) -> JsonObject:
    return make_agent_event(
        ctx, sequence, "tool_call",
        tool_call.to_dict(),
        conversation_id=conversation_id,
        step_id=step_id,
    )


def make_usage_event(
    ctx: AgentEventContext,
    sequence: int,
    usage: UsageMetrics,
    *,
    conversation_id: str = "",
    turn_id: str = "",
) -> JsonObject:
    return make_agent_event(
        ctx, sequence, "usage_update",
        {"usage": usage.to_dict()},
        conversation_id=conversation_id,
        turn_id=turn_id,
    )


def make_structured_output_event(
    ctx: AgentEventContext,
    sequence: int,
    output: Any,
    *,
    conversation_id: str = "",
) -> JsonObject:
    return make_agent_event(
        ctx, sequence, "structured_output",
        {"output": output},
        conversation_id=conversation_id,
    )


def make_cancellation_event(
    ctx: AgentEventContext,
    sequence: int,
    *,
    conversation_id: str = "",
) -> JsonObject:
    return make_agent_event(
        ctx, sequence, "cancelled",
        {"reason": "client_disconnect"},
        conversation_id=conversation_id,
    )


def make_stop_reason_event(
    ctx: AgentEventContext,
    sequence: int,
    stop_reason: str,
    *,
    conversation_id: str = "",
) -> JsonObject:
    return make_agent_event(
        ctx, sequence, "stop_reason",
        {"stop_reason": stop_reason},
        conversation_id=conversation_id,
    )


def _timestamp(sequence: int) -> str:
    return (BASE_CLOCK + timedelta(milliseconds=250 * sequence)).isoformat(
        timespec="milliseconds"
    ) + "Z"


def _digest(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def _canonical_json(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True)


__all__ = [
    "AgentEventContext",
    "UsageMetrics",
    "ToolCall",
    "make_event",
    "make_agent_event",
    "make_text_delta_event",
    "make_tool_call_event",
    "make_usage_event",
    "make_structured_output_event",
    "make_cancellation_event",
    "make_stop_reason_event",
]

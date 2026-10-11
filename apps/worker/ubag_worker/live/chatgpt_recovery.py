"""Validate a saved final answer observed during normal browser navigation."""

from __future__ import annotations

import hashlib
from typing import Any, Mapping, Optional


def completed_answer(snapshot: Any, *, thread_id: str, prompt_digest: str) -> Optional[str]:
    """Fail closed on another turn, incomplete output, or non-final content."""
    if not isinstance(snapshot, Mapping) or snapshot.get("conversation_id") != thread_id:
        return None
    messages = snapshot.get("messages")
    if not isinstance(messages, list):
        return None
    user_index = None
    for index, message in enumerate(messages):
        if not isinstance(message, Mapping):
            return None
        author = message.get("author")
        if isinstance(author, Mapping) and author.get("role") == "user":
            user_index = index
    if user_index is None:
        return None
    content = messages[user_index].get("content")
    if not isinstance(content, Mapping) or content.get("content_type") != "text":
        return None
    parts = content.get("parts")
    if not isinstance(parts, list) or not all(isinstance(part, str) for part in parts):
        return None
    if hashlib.sha256("".join(parts).encode("utf-8")).hexdigest() != prompt_digest:
        return None
    for message in reversed(messages[user_index + 1:]):
        author = message.get("author")
        if not isinstance(author, Mapping) or author.get("role") != "assistant":
            continue
        if message.get("channel") != "final":
            continue  # never extract reasoning, tool input, or a reasoning recap
        metadata = message.get("metadata")
        content = message.get("content")
        if (
            message.get("status") != "finished_successfully"
            or message.get("end_turn") is not True
            or not isinstance(metadata, Mapping)
            or metadata.get("is_complete") is not True
            or not isinstance(content, Mapping)
            or content.get("content_type") != "text"
        ):
            return None
        parts = content.get("parts")
        if not isinstance(parts, list) or not all(isinstance(part, str) for part in parts):
            return None
        answer = "".join(parts)
        return answer if answer.strip() and len(answer) <= 131072 else None
    return None

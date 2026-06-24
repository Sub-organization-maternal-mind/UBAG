"""JSONL runner for UBAG's safe-mode worker adapter registry."""

from __future__ import annotations

import json
from typing import Any, Dict, List, Mapping, TextIO

from .adapter_registry import events_for_payload as _events_for_payload

JsonObject = Dict[str, Any]


def load_payload_from_text(text: str) -> JsonObject:
    payload = json.loads(text)
    if not isinstance(payload, dict):
        raise ValueError("job payload must be a JSON object")
    return payload


def events_for_payload(payload: Mapping[str, Any]) -> List[JsonObject]:
    return _events_for_payload(payload)


def emit_jsonl(payload: Mapping[str, Any], stream: TextIO) -> int:
    count = 0
    for event in events_for_payload(payload):
        stream.write(json.dumps(event, sort_keys=True, separators=(",", ":"), ensure_ascii=True))
        stream.write("\n")
        count += 1
    stream.flush()
    return count

//! SSE chunk parsing and terminal-event detection.

use serde::Deserialize;

#[derive(Debug, Deserialize, PartialEq)]
pub struct SseEvent {
    #[serde(rename = "type")]
    pub event_type: String,
    #[serde(default)]
    pub sequence: i64,
}

const TERMINAL: &[&str] = &["completed", "failed", "cancelled", "dead_letter"];

pub fn is_terminal_event(event_type: &str) -> bool {
    TERMINAL.contains(&event_type)
}

/// Parses JSON events from SSE `data:` lines in a chunk. Malformed frames skip.
pub fn parse_sse_chunk(chunk: &str) -> Vec<SseEvent> {
    let mut events = Vec::new();
    for block in chunk.split("\n\n") {
        for line in block.split('\n') {
            if let Some(rest) = line.strip_prefix("data:") {
                let payload = rest.trim();
                if payload.is_empty() {
                    continue;
                }
                if let Ok(ev) = serde_json::from_str::<SseEvent>(payload) {
                    events.push(ev);
                }
            }
        }
    }
    events
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_multiple_events() {
        let chunk = "data: {\"type\":\"token\",\"sequence\":1}\n\ndata: {\"type\":\"completed\",\"sequence\":2}\n\n";
        let events = parse_sse_chunk(chunk);
        assert_eq!(events.len(), 2);
        assert_eq!(events[0].event_type, "token");
    }

    #[test]
    fn detects_terminal() {
        assert!(is_terminal_event("completed"));
        assert!(!is_terminal_event("token"));
    }
}

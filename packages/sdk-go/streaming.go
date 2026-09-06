package ubag

import (
	"encoding/json"
	"strings"
)

type SSEEvent struct {
	Type     string         `json:"type"`
	Sequence int            `json:"sequence"`
	Data     map[string]any `json:"data,omitempty"`
}

// terminalTypes is derived from the generated manifest's terminal job statuses:
// the contract's terminal job statuses double as terminal event types.
var terminalTypes = func() map[string]bool {
	m := make(map[string]bool, len(UbagJobStatuses))
	for status, meta := range UbagJobStatuses {
		if meta.Terminal {
			m[status] = true
		}
	}
	return m
}()

func IsTerminalEvent(eventType string) bool { return terminalTypes[eventType] }

// ParseSSEChunk extracts JSON events from SSE `data:` lines in a chunk.
func ParseSSEChunk(chunk string) ([]SSEEvent, error) {
	var events []SSEEvent
	for _, block := range strings.Split(chunk, "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" {
				continue
			}
			var ev SSEEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue // skip malformed frame
			}
			events = append(events, ev)
		}
	}
	return events, nil
}

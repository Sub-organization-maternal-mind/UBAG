package ubag

import "testing"

func TestParseSSEChunk(t *testing.T) {
	chunk := "data: {\"type\":\"token\",\"sequence\":1}\n\ndata: {\"type\":\"completed\",\"sequence\":2}\n\n"
	events, err := ParseSSEChunk(chunk)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != "token" {
		t.Fatalf("expected token, got %s", events[0].Type)
	}
}

func TestIsTerminalEvent(t *testing.T) {
	// Contract terminal job statuses double as terminal event types.
	terminal := []string{
		"completed", "completed_with_warnings", "failed_retryable",
		"failed_terminal", "dead_letter", "cancelled", "timed_out",
	}
	for _, typ := range terminal {
		if !IsTerminalEvent(typ) {
			t.Fatalf("expected %s to be terminal", typ)
		}
	}
	nonTerminal := []string{"token", "running", "created"}
	for _, typ := range nonTerminal {
		if IsTerminalEvent(typ) {
			t.Fatalf("expected %s to NOT be terminal", typ)
		}
	}
	if IsTerminalEvent("failed") {
		t.Fatal(`"failed" is not a contract event type; the contract emits failed_retryable/failed_terminal`)
	}
}

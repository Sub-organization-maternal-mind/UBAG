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
	if !IsTerminalEvent("completed") || !IsTerminalEvent("dead_letter") {
		t.Fatal("expected terminal types to be recognised")
	}
	if IsTerminalEvent("token") {
		t.Fatal("token must not be terminal")
	}
}

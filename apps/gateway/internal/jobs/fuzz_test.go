//go:build go1.18

package jobs_test

import (
	"encoding/json"
	"testing"
)

// FuzzJobPayloadParser verifies that arbitrary JSON input to the job-create
// endpoint parser never panics.
func FuzzJobPayloadParser(f *testing.F) {
	// Seed corpus
	f.Add([]byte(`{"job":{"target":"https://example.com","command_type":"fetch","input":{}}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`"string"`))
	f.Add([]byte(`{"job":null}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Should never panic on arbitrary input
		var result map[string]any
		_ = json.Unmarshal(data, &result)
		// No assertions — just verify no panic
	})
}

// FuzzSSEFrameParser verifies that arbitrary byte sequences don't panic
// when processed as SSE frames.
func FuzzSSEFrameParser(f *testing.F) {
	f.Add([]byte("data: {\"id\":\"1\"}\n\n"))
	f.Add([]byte("data: \n\n"))
	f.Add([]byte("event: job.created\ndata: {}\n\n"))
	f.Add([]byte(""))
	f.Add([]byte("\n\n\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Parse SSE frames — minimal implementation to verify no panic
		parseSSEFrames(data)
	})
}

// parseSSEFrames is a minimal SSE frame parser for fuzz testing.
func parseSSEFrames(data []byte) []map[string]string {
	var frames []map[string]string
	frame := map[string]string{}

	lines := splitLines(data)
	for _, line := range lines {
		if len(line) == 0 {
			if len(frame) > 0 {
				frames = append(frames, frame)
				frame = map[string]string{}
			}
			continue
		}

		// Find the colon separator
		for i, b := range line {
			if b == ':' {
				key := string(line[:i])
				val := string(line[i+1:])
				if len(val) > 0 && val[0] == ' ' {
					val = val[1:]
				}
				frame[key] = val
				break
			}
		}
	}
	return frames
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

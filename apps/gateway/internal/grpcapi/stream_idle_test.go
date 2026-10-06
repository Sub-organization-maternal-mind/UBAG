package grpcapi

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	ubagv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/v1"
)

// A stream that starts after the job's terminal event (cursor past it) never
// receives another event; the idle check closes it instead of leaking.
func TestStreamJobEventsIdleClosesOnAlreadyTerminalJob(t *testing.T) {
	old := streamIdleCheck
	streamIdleCheck = 100 * time.Millisecond
	t.Cleanup(func() { streamIdleCheck = old })

	client, store := newTestClientWithStore(t)
	ctx, cancel := context.WithTimeout(authContext(context.Background(), testSecret), 10*time.Second)
	defer cancel()
	created := createStreamTestJob(t, client, ctx, "idem-key-stream-0099")
	applyStreamTestEvent(t, store, created.GetJobId(), "stream_idle_done", "completed", 2, completedStreamData())

	stream, err := client.StreamJobEvents(ctx, &ubagv1.ListJobEventsRequest{JobId: created.GetJobId(), AfterSequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("idle stream on a terminal job returned %v, want io.EOF", err)
	}
}

// blocked and failed(retryable) are terminal in the store but carry no
// terminal data.status; the stream must close promptly, not after the idle check.
func TestStreamJobEventsClosesPromptlyOnBlockedAndRetryableFailure(t *testing.T) {
	old := streamIdleCheck
	streamIdleCheck = time.Hour
	t.Cleanup(func() { streamIdleCheck = old })

	for i, tc := range []struct {
		eventType string
		data      map[string]any
	}{
		{"blocked", map[string]any{"status": "blocked"}},
		{"failed", map[string]any{"retryable": true}},
	} {
		client, store := newTestClientWithStore(t)
		ctx, cancel := context.WithTimeout(authContext(context.Background(), testSecret), 5*time.Second)
		created := createStreamTestJob(t, client, ctx, "idem-key-stream-blk"+string(rune('0'+i))+"-0001")
		applyStreamTestEvent(t, store, created.GetJobId(), "stream_blk_end", tc.eventType, 2, tc.data)
		stream, err := client.StreamJobEvents(ctx, &ubagv1.ListJobEventsRequest{JobId: created.GetJobId()})
		if err != nil {
			t.Fatal(err)
		}
		types, err := recvTypes(t, stream)
		cancel()
		if !errors.Is(err, io.EOF) || len(types) != 2 || types[1] != tc.eventType {
			t.Fatalf("%s: types=%v err=%v, want [queued %s] then io.EOF", tc.eventType, types, err, tc.eventType)
		}
	}
}

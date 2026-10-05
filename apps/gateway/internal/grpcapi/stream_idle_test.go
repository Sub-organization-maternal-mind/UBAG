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

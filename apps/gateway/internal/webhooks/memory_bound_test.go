package webhooks

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestMemoryStoreBoundedTerminalDeliveries verifies the in-memory delivery log
// evicts oldest TERMINAL deliveries past the bound while never evicting
// pending deliveries (dropping one would silently lose a callback).
func TestMemoryStoreBoundedTerminalDeliveries(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	const total = defaultMemoryMaxEntries + 50
	delivered := 0
	for index := 0; index < total; index++ {
		request := EnqueueRequest{
			TenantID:  "t1",
			AppID:     "a1",
			JobID:     fmt.Sprintf("job_%012d", index),
			EventName: "job.completed",
			URL:       "https://hooks.example.test/callback",
			SecretID:  "sec",
			DedupeKey: fmt.Sprintf("dedupe-%d", index),
			Payload:   []byte(`{}`),
		}
		delivery, enqueued, err := store.Enqueue(ctx, request)
		if err != nil || !enqueued {
			t.Fatalf("enqueue %d: enqueued=%v err=%v", index, enqueued, err)
		}
		// Deliver every 3rd delivery so terminal entries accumulate past the
		// bound while pending entries stay in the store.
		if index%3 != 0 {
			continue
		}
		leases, err := store.LeaseDue(ctx, "worker-1", 10, time.Minute)
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		var leaseID string
		for _, lease := range leases {
			if lease.ID == delivery.ID {
				leaseID = lease.LeaseID
				break
			}
		}
		if leaseID == "" {
			t.Fatalf("delivery %s was not leased", delivery.ID)
		}
		if err := store.MarkDelivered(ctx, delivery.ID, leaseID, AttemptResult{StatusCode: 200}); err != nil {
			t.Fatalf("mark delivered: %v", err)
		}
		delivered++
	}

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	totalRemaining := 0
	for _, count := range stats.DepthByState {
		totalRemaining += count
	}
	if totalRemaining > defaultMemoryMaxEntries {
		t.Fatalf("store not bounded: %d entries > %d", totalRemaining, defaultMemoryMaxEntries)
	}
	if got := stats.DepthByState[string(StatusDelivered)]; got >= delivered {
		t.Fatalf("no terminal deliveries were evicted: %d remain of %d delivered", got, delivered)
	}
}

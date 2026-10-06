package jobs

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Store.ApplyWorkerEvents: one atomic batch per job, one waiter wake per batch.
// The contract runs on memory and sqlite here and on Postgres when
// UBAG_TEST_POSTGRES_DSN is set (pnpm test:gateway:postgres).

func batchEvent(job Job, id, eventType string, sequence int, data map[string]any) WorkerEvent {
	return WorkerEvent{EventID: id, JobID: job.ID, APIVersion: job.APIVersion, Type: eventType,
		Sequence: sequence, TraceID: job.TraceID, Data: data}
}

func storedTypes(t *testing.T, store Store, jobID string) string {
	t.Helper()
	events, found, err := store.ListEvents(context.Background(), jobID, 0, 1000)
	if err != nil || !found {
		t.Fatalf("ListEvents found=%v err=%v", found, err)
	}
	types := make([]string, len(events))
	for i, event := range events {
		types[i] = event.Type
	}
	return strings.Join(types, ",")
}

func applyWorkerEventsContract(t *testing.T, store Store, tenantID string) {
	t.Helper()
	ctx := context.Background()

	t.Run("applies a batch in order with one result", func(t *testing.T) {
		job := waitEventsTestJob(t, store, tenantID)
		got, found, err := store.ApplyWorkerEvents(ctx, []WorkerEvent{
			batchEvent(job, "b1_"+job.ID, "running", 2, map[string]any{"status": "running"}),
			batchEvent(job, "b2_"+job.ID, "token", 3, tokenData("a")),
			batchEvent(job, "b3_"+job.ID, "token", 4, tokenData("b")),
		})
		if err != nil || !found {
			t.Fatalf("ApplyWorkerEvents found=%v err=%v", found, err)
		}
		if got.Status != StatusTokenStreaming {
			t.Fatalf("returned status = %s, want token_streaming", got.Status)
		}
		if types := storedTypes(t, store, job.ID); types != "queued,running,token,token" {
			t.Fatalf("history = %s", types)
		}
		stored, _, _ := store.Get(ctx, job.ID)
		if stored.Status != StatusTokenStreaming {
			t.Fatalf("stored status = %s", stored.Status)
		}
	})

	t.Run("duplicates and replays are no-ops", func(t *testing.T) {
		job := waitEventsTestJob(t, store, tenantID)
		batch := []WorkerEvent{
			batchEvent(job, "d1_"+job.ID, "token", 2, tokenData("a")),
			batchEvent(job, "d1_"+job.ID, "token", 2, tokenData("a")), // duplicate inside the batch
			batchEvent(job, "d2_"+job.ID, "token", 3, tokenData("b")),
		}
		if _, found, err := store.ApplyWorkerEvents(ctx, batch); err != nil || !found {
			t.Fatalf("first apply found=%v err=%v", found, err)
		}
		if _, found, err := store.ApplyWorkerEvents(ctx, batch); err != nil || !found { // replay
			t.Fatalf("replay found=%v err=%v", found, err)
		}
		if _, _, err := store.ApplyWorkerEvent(ctx, batch[0]); err != nil { // single-event replay
			t.Fatal(err)
		}
		if types := storedTypes(t, store, job.ID); types != "queued,token,token" {
			t.Fatalf("history = %s, want each event once", types)
		}
	})

	t.Run("out-of-order sequences land in arrival order", func(t *testing.T) {
		job := waitEventsTestJob(t, store, tenantID)
		if _, _, err := store.ApplyWorkerEvents(ctx, []WorkerEvent{
			batchEvent(job, "o5_"+job.ID, "token", 5, tokenData("late")),
			batchEvent(job, "o3_"+job.ID, "token", 3, tokenData("early")),
		}); err != nil {
			t.Fatal(err)
		}
		events, _, _ := store.ListEvents(ctx, job.ID, 0, 10)
		if len(events) != 3 || events[1].Sequence != 2 || events[2].Sequence != 3 {
			t.Fatalf("events = %#v, want the store's own gapless sequence in arrival order", events)
		}
	})

	t.Run("a failing event rolls the whole batch back", func(t *testing.T) {
		job := waitEventsTestJob(t, store, tenantID)
		good := batchEvent(job, "r1_"+job.ID, "token", 2, tokenData("a"))
		bad := batchEvent(job, "r2_"+job.ID, "token", 3, tokenData("b"))
		bad.TraceID = "trace_of_another_job"
		if _, _, err := store.ApplyWorkerEvents(ctx, []WorkerEvent{good, bad}); err == nil {
			t.Fatal("a trace mismatch must fail the batch")
		}
		if types := storedTypes(t, store, job.ID); types != "queued" {
			t.Fatalf("history = %s, want nothing from the failed batch", types)
		}
		// The good event's dedupe key was rolled back too: it still applies.
		if _, _, err := store.ApplyWorkerEvents(ctx, []WorkerEvent{good}); err != nil {
			t.Fatal(err)
		}
		if types := storedTypes(t, store, job.ID); types != "queued,token" {
			t.Fatalf("history = %s, want the retried event applied once", types)
		}
	})

	t.Run("a terminal in the batch finishes the job; later events are dropped", func(t *testing.T) {
		job := waitEventsTestJob(t, store, tenantID)
		got, _, err := store.ApplyWorkerEvents(ctx, []WorkerEvent{
			batchEvent(job, "t1_"+job.ID, "token", 2, tokenData("a")),
			batchEvent(job, "t2_"+job.ID, "completed", 3, map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "a"}}),
			batchEvent(job, "t3_"+job.ID, "token", 4, tokenData("after")),
		})
		if err != nil || got.Status != StatusCompleted {
			t.Fatalf("status = %s err = %v, want completed", got.Status, err)
		}
		if types := storedTypes(t, store, job.ID); types != "queued,token,completed" {
			t.Fatalf("history = %s", types)
		}
	})

	t.Run("shape errors and a missing job", func(t *testing.T) {
		job := waitEventsTestJob(t, store, tenantID)
		other := waitEventsTestJob(t, store, tenantID)
		cases := map[string][]WorkerEvent{
			"empty":      nil,
			"mixed jobs": {batchEvent(job, "m1_"+job.ID, "token", 2, tokenData("a")), batchEvent(other, "m2_"+other.ID, "token", 2, tokenData("a"))},
			"no type":    {batchEvent(job, "n1_"+job.ID, "", 2, nil)},
			"too many":   make([]WorkerEvent, MaxAttemptCommitEvents+1),
		}
		for name, batch := range cases {
			if _, found, err := store.ApplyWorkerEvents(ctx, batch); err == nil || found {
				t.Errorf("%s: found=%v err=%v, want an error", name, found, err)
			}
		}
		ghost := job
		ghost.ID = "job_does_not_exist"
		if _, found, err := store.ApplyWorkerEvents(ctx, []WorkerEvent{batchEvent(ghost, "g1", "token", 2, tokenData("a"))}); err != nil || found {
			t.Errorf("missing job: found=%v err=%v, want found=false and no error", found, err)
		}
		if types := storedTypes(t, store, job.ID); types != "queued" {
			t.Errorf("history = %s, want the rejected batches to leave no trace", types)
		}
	})
}

func TestMemoryStoreApplyWorkerEventsContract(t *testing.T) {
	applyWorkerEventsContract(t, NewMemoryStore(), "tenant_batch_memory")
}

func TestSQLiteStoreApplyWorkerEventsContract(t *testing.T) {
	applyWorkerEventsContract(t, newScheduledTestSQLiteStore(t), "tenant_batch_sqlite")
}

func TestPostgresStoreApplyWorkerEventsContract(t *testing.T) {
	dsn := os.Getenv("UBAG_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("UBAG_TEST_POSTGRES_DSN is not set")
	}
	db := openPostgresTestDB(t, dsn)
	defer db.Close()
	applyPostgresGatewayMigration(t, db)

	tenantID := "tenant_pg_batch_" + time.Now().UTC().Format("20060102150405")
	defer cleanupPostgresJobs(t, db, tenantID)
	applyWorkerEventsContract(t, NewPostgresStore(db), tenantID)
}

// With a one-hour fallback a waiter can only return promptly if the batch
// notifies; a batch that changes nothing (all duplicates) must not wake anyone.
func TestSQLiteStoreApplyWorkerEventsNotifiesOncePerBatch(t *testing.T) {
	store := newScheduledTestSQLiteStore(t)
	store.EnableEventNotify(time.Hour)
	job := waitEventsTestJob(t, store, "tenant_batch_notify")
	wake, cancelWake, ok := store.SubscribeJobWake(job.ID)
	if !ok {
		t.Fatal("hub is on, SubscribeJobWake must succeed")
	}
	defer cancelWake()

	batch := []WorkerEvent{
		batchEvent(job, "nt1", "token", 2, tokenData("a")),
		batchEvent(job, "nt2", "token", 3, tokenData("b")),
		batchEvent(job, "nt3", "token", 4, tokenData("c")),
	}
	if _, _, err := store.ApplyWorkerEvents(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	case <-time.After(2 * time.Second):
		t.Fatal("the batch did not wake the waiter")
	}
	select {
	case <-wake:
		t.Fatal("a second wake arrived for one batch")
	case <-time.After(100 * time.Millisecond):
	}

	if _, _, err := store.ApplyWorkerEvents(context.Background(), batch); err != nil { // replay: nothing changed
		t.Fatal(err)
	}
	select {
	case <-wake:
		t.Fatal("a replayed batch changed nothing and must not wake waiters")
	case <-time.After(100 * time.Millisecond):
	}

	// And a waiter parked on WaitEvents is released by the batch alone.
	next := waitEventsTestJob(t, store, "tenant_batch_notify")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result := startWait(ctx, store, next.ID, 1, 10)
	time.Sleep(100 * time.Millisecond)
	if _, _, err := store.ApplyWorkerEvents(ctx, []WorkerEvent{
		batchEvent(next, "nw1", "token", 2, tokenData("a")),
		batchEvent(next, "nw2", "token", 3, tokenData("b")),
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-result:
		if r.err != nil || len(r.events) != 2 {
			t.Fatalf("WaitEvents = %#v, want both batched events in one read", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitEvents was not released by the batch")
	}
}

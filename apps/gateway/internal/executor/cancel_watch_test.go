package executor

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/sqlitestore"
	_ "modernc.org/sqlite"
)

// With the event hub on, a cancel wakes the cancel-watch immediately even though
// the safety-net Get cadence is 2 s.
func TestCancelWatchWakesFromHub(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "cw.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlitestore.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	store := jobstore.NewSQLiteStore(db)
	store.EnableEventNotify(time.Hour)
	job, envelope := queuedJob(t, store, "cancel_watch_hub_0001")

	entered, cancelled := make(chan struct{}), make(chan struct{})
	consumer := &WorkerConsumer{
		Queue: fakeWorkerQueue{lease: &fakeWorkerLease{jobID: job.ID, leaseID: "a", envelope: envelope}},
		Jobs:  store,
		Runner: WorkerRunFunc(func(ctx context.Context, env DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			close(entered)
			select {
			case <-ctx.Done():
				close(cancelled)
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
				return completedEvents(env), nil
			}
		}),
	}
	go func() { _, _ = consumer.RunOnce(context.Background()) }()
	<-entered
	start := time.Now()
	if _, _, err := store.UpdateStatus(t.Context(), job.ID, jobstore.StatusCanceled); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
		if d := time.Since(start); d > 500*time.Millisecond {
			t.Fatalf("cancel took %v, want hub wake well under the 2s fallback", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker context not cancelled")
	}
}

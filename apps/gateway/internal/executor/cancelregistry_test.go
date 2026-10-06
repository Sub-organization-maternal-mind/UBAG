package executor

import (
	"context"
	"errors"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

func TestCancelRegistryHintsOnlyTheWatchersOfThatJob(t *testing.T) {
	reg := NewCancelRegistry()
	a1, release1 := reg.Watch("job_a")
	a2, release2 := reg.Watch("job_a")
	b, releaseB := reg.Watch("job_b")
	defer releaseB()

	if !reg.Hint("job_a") {
		t.Fatal("Hint reported no watcher")
	}
	for name, ch := range map[string]<-chan struct{}{"first": a1, "second": a2} {
		select {
		case <-ch:
		default:
			t.Fatalf("the %s watcher of job_a was not woken", name)
		}
	}
	select {
	case <-b:
		t.Fatal("a hint for job_a woke the watcher of job_b")
	default:
	}

	// A hint is a wake-up, not a counter: a pending one absorbs the next and never blocks the sender.
	reg.Hint("job_a")
	reg.Hint("job_a")
	<-a1
	select {
	case <-a1:
		t.Fatal("two pending hints queued up")
	default:
	}

	release1()
	release2()
	release2() // idempotent
	if reg.Hint("job_a") {
		t.Fatal("a released watcher was still hinted")
	}
	if reg.Watching() != 1 {
		t.Fatalf("Watching = %d, want only job_b", reg.Watching())
	}
}

func TestCancelRegistryIsNilSafe(t *testing.T) {
	var reg *CancelRegistry
	ch, release := reg.Watch("job")
	release()
	if reg.Hint("job") || reg.Watching() != 0 {
		t.Fatal("a nil registry reported activity")
	}
	select {
	case <-ch:
		t.Fatal("a nil registry woke a watcher")
	default:
	}
	if d := NewNoopDispatcher(); NewCancelNotifier(d, nil) != Dispatcher(d) {
		t.Fatal("a nil registry must leave the dispatcher unwrapped")
	}
}

type failingCancelDispatcher struct{ NoopDispatcher }

func (failingCancelDispatcher) CancelJob(context.Context, jobstore.Job, string) error {
	return errors.New("queue unavailable")
}

// The hint follows a cancel that succeeded: a failed CancelJob leaves the job
// running (the API answers 503), so it must not stop anything.
func TestCancelNotifierHintsOnlyAfterTheCancelSucceeded(t *testing.T) {
	reg := NewCancelRegistry()
	ch, release := reg.Watch("job_1")
	defer release()

	failing := NewCancelNotifier(failingCancelDispatcher{}, reg)
	if err := failing.CancelJob(t.Context(), jobstore.Job{ID: "job_1"}, "x"); err == nil {
		t.Fatal("the error was swallowed")
	}
	select {
	case <-ch:
		t.Fatal("a failed cancel was hinted")
	default:
	}

	working := NewCancelNotifier(NewNoopDispatcher(), reg)
	if err := working.CancelJob(t.Context(), jobstore.Job{ID: "job_1"}, "x"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	default:
		t.Fatal("a successful cancel was not hinted")
	}
	// Everything else passes straight through.
	if _, err := working.Stats(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// A hint is verified against the store before it stops anything.
func TestJobCanceledWithinConfirmsTheHintAgainstTheStore(t *testing.T) {
	store := jobstore.NewMemoryStore()
	job, _ := queuedJob(t, store, "cancel_hint_key_0001")
	c := &WorkerConsumer{Jobs: store}

	start := time.Now()
	if c.jobCanceledWithin(t.Context(), job.ID, 100*time.Millisecond) {
		t.Fatal("an unconfirmed hint was treated as a cancel")
	}
	if time.Since(start) < 90*time.Millisecond {
		t.Fatal("returned before the window passed")
	}

	// The cancel API writes the status just after the hint: seen within the window.
	go func() {
		time.Sleep(40 * time.Millisecond)
		_, _, _ = store.UpdateStatus(context.Background(), job.ID, jobstore.StatusCanceled)
	}()
	if !c.jobCanceledWithin(t.Context(), job.ID, 2*time.Second) {
		t.Fatal("the cancel written after the hint was not seen")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	other, _ := queuedJob(t, store, "cancel_hint_key_0002")
	if c.jobCanceledWithin(ctx, other.ID, time.Hour) {
		t.Fatal("a cancelled context must end the wait")
	}
}

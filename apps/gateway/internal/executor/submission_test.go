package executor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// TestHelperSubmitDaemon is not a real test: a fake daemon that, per job,
// prints a prompt_submitted line (unless the job id is job_daemon_pre) and then
// either dies (GO_HELPER_SUBMIT_MODE=die) or hangs (=hang).
func TestHelperSubmitDaemon(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_SUBMIT_DAEMON") != "1" {
		return
	}
	defer os.Exit(0)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if !strings.Contains(scanner.Text(), "job_daemon_pre") {
			fmt.Printf(`{"job_id":"job_daemon_1","api_version":"2026-05-22","type":"prompt_submitted","sequence":1,"data":{}}` + "\n")
		}
		if os.Getenv("GO_HELPER_SUBMIT_MODE") == "die" {
			os.Exit(3)
		}
		time.Sleep(time.Hour)
	}
}

func submitDaemonRunner(t *testing.T, mode string) *DaemonWorkerRunner {
	t.Helper()
	runner := &DaemonWorkerRunner{MaxRuntime: time.Minute}
	runner.newCommand = func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestHelperSubmitDaemon")
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_SUBMIT_DAEMON=1", "GO_HELPER_SUBMIT_MODE="+mode)
		return cmd
	}
	t.Cleanup(runner.Close)
	return runner
}

func TestSubmissionClassifyIsInertWithFlagOff(t *testing.T) {
	t.Setenv("UBAG_WORKER_STRICT_SUBMIT", "")
	boom := errors.New("boom")
	if got := classifySubmission(boom, true); got != boom {
		t.Fatalf("flag off must not wrap: %v", got)
	}
	t.Setenv("UBAG_WORKER_STRICT_SUBMIT", "true")
	if got := classifySubmission(boom, true); !errors.Is(got, ErrAmbiguous) || !errors.Is(got, boom) || errors.Is(got, ErrNotSubmitted) {
		t.Fatalf("submitted must be ErrAmbiguous keeping the cause: %v", got)
	}
	if got := classifySubmission(context.Canceled, false); !errors.Is(got, ErrNotSubmitted) || !errors.Is(got, context.Canceled) {
		t.Fatalf("not submitted must be ErrNotSubmitted keeping the cause: %v", got)
	}
	if classifySubmission(nil, true) != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestSubmissionMarkerDetection(t *testing.T) {
	if !lineIsPromptSubmitted(`{"type":"prompt_submitted","data":{}}`) {
		t.Fatal("marker line not detected")
	}
	// The word inside a payload must not count as the marker.
	if lineIsPromptSubmitted(`{"type":"token","data":{"text":"prompt_submitted"}}`) {
		t.Fatal("marker detected inside a token payload")
	}
	if !jsonlHasPromptSubmitted([]byte("{\"type\":\"running\"}\n{\"type\":\"prompt_submitted\"}\n")) ||
		jsonlHasPromptSubmitted([]byte("{\"type\":\"running\"}\n")) {
		t.Fatal("jsonlHasPromptSubmitted wrong")
	}
}

func TestSubmissionDaemonProtocolTracksMarker(t *testing.T) {
	var stdin strings.Builder
	var submitted atomic.Bool
	stdout := bufio.NewReader(strings.NewReader(daemonEventLine(t, 1, "prompt_submitted") + "\n"))
	if _, err := runDaemonJobTracked(&stdin, stdout, daemonTestEnvelope(), time.Minute, &submitted); err == nil {
		t.Fatal("stream without terminal marker must fail")
	}
	if !submitted.Load() {
		t.Fatal("prompt_submitted line was not tracked")
	}
}

func TestSubmissionDaemonRunnerTypesFailures(t *testing.T) {
	t.Setenv("UBAG_WORKER_STRICT_SUBMIT", "true")

	pre := daemonTestEnvelope()
	pre.JobID = "job_daemon_pre"
	if _, err := submitDaemonRunner(t, "die").RunWorker(context.Background(), pre); !errors.Is(err, ErrNotSubmitted) || errors.Is(err, ErrAmbiguous) {
		t.Fatalf("pre-submit death = %v, want ErrNotSubmitted", err)
	}
	if _, err := submitDaemonRunner(t, "die").RunWorker(context.Background(), daemonTestEnvelope()); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("post-submit death = %v, want ErrAmbiguous", err)
	}

	// Shutdown after submission: still ambiguous, and still a cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	if _, err := submitDaemonRunner(t, "hang").RunWorker(ctx, daemonTestEnvelope()); !errors.Is(err, ErrAmbiguous) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("post-submit hang = %v, want ErrAmbiguous wrapping the deadline", err)
	}
	// The streaming path shares runJob and so the same typing.
	sctx, scancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer scancel()
	if err := submitDaemonRunner(t, "hang").StreamWorker(sctx, daemonTestEnvelope(), &collectingSink{}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("streamed post-submit hang = %v, want ErrAmbiguous", err)
	}
}

func TestSubmissionDaemonRunnerUntypedWithFlagOff(t *testing.T) {
	t.Setenv("UBAG_WORKER_STRICT_SUBMIT", "")
	_, err := submitDaemonRunner(t, "die").RunWorker(context.Background(), daemonTestEnvelope())
	if err == nil || errors.Is(err, ErrAmbiguous) || errors.Is(err, ErrNotSubmitted) {
		t.Fatalf("flag off error = %v, want a plain untyped failure", err)
	}
}

func TestSubmissionWorkerEnvForwardsFlag(t *testing.T) {
	t.Setenv("UBAG_WORKER_STRICT_SUBMIT", "true")
	for _, item := range minimalWorkerEnv() {
		if item == "UBAG_WORKER_STRICT_SUBMIT=true" {
			return
		}
	}
	t.Fatal("UBAG_WORKER_STRICT_SUBMIT is not forwarded to the worker env")
}

// runSubmissionConsumer drives one RunOnce with the caller's context.
func runSubmissionConsumer(t *testing.T, ctx context.Context, runner WorkerRunner) (jobstore.Job, *fakeWorkerLease, []jobstore.Event, error) {
	t.Helper()
	store := jobstore.NewMemoryStore()
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", Target: "mock",
		CommandType: "submit", Input: map[string]any{"prompt": "hello"}, TraceID: "trace_submission",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	lease := &fakeWorkerLease{jobID: job.ID, leaseID: "lease_submission", envelope: EnvelopeFromJob(job)}
	consumer := WorkerConsumer{Queue: fakeWorkerQueue{lease: lease}, Jobs: store, Runner: runner}
	_, runErr := consumer.RunOnce(ctx)
	final, _, err := store.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	events, _, err := store.ListEvents(context.Background(), job.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	return final, lease, events, runErr
}

func assertFailedClosed(t *testing.T, final jobstore.Job, lease *fakeWorkerLease, events []jobstore.Event) {
	t.Helper()
	if final.Status != jobstore.StatusFailedTerminal {
		t.Fatalf("status = %s, want failed_terminal", final.Status)
	}
	if lease.retried || !lease.failed {
		t.Fatalf("lease retried=%v failed=%v, want Fail and no Retry", lease.retried, lease.failed)
	}
	last := events[len(events)-1]
	if last.Data["submitted"] != true || last.Data["reconcile_required"] != true || last.Data["retryable"] != false {
		t.Fatalf("terminal event data = %#v, want submitted + reconcile_required, not retryable", last.Data)
	}
}

func TestSubmissionPostSubmitFailureNeverRetriesLease(t *testing.T) {
	t.Setenv("UBAG_WORKER_STRICT_SUBMIT", "true")
	final, lease, events, err := runSubmissionConsumer(t, context.Background(),
		WorkerRunFunc(func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return nil, fmt.Errorf("%w: boom", ErrAmbiguous)
		}))
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	assertFailedClosed(t, final, lease, events)
}

// The shutdown path used to lease.Retry any error once ctx was cancelled,
// replaying a submitted prompt on the next consumer.
func TestSubmissionShutdownAfterSubmitFailsClosed(t *testing.T) {
	t.Setenv("UBAG_WORKER_STRICT_SUBMIT", "true")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	final, lease, events, _ := runSubmissionConsumer(t, ctx,
		WorkerRunFunc(func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			cancel() // gateway shutdown mid-job
			return nil, fmt.Errorf("%w: %w", ErrAmbiguous, context.Canceled)
		}))
	assertFailedClosed(t, final, lease, events)
}

func TestSubmissionShutdownBeforeSubmitStillRetries(t *testing.T) {
	t.Setenv("UBAG_WORKER_STRICT_SUBMIT", "true")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	final, lease, _, _ := runSubmissionConsumer(t, ctx,
		WorkerRunFunc(func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			cancel()
			return nil, fmt.Errorf("%w: %w", ErrNotSubmitted, context.Canceled)
		}))
	if !lease.retried || lease.failed || jobstore.TerminalStatus(final.Status) {
		t.Fatalf("lease retried=%v failed=%v status=%s, want a new attempt (Retry, non-terminal)", lease.retried, lease.failed, final.Status)
	}
}

func TestSubmissionPreSubmitFailureStaysRetryable(t *testing.T) {
	t.Setenv("UBAG_WORKER_STRICT_SUBMIT", "true")
	final, lease, _, err := runSubmissionConsumer(t, context.Background(),
		WorkerRunFunc(func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return nil, fmt.Errorf("%w: boom", ErrNotSubmitted)
		}))
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if final.Status != jobstore.StatusFailedRetryable || lease.retried {
		t.Fatalf("status=%s retried=%v, want failed_retryable and no lease replay", final.Status, lease.retried)
	}
}

// A user cancel after submission is still a cancel, not a reconcile failure.
func TestSubmissionUserCancelAfterSubmitStaysCanceled(t *testing.T) {
	t.Setenv("UBAG_WORKER_STRICT_SUBMIT", "true")
	store := jobstore.NewMemoryStore()
	job, err := store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", Target: "mock",
		CommandType: "submit", Input: map[string]any{"prompt": "hello"}, TraceID: "trace_submission_cancel",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	lease := &fakeWorkerLease{jobID: job.ID, leaseID: "lease_cancel", envelope: EnvelopeFromJob(job)}
	consumer := WorkerConsumer{Queue: fakeWorkerQueue{lease: lease}, Jobs: store,
		Runner: WorkerRunFunc(func(context.Context, DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			if _, _, err := store.UpdateStatus(context.Background(), job.ID, jobstore.StatusCanceled); err != nil {
				t.Errorf("cancel: %v", err)
			}
			return nil, fmt.Errorf("%w: %w", ErrAmbiguous, context.Canceled)
		})}
	if _, err := consumer.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !lease.cancelled || lease.retried {
		t.Fatalf("lease cancelled=%v retried=%v, want Cancel", lease.cancelled, lease.retried)
	}
}

// Unusable events after a submit marker are post-submit ambiguity too.
func TestSubmissionUnusableEventsAfterSubmitFailClosed(t *testing.T) {
	noTerminal := WorkerRunFunc(func(_ context.Context, e DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
		return []jobstore.WorkerEvent{
			ingestTestEvent(e, "sub_marker", "prompt_submitted", 1, map[string]any{}),
			ingestTestEvent(e, "sub_run", "running", 2, map[string]any{"status": "running"}),
		}, nil
	})

	t.Setenv("UBAG_WORKER_STRICT_SUBMIT", "true")
	final, lease, events, err := runSubmissionConsumer(t, context.Background(), noTerminal)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	assertFailedClosed(t, final, lease, events)

	t.Setenv("UBAG_WORKER_STRICT_SUBMIT", "")
	final, _, _, err = runSubmissionConsumer(t, context.Background(), noTerminal)
	if err != nil {
		t.Fatalf("RunOnce flag off: %v", err)
	}
	if final.Status != jobstore.StatusFailedRetryable {
		t.Fatalf("flag off status = %s, want unchanged failed_retryable", final.Status)
	}
}

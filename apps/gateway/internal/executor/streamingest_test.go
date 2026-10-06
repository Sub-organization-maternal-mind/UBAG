package executor

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/sqlitestore"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
	_ "modernc.org/sqlite"
)

// These tests drive the WorkerConsumer's streaming ingestion (UBAG_WORKER_STREAM_INGEST)
// end to end against a real DaemonWorkerRunner whose daemon is this test binary
// re-executed (TestHelperStreamDaemon), so the line protocol, the process
// lifecycle and the store are all real; only the worker's browser is fake.

// --- fake streaming daemon ---------------------------------------------------

type helperStream struct{ jobID, apiVersion, traceID, attempt string }

// emit writes one event the way the Python worker does: an attempt-scoped
// event id and data.attempt_id whenever the request carried an attempt.
func (h helperStream) emit(eventType string, seq int, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	id := fmt.Sprintf("evt_%s_%d", h.jobID, seq)
	if h.attempt != "" {
		id = fmt.Sprintf("evt_%s_%s_%d", h.jobID, h.attempt, seq)
		data["attempt_id"] = h.attempt
	}
	h.line(id, eventType, seq, data)
}

func (h helperStream) line(id, eventType string, seq int, data map[string]any) {
	encoded, _ := json.Marshal(map[string]any{
		"event_id": id, "job_id": h.jobID, "api_version": h.apiVersion, "trace_id": h.traceID,
		"type": eventType, "sequence": seq, "data": data,
	})
	fmt.Println(string(encoded))
}

func (h helperStream) end(status, message string) {
	end := map[string]any{daemonJobEndKey: true, "job_id": h.jobID, "status": status}
	if message != "" {
		end["error"] = message
	}
	encoded, _ := json.Marshal(end)
	fmt.Println(string(encoded))
}

func (h helperStream) token(seq int, text string) {
	h.emit("token", seq, map[string]any{"status": "token_streaming", "delta": map[string]any{"text": text}})
}

func (h helperStream) done(seq int) { h.emit("completed", seq, completedIngestData()) }

func helperCap(identity string) map[string]any {
	return map[string]any{"target": "mock", "identity_ref": identity, "current_cap": float64(2), "min": float64(1), "max": float64(4)}
}

func waitForFile(path string) {
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	os.Exit(4)
}

// TestHelperStreamDaemon is not a real test: it is the fake streaming daemon
// spawned by the tests below (job.input.scenario picks the behaviour).
func TestHelperStreamDaemon(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_STREAM_DAEMON") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var request struct {
			JobID   string `json:"job_id"`
			Payload struct {
				APIVersion string `json:"api_version"`
				TraceID    string `json:"trace_id"`
				Attempt    *struct {
					ID string `json:"id"`
				} `json:"attempt"`
				Job struct {
					Input map[string]any `json:"input"`
				} `json:"job"`
			} `json:"payload"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		h := helperStream{jobID: request.JobID, apiVersion: request.Payload.APIVersion, traceID: request.Payload.TraceID}
		if request.Payload.Attempt != nil {
			h.attempt = request.Payload.Attempt.ID
		}
		input := request.Payload.Job.Input
		scenario, _ := input["scenario"].(string)
		release, _ := input["release_file"].(string)
		marker, _ := input["marker_file"].(string)

		h.emit("running", 1, map[string]any{"status": "running"})
		switch scenario {
		case "complete":
			h.token(2, "hel")
			h.token(3, "lo")
			h.done(4)
			h.end("completed", "")
		case "burst": // 200 tokens in a row, then the terminal
			for i := 0; i < 200; i++ {
				h.token(2+i, "x")
			}
			h.done(300)
			h.end("completed", "")
		case "hold": // tokens, then the terminal only once the test releases it
			h.token(2, "hel")
			h.token(3, "lo")
			waitForFile(release)
			h.done(4)
			h.end("completed", "")
		case "hang":
			h.token(2, "hel")
			time.Sleep(time.Hour)
		case "hang_once": // the first run hangs after a token; a re-run completes
			if _, err := os.Stat(marker); err != nil {
				_ = os.WriteFile(marker, nil, 0o600)
				h.token(2, "hel")
				time.Sleep(time.Hour)
			}
			h.token(2, "hel")
			h.token(3, "lo")
			h.done(4)
			h.end("completed", "")
		case "die":
			h.token(2, "hel")
			os.Exit(3)
		case "no_end": // a valid terminal but no JOB_END
			h.token(2, "hel")
			h.done(3)
			os.Exit(0)
		case "zero_terminal":
			h.token(2, "hel")
			h.end("completed", "")
		case "two_terminals":
			h.token(2, "hel")
			h.done(3)
			h.done(4)
			h.end("completed", "")
		case "failed_end":
			h.token(2, "hel")
			h.done(3)
			h.end("failed", "provider blew up")
		case "stale":
			h.token(2, "hel")
			h.line("evt_stale", "token", 3, map[string]any{
				"status": "token_streaming", "delta": map[string]any{"text": "STALE"}, "attempt_id": "att_other"})
			h.token(4, "lo")
			h.done(5)
			h.end("completed", "")
		case "telemetry":
			h.emit("concurrency.cap_changed", 2, helperCap("acct-pre"))
			h.token(3, "hel")
			h.done(4)
			h.emit("concurrency.cap_changed", 5, helperCap("acct-post"))
			h.emit("session.new_chat", 6, nil)
			h.end("completed", "")
		case "submitted_die":
			h.emit("prompt_submitted", 2, nil)
			h.token(3, "hel")
			os.Exit(3)
		case "submitted_two_terminals":
			h.emit("prompt_submitted", 2, nil)
			h.token(3, "hel")
			h.done(4)
			h.done(5)
			h.end("completed", "")
		default:
			os.Exit(5)
		}
	}
	os.Exit(0)
}

// --- rig ---------------------------------------------------------------------

type streamRig struct {
	t        *testing.T
	store    jobstore.Store
	runner   *DaemonWorkerRunner
	registry *topology.ConcurrencyRegistry
	job      jobstore.Job
	dir      string
}

func newStreamRig(t *testing.T, scenario string) *streamRig {
	t.Helper()
	return newStreamRigOn(t, scenario, jobstore.NewMemoryStore())
}

func newStreamRigOn(t *testing.T, scenario string, store jobstore.Store) *streamRig {
	t.Helper()
	t.Setenv("UBAG_WORKER_STREAM_INGEST", "1")
	// Pin one store write per event so the exact-history tests below stay
	// deterministic; the coalescing tests set their own window.
	t.Setenv("UBAG_WORKER_STREAM_FLUSH_MS", "0")
	dir := t.TempDir()
	job, err := store.Create(t.Context(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", Target: "mock", CommandType: "submit",
		Input: map[string]any{
			"prompt": "hi", "scenario": scenario,
			"release_file": filepath.Join(dir, "release"), "marker_file": filepath.Join(dir, "marker"),
		},
		TraceID: "trace_stream_" + scenario,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &DaemonWorkerRunner{MaxRuntime: time.Minute}
	runner.newCommand = func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestHelperStreamDaemon")
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_STREAM_DAEMON=1")
		return cmd
	}
	t.Cleanup(runner.Close)
	return &streamRig{t: t, store: store, runner: runner, registry: topology.NewConcurrencyRegistry(), job: job, dir: dir}
}

func (r *streamRig) lease(id string) *fakeWorkerLease {
	return &fakeWorkerLease{jobID: r.job.ID, leaseID: id, envelope: EnvelopeFromJob(r.job)}
}

func (r *streamRig) consumer(lease *fakeWorkerLease) *WorkerConsumer {
	return &WorkerConsumer{
		Queue: fakeWorkerQueue{lease: lease}, Jobs: r.store, Runner: r.runner, Concurrency: r.registry,
	}
}

type onceResult struct {
	processed bool
	err       error
}

func (r *streamRig) start(ctx context.Context, lease *fakeWorkerLease) <-chan onceResult {
	done := make(chan onceResult, 1)
	consumer := r.consumer(lease)
	go func() {
		processed, err := consumer.RunOnce(ctx)
		done <- onceResult{processed, err}
	}()
	return done
}

func (r *streamRig) wait(done <-chan onceResult) onceResult {
	r.t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(30 * time.Second):
		r.t.Fatal("RunOnce did not return")
		return onceResult{}
	}
}

func (r *streamRig) run(lease *fakeWorkerLease) onceResult {
	r.t.Helper()
	return r.wait(r.start(context.Background(), lease))
}

func (r *streamRig) events() []jobstore.Event {
	r.t.Helper()
	events, _, err := r.store.ListEvents(r.t.Context(), r.job.ID, 0, 100)
	if err != nil {
		r.t.Fatal(err)
	}
	return events
}

func (r *streamRig) history() string { return eventTypes(r.events()) }

func (r *streamRig) final() jobstore.Job {
	r.t.Helper()
	job, found, err := r.store.Get(r.t.Context(), r.job.ID)
	if err != nil || !found {
		r.t.Fatalf("Get found=%v err=%v", found, err)
	}
	return job
}

// workerEventIDs lists the worker event ids the store recorded, in order.
func (r *streamRig) workerEventIDs() []string {
	var ids []string
	for _, event := range r.events() {
		if meta, ok := event.Data["worker_event"].(map[string]any); ok {
			ids = append(ids, fmt.Sprint(meta["event_id"]))
		}
	}
	return ids
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if condition() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

// --- the exit gate -------------------------------------------------------------

// The store sees tokens while the daemon is still running, and the terminal is
// applied only once the run ends cleanly.
func TestStreamIngestAppliesTokensBeforeTheRunReturns(t *testing.T) {
	rig := newStreamRig(t, "hold")
	lease := rig.lease("lease_hold")
	done := rig.start(context.Background(), lease)

	eventually(t, "both tokens applied while the daemon is still running", func() bool {
		return strings.Count(rig.history(), "token") == 2
	})
	if job := rig.final(); job.Status != jobstore.StatusTokenStreaming || job.Result != nil {
		t.Fatalf("mid-stream status=%s result=%#v, want token_streaming and no result", job.Status, job.Result)
	}
	select {
	case res := <-done:
		t.Fatalf("RunOnce returned before the terminal was released: %+v", res)
	default:
	}

	if err := os.WriteFile(filepath.Join(rig.dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if res := rig.wait(done); res.err != nil || !res.processed {
		t.Fatalf("RunOnce processed=%v err=%v", res.processed, res.err)
	}
	final := rig.final()
	if final.Status != jobstore.StatusCompleted || final.Result == nil {
		t.Fatalf("status=%s result=%#v, want completed with a result", final.Status, final.Result)
	}
	if got := rig.history(); got != "queued,assigned,running,token,token,completed" {
		t.Fatalf("history = %s", got)
	}
	if !lease.completed || lease.failed || lease.retried {
		t.Fatalf("lease = %+v, want Complete only", lease)
	}
}

// A daemon killed mid-stream fails the job; the tokens it produced stay as the
// discarded partial but the job never completes and carries no result.
func TestStreamIngestKilledDaemonMidStreamFailsNeverCompletes(t *testing.T) {
	rig := newStreamRig(t, "die")
	lease := rig.lease("lease_die")
	if res := rig.run(lease); res.err != nil {
		t.Fatalf("RunOnce: %v", res.err)
	}
	final := rig.final()
	if final.Status != jobstore.StatusFailedRetryable || final.Result != nil {
		t.Fatalf("status=%s result=%#v, want failed_retryable and no result", final.Status, final.Result)
	}
	if got := rig.history(); got != "queued,assigned,running,token,failed" {
		t.Fatalf("history = %s", got)
	}
	if !lease.failed || lease.completed || lease.retried {
		t.Fatalf("lease = %+v, want Fail only", lease)
	}
}

// Missing JOB_END, zero terminals, two terminals and a failed JOB_END all
// abandon the attempt. The held terminal is never applied, so a stream that
// DID carry a valid terminal still cannot complete the job without a clean end.
func TestStreamIngestAbandonsTheAttemptWithoutOneValidTerminal(t *testing.T) {
	cases := []struct{ scenario, history string }{
		{"no_end", "queued,assigned,running,token,failed"},
		{"zero_terminal", "queued,assigned,running,token,failed"},
		{"two_terminals", "queued,assigned,running,token,failed"},
		{"failed_end", "queued,assigned,running,token,failed"},
	}
	for _, tc := range cases {
		t.Run(tc.scenario, func(t *testing.T) {
			rig := newStreamRig(t, tc.scenario)
			lease := rig.lease("lease_" + tc.scenario)
			if res := rig.run(lease); res.err != nil {
				t.Fatalf("RunOnce: %v", res.err)
			}
			final := rig.final()
			if final.Status != jobstore.StatusFailedRetryable || final.Result != nil {
				t.Fatalf("status=%s result=%#v, want failed_retryable and no result", final.Status, final.Result)
			}
			if got := rig.history(); got != tc.history || strings.Contains(got, "completed") {
				t.Fatalf("history = %s, want %s (the held terminal must never be applied)", got, tc.history)
			}
			if !lease.failed || lease.completed || lease.retried {
				t.Fatalf("lease = %+v, want Fail only", lease)
			}
			events := rig.events()
			if class := events[len(events)-1].Data["error_class"]; class != "worker_execution" {
				t.Fatalf("failure error_class = %v, want worker_execution", class)
			}
		})
	}
}

// Events stamped by another attempt are dropped, never applied.
func TestStreamIngestDropsStaleAttemptEvents(t *testing.T) {
	rig := newStreamRig(t, "stale")
	if res := rig.run(rig.lease("lease_current")); res.err != nil {
		t.Fatalf("RunOnce: %v", res.err)
	}
	if final := rig.final(); final.Status != jobstore.StatusCompleted {
		t.Fatalf("status = %s, want completed", final.Status)
	}
	for _, id := range rig.workerEventIDs() {
		if id == "evt_stale" {
			t.Fatalf("stale-attempt event was applied: %v", rig.workerEventIDs())
		}
	}
	if got := rig.history(); got != "queued,assigned,running,token,token,completed" {
		t.Fatalf("history = %s", got)
	}
}

// A job re-leased after a streamed partial (here: gateway shutdown mid-stream)
// runs again and completes. Its events reuse the first attempt's sequence
// numbers, so without attempt-scoped ids the store would drop them all.
func TestStreamIngestRetryAfterAStreamedPartialCompletes(t *testing.T) {
	rig := newStreamRig(t, "hang_once")
	ctx, cancel := context.WithCancel(context.Background())
	first := rig.lease("lease_one")
	done := rig.start(ctx, first)
	eventually(t, "the first attempt's token", func() bool { return strings.Contains(rig.history(), "token") })
	cancel()
	if res := rig.wait(done); !errors.Is(res.err, context.Canceled) {
		t.Fatalf("first attempt err = %v, want context canceled", res.err)
	}
	if !first.retried || first.failed || first.completed {
		t.Fatalf("first lease = %+v, want Retry only", first)
	}
	if job := rig.final(); jobstore.TerminalStatus(job.Status) {
		t.Fatalf("a shutdown mid-stream must not finish the job, status = %s", job.Status)
	}

	second := rig.lease("lease_two")
	if res := rig.run(second); res.err != nil {
		t.Fatalf("second attempt: %v", res.err)
	}
	final := rig.final()
	if final.Status != jobstore.StatusCompleted || final.Result == nil {
		t.Fatalf("status=%s result=%#v, want completed with a result", final.Status, final.Result)
	}
	if !second.completed || second.failed {
		t.Fatalf("second lease = %+v, want Complete only", second)
	}
	ids := strings.Join(rig.workerEventIDs(), ",")
	one, two := attemptIDForLease("lease_one"), attemptIDForLease("lease_two")
	for _, want := range []string{one + "_2", two + "_2", two + "_3", two + "_4"} {
		if !strings.Contains(ids, want) {
			t.Fatalf("event ids %s are missing %s: attempt-scoped ids must keep both attempts' events", ids, want)
		}
	}
}

// Cancelling the job mid-stream stops the run and ends the job canceled.
func TestStreamIngestCancelMidStream(t *testing.T) {
	rig := newStreamRig(t, "hang")
	lease := rig.lease("lease_cancel")
	done := rig.start(context.Background(), lease)
	eventually(t, "the first token", func() bool { return strings.Contains(rig.history(), "token") })
	if _, _, err := rig.store.UpdateStatus(t.Context(), rig.job.ID, jobstore.StatusCanceled); err != nil {
		t.Fatal(err)
	}
	if res := rig.wait(done); res.err != nil {
		t.Fatalf("RunOnce: %v", res.err)
	}
	if final := rig.final(); final.Status != jobstore.StatusCanceled || final.Result != nil {
		t.Fatalf("status=%s result=%#v, want canceled and no result", final.Status, final.Result)
	}
	if !lease.cancelled || lease.completed || lease.retried {
		t.Fatalf("lease = %+v, want Cancel only", lease)
	}
}

// Telemetry keeps being intercepted on the stream path, before and after the
// held terminal: it reaches its projections and never becomes a job event.
func TestStreamIngestInterceptsTelemetry(t *testing.T) {
	rig := newStreamRig(t, "telemetry")
	if res := rig.run(rig.lease("lease_telemetry")); res.err != nil {
		t.Fatalf("RunOnce: %v", res.err)
	}
	if final := rig.final(); final.Status != jobstore.StatusCompleted {
		t.Fatalf("status = %s, want completed", final.Status)
	}
	if got := rig.history(); got != "queued,assigned,running,token,completed" {
		t.Fatalf("history = %s (telemetry must not be applied as job events)", got)
	}
	identities := map[string]bool{}
	for _, view := range rig.registry.List("tenant_a") {
		identities[view.IdentityRef] = true
	}
	if !identities["acct-pre"] || !identities["acct-post"] {
		t.Fatalf("concurrency reports recorded = %v, want both the pre- and post-terminal one", identities)
	}
}

// With UBAG_WORKER_STRICT_SUBMIT a failure after prompt_submitted ends the job
// failed_terminal for reconcile and is never replayed, whether the daemon died
// or the ingestion itself rejected the stream.
func TestStreamIngestStrictSubmitFailsClosedAfterSubmission(t *testing.T) {
	for _, scenario := range []string{"submitted_die", "submitted_two_terminals"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("UBAG_WORKER_STRICT_SUBMIT", "1")
			rig := newStreamRig(t, scenario)
			lease := rig.lease("lease_" + scenario)
			if res := rig.run(lease); res.err != nil {
				t.Fatalf("RunOnce: %v", res.err)
			}
			final := rig.final()
			if final.Status != jobstore.StatusFailedTerminal || final.Result != nil {
				t.Fatalf("status=%s result=%#v, want failed_terminal and no result", final.Status, final.Result)
			}
			events := rig.events()
			last := events[len(events)-1]
			if last.Data["reconcile_required"] != true || last.Data["submitted"] != true {
				t.Fatalf("failure data = %#v, want submitted + reconcile_required", last.Data)
			}
			if lease.retried || lease.completed || !lease.failed {
				t.Fatalf("lease = %+v, want Fail only (never a replay)", lease)
			}
		})
	}
}

// The held-terminal rule lives in the consumer, not the store: the same streams
// end the same way on the SQLite store.
func TestStreamIngestOnSQLiteStore(t *testing.T) {
	newStore := func(t *testing.T) jobstore.Store {
		db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "stream.db"))
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		if err := sqlitestore.Apply(t.Context(), db); err != nil {
			t.Fatal(err)
		}
		return jobstore.NewSQLiteStore(db)
	}
	cases := []struct {
		scenario string
		status   jobstore.Status
		history  string
	}{
		{"complete", jobstore.StatusCompleted, "queued,assigned,running,token,token,completed"},
		{"no_end", jobstore.StatusFailedRetryable, "queued,assigned,running,token,failed"},
		{"die", jobstore.StatusFailedRetryable, "queued,assigned,running,token,failed"},
	}
	for _, tc := range cases {
		t.Run(tc.scenario, func(t *testing.T) {
			rig := newStreamRigOn(t, tc.scenario, newStore(t))
			if res := rig.run(rig.lease("lease_sqlite_" + tc.scenario)); res.err != nil {
				t.Fatalf("RunOnce: %v", res.err)
			}
			if final := rig.final(); final.Status != tc.status {
				t.Fatalf("status = %s, want %s", final.Status, tc.status)
			}
			if got := rig.history(); got != tc.history {
				t.Fatalf("history = %s, want %s", got, tc.history)
			}
		})
	}
}

// --- selection and attempt ids -------------------------------------------------

// selectedRunner streams (or not) per job and records what it was handed.
type selectedRunner struct {
	streams   bool
	batch     atomic.Int32
	stream    atomic.Int32
	attemptID atomic.Value
}

func (r *selectedRunner) StreamsJob(DispatchEnvelope) bool { return r.streams }

func (r *selectedRunner) record(e DispatchEnvelope) {
	id := ""
	if e.Attempt != nil {
		id = e.Attempt.ID
	}
	r.attemptID.Store(id)
}

func (r *selectedRunner) events(e DispatchEnvelope) []jobstore.WorkerEvent {
	return []jobstore.WorkerEvent{ingestTestEvent(e, "sel_done", "completed", 1, completedIngestData())}
}

func (r *selectedRunner) RunWorker(_ context.Context, e DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
	r.batch.Add(1)
	r.record(e)
	return r.events(e), nil
}

func (r *selectedRunner) StreamWorker(ctx context.Context, e DispatchEnvelope, sink EventSink) error {
	r.stream.Add(1)
	r.record(e)
	for _, event := range r.events(e) {
		if err := sink.Emit(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func TestStreamIngestSelectionAndAttemptScopedIDs(t *testing.T) {
	cases := []struct {
		name        string
		flag        string
		streams     bool
		wantStream  bool
		wantAttempt bool
	}{
		{"flag on, runner streams the job", "1", true, true, true},
		{"flag on, runner keeps the job on the batch path", "1", false, false, false},
		{"flag off", "", true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UBAG_WORKER_STREAM_INGEST", tc.flag)
			t.Setenv("UBAG_WORKER_ATTEMPT_EVENT_IDS", "")
			runner := &selectedRunner{streams: tc.streams}
			final, lease, _ := runIngestWith(t, runner)
			if final.Status != jobstore.StatusCompleted || !lease.completed {
				t.Fatalf("status=%s lease=%+v, want completed", final.Status, lease)
			}
			if gotStream := runner.stream.Load() == 1 && runner.batch.Load() == 0; gotStream != tc.wantStream {
				t.Fatalf("stream=%d batch=%d, want streamed=%v", runner.stream.Load(), runner.batch.Load(), tc.wantStream)
			}
			// Streaming applies events before the end of the run, so it implies
			// attempt-scoped ids even without UBAG_WORKER_ATTEMPT_EVENT_IDS.
			gotID, _ := runner.attemptID.Load().(string)
			if tc.wantAttempt && gotID != attemptIDForLease("lease_ingest") || !tc.wantAttempt && gotID != "" {
				t.Fatalf("envelope attempt id = %q, want scoped=%v", gotID, tc.wantAttempt)
			}
		})
	}
}

// --- the sink itself -----------------------------------------------------------

func newSinkForTest(t *testing.T) (*streamIngest, *jobstore.MemoryStore) {
	t.Helper()
	store := jobstore.NewMemoryStore()
	job, err := store.Create(t.Context(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", Target: "mock",
		CommandType: "submit", Input: map[string]any{"prompt": "hi"}, TraceID: "trace_sink",
	})
	if err != nil {
		t.Fatal(err)
	}
	consumer := &WorkerConsumer{Jobs: store}
	return &streamIngest{c: consumer, job: job, envelope: EnvelopeFromJob(job)}, store
}

func TestStreamIngestSinkEnforcesTheByteBudgetAndRequiresType(t *testing.T) {
	sink, _ := newSinkForTest(t)
	ctx := t.Context()
	var ingestErr *streamIngestError
	if err := sink.Emit(ctx, jobstore.WorkerEvent{}); !errors.As(err, &ingestErr) || ingestErr.class != "invalid_event" {
		t.Fatalf("missing type error = %v", err)
	}

	// More than the old 512-event cap is fine while the byte budget holds.
	sink, store := newSinkForTest(t)
	sink.configured, sink.budget, sink.interval = true, defaultStreamByteBudget, 0
	for i := 1; i <= maxWorkerEvents+10; i++ {
		event := ingestTestEvent(sink.envelope, fmt.Sprintf("many_%d", i), "token", i,
			map[string]any{"status": "token_streaming", "delta": map[string]any{"text": "x"}})
		if err := sink.Emit(ctx, event); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	if events, _, _ := store.ListEvents(ctx, sink.job.ID, 0, 1000); len(events) != maxWorkerEvents+10+1 {
		t.Fatalf("stored %d events, want %d", len(events), maxWorkerEvents+10+1)
	}

	// The budget is explicit: an overrun fails the attempt, never truncates.
	sink, _ = newSinkForTest(t)
	sink.configured, sink.budget, sink.interval = true, 2*streamEventOverheadBytes+200, 0
	big := func(id string, seq int) jobstore.WorkerEvent {
		return ingestTestEvent(sink.envelope, id, "token", seq,
			map[string]any{"status": "token_streaming", "delta": map[string]any{"text": strings.Repeat("y", 100)}})
	}
	if err := sink.Emit(ctx, big("b1", 1)); err != nil {
		t.Fatalf("within budget: %v", err)
	}
	if err := sink.Emit(ctx, big("b2", 2)); !errors.As(err, &ingestErr) || ingestErr.class != "over_budget" {
		t.Fatalf("over the byte budget: err = %v, want an over_budget failure (never a silent truncation)", err)
	}
}

// Tokens arriving inside the window merge into one event; a non-token event
// flushes at once, in the worker's order.
func TestStreamIngestSinkCoalescesTokensAndFlushesInOrder(t *testing.T) {
	sink, store := newSinkForTest(t)
	sink.configured, sink.budget, sink.interval = true, defaultStreamByteBudget, time.Hour
	ctx := t.Context()
	token := func(i int, text string) jobstore.WorkerEvent {
		return ingestTestEvent(sink.envelope, fmt.Sprintf("co_%d", i), "token", i,
			map[string]any{"status": "token_streaming", "token_index": i, "delta": map[string]any{"text": text}})
	}
	// The first token after a quiet period is written at once.
	if err := sink.Emit(ctx, token(1, "a")); err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"b", "c", "d"} {
		if err := sink.Emit(ctx, token(2+i, text)); err != nil {
			t.Fatal(err)
		}
	}
	if events, _, _ := store.ListEvents(ctx, sink.job.ID, 0, 10); eventTypes(events) != "queued,token" {
		t.Fatalf("history = %s, want queued,token (the rest is coalescing)", eventTypes(events))
	}
	if err := sink.Emit(ctx, ingestTestEvent(sink.envelope, "co_run", "running", 9, map[string]any{"status": "running"})); err != nil {
		t.Fatal(err)
	}
	events, _, _ := store.ListEvents(ctx, sink.job.ID, 0, 10)
	if eventTypes(events) != "queued,token,token,running" {
		t.Fatalf("history = %s, want the merged token before the running event", eventTypes(events))
	}
	merged := events[2].Data
	if text, _ := tokenText(jobstore.WorkerEvent{Data: merged}); text != "bcd" || fmt.Sprint(merged["coalesced_deltas"]) != "3" {
		t.Fatalf("merged token = %#v, want text bcd from 3 deltas", merged)
	}
}

// A trailing token is written by the timer, and the run's end flushes the rest.
func TestStreamIngestSinkFlushesATrailingTokenOnTheTimerAndOnClose(t *testing.T) {
	sink, store := newSinkForTest(t)
	sink.configured, sink.budget, sink.interval = true, defaultStreamByteBudget, 30*time.Millisecond
	sink.lastFlush = time.Now()
	ctx := t.Context()
	token := func(i int, text string) jobstore.WorkerEvent {
		return ingestTestEvent(sink.envelope, fmt.Sprintf("tm_%d", i), "token", i,
			map[string]any{"status": "token_streaming", "delta": map[string]any{"text": text}})
	}
	if err := sink.Emit(ctx, token(1, "a")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the timer to flush the trailing token", func() bool {
		events, _, _ := store.ListEvents(ctx, sink.job.ID, 0, 10)
		return eventTypes(events) == "queued,token"
	})
	sink.mu.Lock()
	sink.interval = time.Hour
	sink.lastFlush = time.Now()
	sink.mu.Unlock()
	if err := sink.Emit(ctx, token(2, "b")); err != nil {
		t.Fatal(err)
	}
	if err := sink.close(ctx); err != nil {
		t.Fatal(err)
	}
	if events, _, _ := store.ListEvents(ctx, sink.job.ID, 0, 10); eventTypes(events) != "queued,token,token" {
		t.Fatalf("history after close = %s, want queued,token,token", eventTypes(events))
	}
}

// A store failure in the batch fails the attempt at the run's end.
func TestStreamIngestSinkReportsABatchFailure(t *testing.T) {
	sink, _ := newSinkForTest(t)
	sink.configured, sink.budget, sink.interval = true, defaultStreamByteBudget, time.Hour
	sink.lastFlush = time.Now()
	ctx := t.Context()
	if err := sink.Emit(ctx, ingestTestEvent(sink.envelope, "bf_1", "token", 1,
		map[string]any{"status": "token_streaming", "delta": map[string]any{"text": "a"}})); err != nil {
		t.Fatal(err)
	}
	sink.pending[0].JobID = "job_missing"
	var ingestErr *streamIngestError
	if err := sink.close(ctx); !errors.As(err, &ingestErr) || !ingestErr.missingJob {
		t.Fatalf("close = %v, want a missing_job failure", err)
	}
}

// A fast stream through the real consumer: 200 tokens collapse into a few
// events, none is lost, the text is intact, and the terminal still completes it.
func TestStreamIngestCoalescesABurstThroughTheConsumer(t *testing.T) {
	rig := newStreamRig(t, "burst")
	t.Setenv("UBAG_WORKER_STREAM_FLUSH_MS", "60000")
	if res := rig.run(rig.lease("lease_burst")); res.err != nil {
		t.Fatalf("RunOnce: %v", res.err)
	}
	if final := rig.final(); final.Status != jobstore.StatusCompleted || final.Result == nil {
		t.Fatalf("status=%s result=%#v, want completed", final.Status, final.Result)
	}
	var text strings.Builder
	var deltas, tokenEvents int
	for _, event := range rig.events() {
		if event.Type != "token" {
			continue
		}
		tokenEvents++
		part, _ := tokenText(jobstore.WorkerEvent{Data: event.Data})
		text.WriteString(part)
		switch n := event.Data["coalesced_deltas"].(type) {
		case int:
			deltas += n
		case float64:
			deltas += int(n)
		default:
			deltas++
		}
	}
	if text.String() != strings.Repeat("x", 200) || deltas != 200 {
		t.Fatalf("text len %d, deltas %d, want 200 and 200", text.Len(), deltas)
	}
	if tokenEvents > 3 {
		t.Fatalf("%d token events stored, want the burst coalesced into a few", tokenEvents)
	}
	if h := rig.history(); !strings.HasSuffix(h, ",completed") {
		t.Fatalf("history = %s, want the terminal last", h)
	}
}

// The terminal is held, not applied, and nothing but telemetry follows it.
func TestStreamIngestSinkHoldsTheTerminalAndIgnoresLaterEvents(t *testing.T) {
	sink, store := newSinkForTest(t)
	ctx := t.Context()
	done := ingestTestEvent(sink.envelope, "hold_done", "completed", 1, completedIngestData())
	if err := sink.Emit(ctx, done); err != nil {
		t.Fatal(err)
	}
	late := ingestTestEvent(sink.envelope, "hold_late", "token", 2,
		map[string]any{"status": "token_streaming", "delta": map[string]any{"text": "late"}})
	if err := sink.Emit(ctx, late); err != nil {
		t.Fatal(err)
	}
	if events, _, _ := store.ListEvents(ctx, sink.job.ID, 0, 10); eventTypes(events) != "queued" {
		t.Fatalf("history = %s, want only queued: the terminal is held and later events are dropped", eventTypes(events))
	}
	got, err := sink.finish()
	if err != nil || len(got) != 1 || got[0].EventID != "hold_done" {
		t.Fatalf("finish = %+v, %v, want the held terminal", got, err)
	}
	second := ingestTestEvent(sink.envelope, "hold_done_2", "completed", 3, completedIngestData())
	var ingestErr *streamIngestError
	if err := sink.Emit(ctx, second); !errors.As(err, &ingestErr) || ingestErr.class != "invalid_event" {
		t.Fatalf("second terminal err = %v, want an invalid_event failure", err)
	}
}

// --- P1.1 through the streaming path -------------------------------------------

// The P1.1 terminal-validation table, run through the streaming sink with the
// flag on: the same end state (failed_retryable, no result, Fail only, never
// completed), except that non-terminal events already applied stay as history
// and a terminal is never applied.
func TestIngestTerminalValidationAlwaysFailsNeverCompletesStreamed(t *testing.T) {
	t.Setenv("UBAG_WORKER_STREAM_INGEST", "1")
	for _, tc := range ingestFailureCases() {
		t.Run(tc.name, func(t *testing.T) {
			final, lease, events := runIngestWith(t, &dualRunner{batch: tc.runner})
			if final.Status != jobstore.StatusFailedRetryable {
				t.Fatalf("status = %s, want %s", final.Status, jobstore.StatusFailedRetryable)
			}
			if final.Result != nil {
				t.Fatalf("job must never carry a result: %#v", final.Result)
			}
			if !lease.failed || lease.completed || lease.retried || lease.poisoned {
				t.Fatalf("lease outcome failed=%v completed=%v retried=%v poisoned=%v, want only Fail",
					lease.failed, lease.completed, lease.retried, lease.poisoned)
			}
			history := eventTypes(events)
			if !strings.HasSuffix(history, ",failed") || strings.Contains(history, "completed") {
				t.Fatalf("history = %s, want it to end in the failure and never apply a terminal", history)
			}
			if class := events[len(events)-1].Data["error_class"]; class != "worker_execution" {
				t.Fatalf("failure error_class = %v, want worker_execution", class)
			}
		})
	}
}

// The worker's live-token cap (P3.8) must reach the daemon, or setting it on
// the gateway would silently do nothing.
func TestMinimalWorkerEnvForwardsTheStreamTokenCap(t *testing.T) {
	t.Setenv("UBAG_WORKER_STREAM_MAX_TOKEN_EVENTS", "123")
	for _, item := range minimalWorkerEnv() {
		if item == "UBAG_WORKER_STREAM_MAX_TOKEN_EVENTS=123" {
			return
		}
	}
	t.Fatal("UBAG_WORKER_STREAM_MAX_TOKEN_EVENTS is not forwarded to the worker")
}

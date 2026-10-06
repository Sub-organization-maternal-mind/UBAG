package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func daemonTestEnvelope() DispatchEnvelope {
	return DispatchEnvelope{
		APIVersion: "2026-05-22",
		JobID:      "job_daemon_1",
		TenantID:   "tenant_a",
		AppID:      "app_a",
		TraceID:    "trace_daemon_1",
		Job:        DispatchJob{Target: "mock"},
	}
}

func daemonEventLine(t *testing.T, seq int, eventType string) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"job_id":      "job_daemon_1",
		"api_version": "2026-05-22",
		"type":        eventType,
		"sequence":    seq,
		"data":        map[string]any{},
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return string(line)
}

func daemonEndLine(t *testing.T, status, errMsg string) string {
	t.Helper()
	end := map[string]any{daemonJobEndKey: true, "job_id": "job_daemon_1", "status": status}
	if errMsg != "" {
		end["error"] = errMsg
	}
	line, err := json.Marshal(end)
	if err != nil {
		t.Fatalf("marshal end: %v", err)
	}
	return string(line)
}

// --- protocol (no process involved) ------------------------------------------

func TestDaemonJobReturnsEventsBeforeTheTerminalMarker(t *testing.T) {
	stdout := bufio.NewReader(strings.NewReader(
		daemonEventLine(t, 1, "queued") + "\n" +
			daemonEventLine(t, 2, "completed") + "\n" +
			daemonEndLine(t, "completed", "") + "\n"))
	var stdin strings.Builder

	events, err := runDaemonJob(&stdin, stdout, daemonTestEnvelope(), time.Minute)
	if err != nil {
		t.Fatalf("runDaemonJob: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type != "queued" || events[1].Type != "completed" {
		t.Fatalf("unexpected event types: %+v", events)
	}
}

// The marker carries no "type", so letting it reach parseWorkerJSONL would fail
// the job with "missing type". It must be consumed as control, never as data.
func TestDaemonJobDoesNotTreatTheTerminalMarkerAsAnEvent(t *testing.T) {
	stdout := bufio.NewReader(strings.NewReader(
		daemonEventLine(t, 1, "completed") + "\n" + daemonEndLine(t, "completed", "") + "\n"))
	var stdin strings.Builder

	events, err := runDaemonJob(&stdin, stdout, daemonTestEnvelope(), time.Minute)
	if err != nil {
		t.Fatalf("runDaemonJob: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("marker leaked into events: %+v", events)
	}
}

func TestDaemonJobSurfacesAFailedMarkerAsAnError(t *testing.T) {
	stdout := bufio.NewReader(strings.NewReader(
		daemonEventLine(t, 1, "queued") + "\n" + daemonEndLine(t, "failed", "provider blew up") + "\n"))
	var stdin strings.Builder

	if _, err := runDaemonJob(&stdin, stdout, daemonTestEnvelope(), time.Minute); err == nil ||
		!strings.Contains(err.Error(), "provider blew up") {
		t.Fatalf("expected the daemon's error to surface, got %v", err)
	}
}

func TestDaemonJobRejectsATerminalMarkerForAnotherJob(t *testing.T) {
	wrongEnd := strings.Replace(
		daemonEndLine(t, "completed", ""),
		"job_daemon_1",
		"job_daemon_other",
		1,
	)
	stdout := bufio.NewReader(strings.NewReader(wrongEnd + "\n"))
	var stdin strings.Builder

	if _, err := runDaemonJob(&stdin, stdout, daemonTestEnvelope(), time.Minute); err == nil ||
		!strings.Contains(err.Error(), "does not match active job") {
		t.Fatalf("expected a job correlation error, got %v", err)
	}
}

func TestDaemonLineReaderRejectsOversizedLinesBeforeReadingTheJob(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 65)+"\n"), 8)
	if _, err := readBoundedDaemonLine(reader, 64); err == nil ||
		!strings.Contains(err.Error(), "exceeded 64 bytes") {
		t.Fatalf("expected bounded line error, got %v", err)
	}
}

// A daemon that dies mid-job yields EOF with no marker. That must fail THIS job
// rather than hang or silently return a truncated report as if it were complete.
func TestDaemonJobFailsWhenTheDaemonDiesWithoutAMarker(t *testing.T) {
	stdout := bufio.NewReader(strings.NewReader(daemonEventLine(t, 1, "queued") + "\n"))
	var stdin strings.Builder

	if _, err := runDaemonJob(&stdin, stdout, daemonTestEnvelope(), time.Minute); err == nil {
		t.Fatal("expected an error when the stream ended without a terminal marker")
	}
}

// The Go side no longer bounds the job by killing a per-job subprocess, so the
// deadline has to travel in the request for the daemon to enforce.
func TestDaemonJobSendsTheDeadlineInTheRequest(t *testing.T) {
	stdout := bufio.NewReader(strings.NewReader(daemonEndLine(t, "completed", "") + "\n"))
	var stdin strings.Builder

	if _, err := runDaemonJob(&stdin, stdout, daemonTestEnvelope(), 90*time.Second); err != nil {
		t.Fatalf("runDaemonJob: %v", err)
	}

	var request struct {
		JobID     string          `json:"job_id"`
		DeadlineS float64         `json:"deadline_s"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdin.String())), &request); err != nil {
		t.Fatalf("request is not one JSON line: %v (%q)", err, stdin.String())
	}
	if request.JobID != "job_daemon_1" {
		t.Fatalf("job_id = %q", request.JobID)
	}
	if request.DeadlineS != 90 {
		t.Fatalf("deadline_s = %v, want 90", request.DeadlineS)
	}
	if len(request.Payload) == 0 {
		t.Fatal("payload must carry the dispatch envelope")
	}
}

// --- process lifecycle (re-execs this test binary as a fake daemon) -----------

// TestHelperDaemon is not a real test: it is the fake daemon process spawned by
// the lifecycle tests below. Using the test binary avoids depending on a Python
// interpreter, which makes these tests hermetic and cross-platform.
func TestHelperDaemon(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_DAEMON") != "1" {
		return
	}
	defer os.Exit(0)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			JobID   string `json:"job_id"`
			Payload struct {
				Job struct {
					Input struct {
						// job.input.sleep_ms makes a job take that long, so tests can tell
						// overlapping jobs from serialized ones by their work windows.
						SleepMS int `json:"sleep_ms"`
					} `json:"input"`
				} `json:"job"`
			} `json:"payload"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &request)
		if os.Getenv("GO_HELPER_DIE") == "1" {
			os.Exit(3)
		}
		if request.JobID == "job_daemon_hang" {
			time.Sleep(time.Hour)
		}
		startUS := time.Now().UnixMicro()
		if ms := request.Payload.Job.Input.SleepMS; ms > 0 {
			time.Sleep(time.Duration(ms) * time.Millisecond)
		}
		endUS := time.Now().UnixMicro()
		fmt.Printf(`{"job_id":%q,"api_version":"2026-05-22","type":"completed","sequence":1,"data":{"pid":%d,"slot":%q,"start_us":%d,"end_us":%d}}`+"\n",
			request.JobID, os.Getpid(), os.Getenv("GO_HELPER_SLOT"), startUS, endUS)
		fmt.Printf(`{%q:true,"job_id":%q,"status":"completed"}`+"\n", daemonJobEndKey, request.JobID)
	}
}

func helperDaemonRunner(t *testing.T, spawns *int32) *DaemonWorkerRunner {
	t.Helper()
	var mu sync.Mutex
	runner := &DaemonWorkerRunner{MaxRuntime: time.Minute}
	runner.newCommand = func() *exec.Cmd {
		mu.Lock()
		*spawns++
		mu.Unlock()
		cmd := exec.Command(os.Args[0], "-test.run=TestHelperDaemon")
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_DAEMON=1")
		return cmd
	}
	t.Cleanup(runner.Close)
	return runner
}

// The entire point of Layer C: one process serves many jobs, so a job stops
// paying process startup + CDP re-attach.
func TestDaemonRunnerReusesOneProcessAcrossJobs(t *testing.T) {
	var spawns int32
	runner := helperDaemonRunner(t, &spawns)

	first, err := runner.RunWorker(context.Background(), daemonTestEnvelope())
	if err != nil {
		t.Fatalf("first job: %v", err)
	}
	second, err := runner.RunWorker(context.Background(), daemonTestEnvelope())
	if err != nil {
		t.Fatalf("second job: %v", err)
	}

	if spawns != 1 {
		t.Fatalf("spawned %d daemons, want 1", spawns)
	}
	if first[0].Data["pid"] != second[0].Data["pid"] {
		t.Fatalf("jobs ran in different processes: %v vs %v", first[0].Data["pid"], second[0].Data["pid"])
	}
}

// A crashed daemon must not wedge the gateway permanently: the next job restarts
// it. Losing warm pages is fine; refusing every future job is not.
func TestDaemonRunnerRestartsAfterTheDaemonDies(t *testing.T) {
	var spawns int32
	runner := helperDaemonRunner(t, &spawns)

	if _, err := runner.RunWorker(context.Background(), daemonTestEnvelope()); err != nil {
		t.Fatalf("first job: %v", err)
	}
	runner.killForTest()

	if _, err := runner.RunWorker(context.Background(), daemonTestEnvelope()); err != nil {
		t.Fatalf("job after daemon death should have restarted it: %v", err)
	}
	if spawns != 2 {
		t.Fatalf("spawned %d daemons, want 2 (one restart)", spawns)
	}
}

// One browser profile, one job at a time: concurrent turns on a shared provider
// account risk interleaved output and CAPTCHA/lockout. The pool of one slot
// (TestDaemonPoolOfOneSerializesConcurrentJobs) keeps the same promise.
func TestDaemonRunnerSerializesConcurrentJobs(t *testing.T) {
	var spawns int32
	runner := helperDaemonRunner(t, &spawns)

	var wg sync.WaitGroup
	windows := make(chan jobWindow, 4)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env := daemonTestEnvelope()
			env.Job.Input = map[string]any{"sleep_ms": 60}
			events, err := runner.RunWorker(context.Background(), env)
			if err != nil {
				errs <- err
				return
			}
			windows <- windowOf(t, events)
		}()
	}
	wg.Wait()
	close(errs)
	close(windows)
	for err := range errs {
		t.Fatalf("concurrent job failed: %v", err)
	}
	if spawns != 1 {
		t.Fatalf("spawned %d daemons, want 1", spawns)
	}
	var all []jobWindow
	for w := range windows {
		all = append(all, w)
	}
	assertNoOverlap(t, all)
}

// The run timeout must start once the daemon is held, not before the mutex: a job
// queued behind another one would otherwise burn its own MaxRuntime budget waiting
// and time out without ever having run.
func TestDaemonRunnerMaxRuntimeStartsAfterTheMutex(t *testing.T) {
	var spawns int32
	runner := helperDaemonRunner(t, &spawns)
	// Spawn the daemon first so process start-up does not eat the budget below.
	if _, err := runner.RunWorker(context.Background(), daemonTestEnvelope()); err != nil {
		t.Fatalf("warm-up job: %v", err)
	}
	runner.MaxRuntime = 700 * time.Millisecond

	first := make(chan error, 1)
	go func() {
		env := daemonTestEnvelope()
		env.Job.Input = map[string]any{"sleep_ms": 500}
		_, err := runner.RunWorker(context.Background(), env)
		first <- err
	}()
	time.Sleep(100 * time.Millisecond) // let the first job take the mutex

	// The second job waits ~400ms for the mutex and then runs 400ms: 800ms in
	// total is past MaxRuntime, but each run is inside it. Starting the clock
	// before the mutex would time it out here.
	second := daemonTestEnvelope()
	second.Job.Input = map[string]any{"sleep_ms": 400}
	if _, err := runner.RunWorker(context.Background(), second); err != nil {
		t.Fatalf("queued job must get its own run budget once it holds the daemon: %v", err)
	}
	if err := <-first; err != nil {
		t.Fatalf("first job: %v", err)
	}
}

func TestDaemonRunnerCancellationKillsAndReplacesTheActiveDaemon(t *testing.T) {
	var spawns int32
	runner := helperDaemonRunner(t, &spawns)
	hanging := daemonTestEnvelope()
	hanging.JobID = "job_daemon_hang"
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if _, err := runner.RunWorker(ctx, hanging); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hanging job error = %v, want context deadline exceeded", err)
	}
	if _, err := runner.RunWorker(context.Background(), daemonTestEnvelope()); err != nil {
		t.Fatalf("job after cancellation should use a replacement daemon: %v", err)
	}
	if spawns != 2 {
		t.Fatalf("spawned %d daemons, want 2 (one cancellation replacement)", spawns)
	}
}

func TestDaemonRunnerMaxRuntimeKillsAHungDaemonWithoutCallerDeadline(t *testing.T) {
	var spawns int32
	runner := helperDaemonRunner(t, &spawns)
	runner.MaxRuntime = 500 * time.Millisecond
	hanging := daemonTestEnvelope()
	hanging.JobID = "job_daemon_hang"

	if _, err := runner.RunWorker(context.Background(), hanging); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hanging job error = %v, want context deadline exceeded", err)
	}
	if _, err := runner.RunWorker(context.Background(), daemonTestEnvelope()); err != nil {
		t.Fatalf("job after max-runtime kill should use a replacement daemon: %v", err)
	}
	if spawns != 2 {
		t.Fatalf("spawned %d daemons, want 2 (one max-runtime replacement)", spawns)
	}
}

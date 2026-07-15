package executor

import (
	"bufio"
	"context"
	"encoding/json"
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
			JobID string `json:"job_id"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &request)
		if os.Getenv("GO_HELPER_DIE") == "1" {
			os.Exit(3)
		}
		fmt.Printf(`{"job_id":%q,"api_version":"2026-05-22","type":"completed","sequence":1,"data":{"pid":%d}}`+"\n",
			request.JobID, os.Getpid())
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
// account risk interleaved output and CAPTCHA/lockout.
func TestDaemonRunnerSerializesConcurrentJobs(t *testing.T) {
	var spawns int32
	runner := helperDaemonRunner(t, &spawns)

	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := runner.RunWorker(context.Background(), daemonTestEnvelope()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent job failed: %v", err)
	}
	if spawns != 1 {
		t.Fatalf("spawned %d daemons, want 1", spawns)
	}
}

package executor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// Baseline executor micro-benchmarks (perf-fleet P0.8). Test-only; no runtime
// code is touched. Run:
//
//	go test -run '^$' -bench 'Spool|RunOnce|Daemon' -benchmem ./internal/executor
//
// Numbers from a laptop/Docker are NON-AUTHORITATIVE; they are a control for
// later hot-path changes, not production capacity.

var benchTerminalCounts = []int{0, 1000, 10000}

// seedTerminalFiles drops n envelope-shaped files into the done dir so the
// per-enqueue duplicate check (Stat + Glob over terminal dirs) sees a realistic
// terminal backlog.
func seedTerminalFiles(b *testing.B, d *FileSpoolDispatcher, n int) {
	b.Helper()
	if err := d.Ready(context.Background()); err != nil {
		b.Fatalf("Ready: %v", err)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("job_term_%08d", i)
		body := fmt.Sprintf(`{"job_id":%q}`+"\n", id)
		if err := os.WriteFile(filepath.Join(d.doneDir(), id+".json"), []byte(body), 0o600); err != nil {
			b.Fatalf("seed: %v", err)
		}
	}
}

func benchJob(i int) jobstore.Job {
	return jobstore.Job{
		ID:         fmt.Sprintf("job_%012d", i+1),
		APIVersion: "2026-05-22",
		TenantID:   "tenant_a",
		AppID:      "app_a",
		Target:     "mock",
		Status:     jobstore.StatusQueued,
		Input:      map[string]any{"prompt": "hello"},
	}
}

func BenchmarkSpoolEnqueue(b *testing.B) {
	for _, terminal := range benchTerminalCounts {
		b.Run(fmt.Sprintf("terminal=%d", terminal), func(b *testing.B) {
			d := NewFileSpoolDispatcher(b.TempDir())
			seedTerminalFiles(b, d, terminal)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := d.EnqueueJob(ctx, benchJob(i)); err != nil {
					b.Fatalf("EnqueueJob: %v", err)
				}
			}
		})
	}
}

func BenchmarkSpoolLeaseNext(b *testing.B) {
	for _, terminal := range benchTerminalCounts {
		b.Run(fmt.Sprintf("terminal=%d", terminal), func(b *testing.B) {
			d := NewFileSpoolDispatcher(b.TempDir())
			seedTerminalFiles(b, d, terminal)
			ctx := context.Background()
			for i := 0; i < b.N; i++ { // pending depth == b.N
				if _, err := d.EnqueueJob(ctx, benchJob(i)); err != nil {
					b.Fatalf("EnqueueJob: %v", err)
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, ok, err := d.LeaseNext(ctx); err != nil || !ok {
					b.Fatalf("LeaseNext ok=%v err=%v", ok, err)
				}
			}
		})
	}
}

func BenchmarkRunOnceWorkerRunFunc(b *testing.B) {
	store := jobstore.NewMemoryStore()
	d := NewFileSpoolDispatcher(b.TempDir())
	ctx := context.Background()
	consumer := WorkerConsumer{
		Spool: d,
		Jobs:  store,
		Runner: WorkerRunFunc(func(_ context.Context, e DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return []jobstore.WorkerEvent{
				{EventID: "evt_run_" + e.JobID, JobID: e.JobID, APIVersion: e.APIVersion, Type: "running", Sequence: 1, TraceID: e.TraceID, Data: map[string]any{"status": "running"}},
				{EventID: "evt_done_" + e.JobID, JobID: e.JobID, APIVersion: e.APIVersion, Type: "completed", Sequence: 2, TraceID: e.TraceID, Data: map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "ok"}}},
			}, nil
		}),
	}
	for i := 0; i < b.N; i++ { // setup excluded from the timed region
		job, err := store.Create(ctx, jobstore.CreateRequest{
			APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a",
			IdempotencyKey: fmt.Sprintf("idem_bench_%d", i), Target: "mock", CommandType: "submit",
			Input: map[string]any{"prompt": "hello"}, TraceID: "trace_bench",
		})
		if err != nil {
			b.Fatalf("Create: %v", err)
		}
		if _, err := d.EnqueueJob(ctx, job); err != nil {
			b.Fatalf("EnqueueJob: %v", err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if ok, err := consumer.RunOnce(ctx); err != nil || !ok {
			b.Fatalf("RunOnce ok=%v err=%v", ok, err)
		}
	}
}

// BenchmarkDaemonLineProtocol measures Go-side protocol cost only (JSON request
// out, N event lines parsed, terminal marker), with no process or browser.
func BenchmarkDaemonLineProtocol(b *testing.B) {
	env := DispatchEnvelope{APIVersion: "2026-05-22", JobID: "job_daemon_1", TenantID: "tenant_a", AppID: "app_a", TraceID: "t", Job: DispatchJob{Target: "mock"}}
	for _, n := range []int{2, 50} {
		var sb strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&sb, `{"job_id":"job_daemon_1","api_version":"2026-05-22","type":"token","sequence":%d,"data":{"text":"x"}}`+"\n", i)
		}
		sb.WriteString(`{"` + daemonJobEndKey + `":true,"job_id":"job_daemon_1","status":"completed"}` + "\n")
		payload := sb.String()
		b.Run(fmt.Sprintf("events=%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				r := bufio.NewReader(strings.NewReader(payload))
				if _, err := runDaemonJob(io.Discard, r, env, time.Minute); err != nil {
					b.Fatalf("runDaemonJob: %v", err)
				}
			}
		})
	}
}

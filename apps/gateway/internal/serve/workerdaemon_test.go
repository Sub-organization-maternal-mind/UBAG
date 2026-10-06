package serve

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// Warm-browser reuse is opt-in. An unset flag MUST keep the per-job spawn: the
// daemon changes how a live browser session is driven for a radiology product,
// so it may never switch itself on by being deployed.
func TestWorkerDaemonDisabledByDefault(t *testing.T) {
	t.Setenv("UBAG_WORKER_DAEMON", "")

	if workerDaemonEnabled() {
		t.Fatal("worker daemon must be OFF unless explicitly enabled")
	}
}

func TestWorkerDaemonEnabledOnlyByExplicitTruthyValue(t *testing.T) {
	for _, value := range []string{"1", "true", "yes", "TRUE"} {
		t.Run("on/"+value, func(t *testing.T) {
			t.Setenv("UBAG_WORKER_DAEMON", value)
			if !workerDaemonEnabled() {
				t.Fatalf("%q should enable the daemon", value)
			}
		})
	}
	for _, value := range []string{"0", "false", "no", "maybe", " "} {
		t.Run("off/"+value, func(t *testing.T) {
			t.Setenv("UBAG_WORKER_DAEMON", value)
			if workerDaemonEnabled() {
				t.Fatalf("%q must NOT enable the daemon", value)
			}
		})
	}
}

func TestBuildWorkerRunnerUsesPerJobSpawnByDefault(t *testing.T) {
	t.Setenv("UBAG_WORKER_DAEMON", "")

	jobs := jobstore.NewMemoryStore()
	runner, err := buildWorkerRunner("python", "/scripts/run_live_worker.py", 0, nil, jobs)
	if err != nil {
		t.Fatalf("buildWorkerRunner: %v", err)
	}
	if process, ok := runner.(executor.ProcessWorkerRunner); !ok || process.Jobs != jobs {
		t.Fatalf("expected ProcessWorkerRunner, got %T", runner)
	}
}

func TestBuildWorkerRunnerUsesDaemonWhenEnabled(t *testing.T) {
	// The script path is resolved and existence-checked at startup, so point at
	// a real file rather than asserting against a path that cannot exist.
	script := filepath.Join(t.TempDir(), "run_worker_daemon.py")
	if err := os.WriteFile(script, []byte("# daemon\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	t.Setenv("UBAG_WORKER_DAEMON", "1")
	t.Setenv("UBAG_WORKER_DAEMON_SCRIPT", script)

	jobs := jobstore.NewMemoryStore()
	runner, err := buildWorkerRunner("python", "/scripts/run_live_worker.py", 0, nil, jobs)
	if err != nil {
		t.Fatalf("buildWorkerRunner: %v", err)
	}
	routed, ok := runner.(*targetWorkerRunner)
	if !ok {
		t.Fatalf("expected *targetWorkerRunner, got %T", runner)
	}
	daemon, ok := routed.daemon.(*executor.DaemonWorkerRunner)
	if !ok {
		t.Fatalf("expected daemon branch to be *DaemonWorkerRunner, got %T", routed.daemon)
	}
	if daemon.Script != script {
		t.Fatalf("daemon script = %q, want %q", daemon.Script, script)
	}
	if fallback, ok := routed.fallback.(executor.ProcessWorkerRunner); !ok || fallback.Jobs != jobs {
		t.Fatalf("expected fallback branch to be ProcessWorkerRunner, got %T", routed.fallback)
	}
}

// A pool size above one swaps the single warm daemon for isolated slots. Unset is
// today's single daemon (TestBuildWorkerRunnerUsesDaemonWhenEnabled).
func TestBuildWorkerRunnerUsesADaemonPoolWhenSizeAboveOne(t *testing.T) {
	script := filepath.Join(t.TempDir(), "run_worker_daemon.py")
	if err := os.WriteFile(script, []byte("# daemon\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	t.Setenv("UBAG_WORKER_DAEMON", "1")
	t.Setenv("UBAG_WORKER_DAEMON_SCRIPT", script)
	t.Setenv("UBAG_WORKER_POOL_SIZE", "2")
	t.Setenv("UBAG_WORKER_POOL_WAIT_MS", "1500")

	runner, err := buildWorkerRunner("python", "/scripts/run_live_worker.py", 90*time.Second, nil, jobstore.NewMemoryStore())
	if err != nil {
		t.Fatalf("buildWorkerRunner: %v", err)
	}
	routed, ok := runner.(*targetWorkerRunner)
	if !ok {
		t.Fatalf("expected *targetWorkerRunner, got %T", runner)
	}
	pool, ok := routed.daemon.(*executor.DaemonPool)
	if !ok {
		t.Fatalf("expected daemon branch to be *DaemonPool, got %T", routed.daemon)
	}
	if pool.Size != 2 || pool.Script != script || pool.MaxRuntime != 90*time.Second || pool.MaxWait != 1500*time.Millisecond {
		t.Fatalf("pool = size %d script %q max runtime %v max wait %v", pool.Size, pool.Script, pool.MaxRuntime, pool.MaxWait)
	}
	if _, ok := routed.fallback.(executor.ProcessWorkerRunner); !ok {
		t.Fatalf("non-live targets must stay on the per-job worker, got %T", routed.fallback)
	}
}

func TestBuildWorkerRunnerKeepsTheSingleDaemonAtPoolSizeOne(t *testing.T) {
	script := filepath.Join(t.TempDir(), "run_worker_daemon.py")
	if err := os.WriteFile(script, []byte("# daemon\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	t.Setenv("UBAG_WORKER_DAEMON", "1")
	t.Setenv("UBAG_WORKER_DAEMON_SCRIPT", script)
	t.Setenv("UBAG_WORKER_POOL_SIZE", "1")

	runner, err := buildWorkerRunner("python", "/scripts/run_live_worker.py", 0, nil, nil)
	if err != nil {
		t.Fatalf("buildWorkerRunner: %v", err)
	}
	if _, ok := runner.(*targetWorkerRunner).daemon.(*executor.DaemonWorkerRunner); !ok {
		t.Fatalf("pool size 1 must build today's single DaemonWorkerRunner, got %T", runner.(*targetWorkerRunner).daemon)
	}
}

func TestWorkerPoolSizeDefaultsToOneAndIsCappedByTheCeiling(t *testing.T) {
	cases := []struct {
		name            string
		size, max       string
		want, wantAsked int
	}{
		{"unset is one daemon", "", "", 1, 1},
		{"explicit", "2", "", 2, 2},
		{"default ceiling", "10", "", 3, 10},
		{"raised ceiling", "6", "8", 6, 6},
		{"raised ceiling still caps", "10", "8", 8, 10},
		{"ceiling never exceeds 32", "99", "99", 32, 99},
		{"lowered ceiling", "4", "1", 1, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("UBAG_WORKER_POOL_SIZE", tc.size)
			t.Setenv("UBAG_WORKER_POOL_MAX", tc.max)
			got, asked, err := workerPoolSizeFromEnv()
			if err != nil || got != tc.want || asked != tc.wantAsked {
				t.Fatalf("size=%q max=%q -> (%d, %d, %v), want (%d, %d, nil)", tc.size, tc.max, got, asked, err, tc.want, tc.wantAsked)
			}
		})
	}
	for _, bad := range []string{"0", "-1", "two"} {
		t.Setenv("UBAG_WORKER_POOL_SIZE", bad)
		if _, _, err := workerPoolSizeFromEnv(); err == nil {
			t.Fatalf("UBAG_WORKER_POOL_SIZE=%q must be rejected at startup", bad)
		}
	}
}

// The slots are only used if enough consumer goroutines feed them.
func TestCoupledWorkerConcurrencyRaisesButNeverLowers(t *testing.T) {
	for _, tc := range []struct{ concurrency, pool, want int }{{1, 1, 1}, {1, 3, 3}, {2, 3, 3}, {8, 3, 8}} {
		if got := coupledWorkerConcurrency(tc.concurrency, tc.pool); got != tc.want {
			t.Fatalf("concurrency %d, pool %d -> %d, want %d", tc.concurrency, tc.pool, got, tc.want)
		}
	}
}

func TestWorkerConsumerConcurrencyFollowsTheDaemonPoolSize(t *testing.T) {
	script := filepath.Join(t.TempDir(), "run_worker_daemon.py")
	if err := os.WriteFile(script, []byte("# daemon\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	t.Setenv("UBAG_WORKER_SCRIPT", script)
	t.Setenv("UBAG_WORKER_PYTHON", os.Args[0])
	t.Setenv("UBAG_WORKER_DAEMON_SCRIPT", script)
	t.Setenv("UBAG_WORKER_CONCURRENCY", "1")
	t.Setenv("UBAG_WORKER_POOL_SIZE", "2")
	t.Setenv("UBAG_EXECUTOR_MODE", "file")
	t.Setenv("UBAG_EXECUTOR_SPOOL_DIR", t.TempDir())
	dispatcher, err := newDispatcherFromEnv()
	if err != nil {
		t.Fatalf("newDispatcherFromEnv: %v", err)
	}
	build := func() *executor.WorkerConsumer {
		t.Helper()
		consumer, err := newWorkerConsumerFromEnv(dispatcher, jobstore.NewMemoryStore(), nil, nil, nil, nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("newWorkerConsumerFromEnv: %v", err)
		}
		return consumer
	}

	t.Setenv("UBAG_WORKER_DAEMON", "1")
	if got := build().PoolSize; got != 2 {
		t.Fatalf("consumer PoolSize = %d, want 2 (coupled to the daemon pool)", got)
	}
	// Pool size has no effect while the daemon is off: concurrency is today's.
	t.Setenv("UBAG_WORKER_DAEMON", "")
	if got := build().PoolSize; got != 1 {
		t.Fatalf("consumer PoolSize = %d, want 1 without the daemon", got)
	}
}

func TestWorkerDaemonRoutesOnlyLiveWebTargets(t *testing.T) {
	var daemonTargets []string
	var fallbackTargets []string
	runner := &targetWorkerRunner{
		daemon: executor.WorkerRunFunc(func(_ context.Context, envelope executor.DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			daemonTargets = append(daemonTargets, envelope.Job.Target)
			return nil, nil
		}),
		fallback: executor.WorkerRunFunc(func(_ context.Context, envelope executor.DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			fallbackTargets = append(fallbackTargets, envelope.Job.Target)
			return nil, nil
		}),
	}

	for _, target := range []string{
		"chatgpt_web",
		"deepseek_web",
		"gemini_web",
		"mistral_lechat",
		"duckai_web",
	} {
		_, err := runner.RunWorker(context.Background(), executor.DispatchEnvelope{
			Job: executor.DispatchJob{Target: target},
		})
		if err != nil {
			t.Fatalf("route live target %q: %v", target, err)
		}
	}
	for _, target := range []string{"mock", "generic_chat", "generic_form", "unknown"} {
		_, err := runner.RunWorker(context.Background(), executor.DispatchEnvelope{
			Job: executor.DispatchJob{Target: target},
		})
		if err != nil {
			t.Fatalf("route fallback target %q: %v", target, err)
		}
	}

	if len(daemonTargets) != 5 {
		t.Fatalf("daemon targets = %v, want all five live web providers", daemonTargets)
	}
	if len(fallbackTargets) != 4 {
		t.Fatalf("fallback targets = %v, want mock/generic/unknown", fallbackTargets)
	}
}

// A daemon pointed at a missing script must fail startup rather than boot and
// fail every job at runtime.
func TestBuildWorkerRunnerRefusesAMissingDaemonScript(t *testing.T) {
	t.Setenv("UBAG_WORKER_DAEMON", "1")
	t.Setenv("UBAG_WORKER_DAEMON_SCRIPT", filepath.Join(t.TempDir(), "absent.py"))

	if _, err := buildWorkerRunner("python", "/scripts/run_live_worker.py", 0, nil, nil); err == nil {
		t.Fatal("expected startup to refuse a daemon whose script does not exist")
	}
}

// The daemon entrypoint is a different script from the per-job worker. Silently
// falling back to the per-job script would start a process that exits after one
// job, and every subsequent job would restart it -- warm reuse would appear
// enabled while quietly doing nothing.
func TestBuildWorkerRunnerRefusesTheDaemonWithoutItsOwnScript(t *testing.T) {
	t.Setenv("UBAG_WORKER_DAEMON", "1")
	t.Setenv("UBAG_WORKER_DAEMON_SCRIPT", "")

	_, err := buildWorkerRunner("python", "/scripts/run_live_worker.py", 0, nil, nil)
	if err == nil {
		t.Fatal("expected startup to refuse a daemon with no daemon script")
	}
}

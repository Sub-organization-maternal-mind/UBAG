package serve

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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

	runner, err := buildWorkerRunner("python", "/scripts/run_live_worker.py", 0, nil)
	if err != nil {
		t.Fatalf("buildWorkerRunner: %v", err)
	}
	if _, ok := runner.(executor.ProcessWorkerRunner); !ok {
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

	runner, err := buildWorkerRunner("python", "/scripts/run_live_worker.py", 0, nil)
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
	if _, ok := routed.fallback.(executor.ProcessWorkerRunner); !ok {
		t.Fatalf("expected fallback branch to be ProcessWorkerRunner, got %T", routed.fallback)
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
		"claude_web",
		"deepseek_web",
		"gemini_web",
		"mistral_lechat",
		"perplexity_web",
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

	if len(daemonTargets) != 6 {
		t.Fatalf("daemon targets = %v, want all six live web providers", daemonTargets)
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

	if _, err := buildWorkerRunner("python", "/scripts/run_live_worker.py", 0, nil); err == nil {
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

	_, err := buildWorkerRunner("python", "/scripts/run_live_worker.py", 0, nil)
	if err == nil {
		t.Fatal("expected startup to refuse a daemon with no daemon script")
	}
}

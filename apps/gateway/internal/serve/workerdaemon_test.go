package serve

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
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
	daemon, ok := runner.(*executor.DaemonWorkerRunner)
	if !ok {
		t.Fatalf("expected *DaemonWorkerRunner, got %T", runner)
	}
	if daemon.Script != script {
		t.Fatalf("daemon script = %q, want %q", daemon.Script, script)
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

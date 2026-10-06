package runner

// Helper isolation acceptance (perf-fleet P7.3): the env-dump scan.
//
// The helper's own process refuses to start with primary-only configuration
// (helper.CheckEnvIsolation); this test covers the processes it spawns. A helper
// process that carries every primary secret is started for real, N worker daemons
// are spawned through the real runner and the real env allowlist, and each daemon
// reports the environment it actually received. The scan fails on any primary
// secret VALUE, any forbidden or secret-shaped KEY, and on a missing helper-mode
// switch, at 1, 2, 5, 10 and 20 concurrent workloads.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/helper"
)

const (
	isolationDaemonMarker = "isolation-env-daemon"
	secretSentinel        = "S3CR3T-LEAK-"
)

// TestIsolationEnvDaemon is the worker daemon the isolation scan spawns (the test
// binary re-executes itself). UBAG_MOCK_SYNTHETIC is on the worker allowlist, so
// it is the one switch that survives into the daemon; it is inert otherwise.
func TestIsolationEnvDaemon(t *testing.T) {
	if os.Getenv("UBAG_MOCK_SYNTHETIC") != isolationDaemonMarker {
		return
	}
	in := bufio.NewReaderSize(os.Stdin, 1<<20)
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			os.Exit(0) // stdin EOF: the pool is draining us
		}
		var req struct {
			JobID string `json:"job_id"`
		}
		if json.Unmarshal(line, &req) != nil {
			os.Exit(2)
		}
		dump, _ := json.Marshal(os.Environ())
		ev("prompt_submitted", map[string]any{})
		ev("completed", map[string]any{"result": map[string]any{"type": "text", "text": string(dump)}})
		say(map[string]any{"__ubag_job_end__": true, "job_id": req.JobID, "status": "completed"})
	}
}

// primarySecrets is what a primary carries and a helper must never see: every
// name the helper's start-up gate forbids, plus common credential-shaped names no
// allowlist should pass.
var primarySecrets = []string{
	"UBAG_APP_SECRET", "UBAG_MASTER_KEK_HEX", "UBAG_DATABASE_URL", "UBAG_POSTGRES_DSN", "UBAG_SQLITE_DSN",
	"UBAG_REMOTE_BROWSER_ENDPOINT", "UBAG_NOVNC_BASE_URL", "UBAG_FLEET_MANAGER_URL", "DATABASE_URL", "PGPASSWORD",
	"UBAG_MINIO_ACCESS_KEY", "UBAG_MINIO_SECRET_KEY", "UBAG_GARAGE_ADMIN_TOKEN", "UBAG_NATS_URL", "UBAG_NATS_CREDS",
	"UBAG_DATABASE_REPLICA_URL", "UBAG_APP_JWT_SIGNING_KEY", "UBAG_WEBHOOK_SECRET", "UBAG_ANTIGRAVITY_MODEL",
	"UBAG_FLEET_TOKEN", "UBAG_VOICE_RELAY_SECRET", "UBAG_VOICE_TURN_SECRET", "UBAG_HELPER_CLIENT_KEY_FILE",
	"AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "SSH_AUTH_SOCK",
}

func TestIsolationWorkerProcessesCarryNoPrimarySecrets(t *testing.T) {
	for _, k := range primarySecrets {
		t.Setenv(k, secretSentinel+k)
	}
	t.Setenv("UBAG_MOCK_SYNTHETIC", isolationDaemonMarker)
	t.Setenv("UBAG_HELPER_PLANE", "0") // a stray value must not switch helper mode off

	for _, n := range ladder() {
		t.Run(fmt.Sprintf("%d_workloads", n), func(t *testing.T) {
			r, err := New(Config{
				Python: os.Args[0], Script: "-test.run=TestIsolationEnvDaemon", Slots: n,
				MaxWait: 60 * time.Second, ProfileRoot: "/var/ubag/profiles",
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(r.Close)

			dumps := make([][]string, n)
			errs := make([]error, n)
			var wg sync.WaitGroup
			for i := range n {
				wg.Add(1)
				go func() {
					defer wg.Done()
					sp := spec(fmt.Sprintf("jobenv%d", i), fmt.Sprintf("ident-env-%d", i), `{}`)
					sk := &sink{}
					ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
					defer cancel()
					if err := r.Run(ctx, sp, sk.emit); err != nil {
						errs[i] = err
						return
					}
					last := sk.events[len(sk.events)-1]
					var out struct {
						Text string `json:"text"`
					}
					if last.Outcome == nil || json.Unmarshal([]byte(last.Outcome.GetResultJson()), &out) != nil ||
						json.Unmarshal([]byte(out.Text), &dumps[i]) != nil {
						errs[i] = fmt.Errorf("no environment dump in %v", sk.types())
					}
				}()
			}
			wg.Wait()
			for i := range n {
				if errs[i] != nil {
					t.Fatalf("workload %d: %v", i, errs[i])
				}
				scanWorkerEnv(t, i, dumps[i])
			}
		})
	}
}

// scanWorkerEnv fails on anything of the primary's in a worker's own environment.
func scanWorkerEnv(t *testing.T, workload int, env []string) {
	t.Helper()
	if len(env) == 0 {
		t.Fatalf("workload %d: empty environment dump", workload)
	}
	if err := helper.CheckEnvIsolation(env); err != nil {
		t.Fatalf("workload %d: %v", workload, err)
	}
	planeOn := false
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(key)
		if strings.Contains(kv, secretSentinel) {
			t.Fatalf("workload %d: a primary secret value reached the worker under %s", workload, key)
		}
		for _, s := range primarySecrets {
			if upper == s {
				t.Fatalf("workload %d: the worker carries %s", workload, key)
			}
		}
		for _, shape := range []string{"SECRET", "PASSWORD", "TOKEN", "CREDENTIAL", "PRIVATE_KEY", "_DSN", "API_KEY"} {
			if strings.Contains(upper, shape) {
				t.Fatalf("workload %d: a credential-shaped variable (%s) reached the worker", workload, key)
			}
		}
		if key == "UBAG_HELPER_PLANE" {
			planeOn = value == "1" // the last one wins, as in os/exec
		}
	}
	if !planeOn {
		t.Fatalf("workload %d: the worker is not in helper mode (the last UBAG_HELPER_PLANE must be 1)", workload)
	}
}

// ladder is the workload ladder of the acceptance plan; -short keeps the ends.
func ladder() []int {
	if testing.Short() {
		return []int{1, 5}
	}
	return []int{1, 2, 5, 10, 20}
}

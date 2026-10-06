package serve

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/helperauth/authtest"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

type dispatchEnv struct{ ca, cert, key, workload, adapters string }

func writeDispatchEnv(t *testing.T) dispatchEnv {
	t.Helper()
	dir := t.TempDir()
	ca := authtest.NewCA(t)
	cert, key := ca.Issue(t, authtest.Spec{NodeID: "primary-1", URIs: []string{"spiffe://ubag/primary/primary-1"}}).WriteFiles(t, dir, "client")
	return dispatchEnv{
		ca: ca.WriteCA(t, dir, "ca.pem"), cert: cert, key: key, workload: "w1",
		adapters: filepath.Join("..", "..", "..", "..", "adapters"), // the repository's real adapter registry
	}
}

func setDispatchEnv(t *testing.T, flag string, e dispatchEnv) {
	t.Helper()
	t.Setenv("UBAG_HELPER_DISPATCH", flag)
	t.Setenv("UBAG_EXECUTOR_ATTEMPTS", "true")
	t.Setenv("UBAG_HELPER_CA_FILE", e.ca)
	t.Setenv("UBAG_HELPER_CLIENT_CERT_FILE", e.cert)
	t.Setenv("UBAG_HELPER_CLIENT_KEY_FILE", e.key)
	t.Setenv("UBAG_HELPER_WORKLOAD_VERSION", e.workload)
	t.Setenv("UBAG_ADAPTERS_DIR", e.adapters)
}

func TestHelperRemoteFromEnv(t *testing.T) {
	good := writeDispatchEnv(t)
	missing := filepath.Join(t.TempDir(), "missing")
	plane := &helperPlane{} // only its presence matters here
	cases := []struct {
		name     string
		flag     string
		jobs     jobstore.Store
		plane    *helperPlane
		nodes    nodes.Store
		mutate   func(*dispatchEnv)
		attempts string // UBAG_EXECUTOR_ATTEMPTS override ("-" = leave as set)
		wantRun  bool
		wantErr  string
	}{
		{name: "unset is inert", flag: "", jobs: jobstore.NewMemoryStore()},
		{name: "off is inert even when configured", flag: "false", jobs: jobstore.NewMemoryStore(), plane: plane, nodes: nodes.NewMemoryStore()},
		{name: "off needs nothing at all", flag: "false"},
		{name: "needs the helper plane", flag: "true", jobs: jobstore.NewMemoryStore(), nodes: nodes.NewMemoryStore(), wantErr: "UBAG_HELPER_PLANE"},
		{name: "needs the node store", flag: "true", jobs: jobstore.NewMemoryStore(), plane: plane, wantErr: "UBAG_HELPER_NODES"},
		{name: "needs the attempt ledger flag", flag: "true", jobs: jobstore.NewMemoryStore(), plane: plane, nodes: nodes.NewMemoryStore(), attempts: "false", wantErr: "UBAG_EXECUTOR_ATTEMPTS"},
		{name: "needs a store with the ledger", flag: "true", jobs: struct{ jobstore.Store }{}, plane: plane, nodes: nodes.NewMemoryStore(), wantErr: "attempt ledger"},
		{name: "needs the CA", flag: "true", jobs: jobstore.NewMemoryStore(), plane: plane, nodes: nodes.NewMemoryStore(), mutate: func(e *dispatchEnv) { e.ca = "" }, wantErr: "UBAG_HELPER_CA_FILE"},
		{name: "needs the client certificate", flag: "true", jobs: jobstore.NewMemoryStore(), plane: plane, nodes: nodes.NewMemoryStore(), mutate: func(e *dispatchEnv) { e.cert = "" }, wantErr: "UBAG_HELPER_CLIENT_CERT_FILE"},
		{name: "needs the client key", flag: "true", jobs: jobstore.NewMemoryStore(), plane: plane, nodes: nodes.NewMemoryStore(), mutate: func(e *dispatchEnv) { e.key = "" }, wantErr: "UBAG_HELPER_CLIENT_KEY_FILE"},
		{name: "needs the workload version", flag: "true", jobs: jobstore.NewMemoryStore(), plane: plane, nodes: nodes.NewMemoryStore(), mutate: func(e *dispatchEnv) { e.workload = "" }, wantErr: "UBAG_HELPER_WORKLOAD_VERSION"},
		{name: "unreadable CA", flag: "true", jobs: jobstore.NewMemoryStore(), plane: plane, nodes: nodes.NewMemoryStore(), mutate: func(e *dispatchEnv) { e.ca = missing }, wantErr: "CA bundle"},
		{name: "unreadable client key pair", flag: "true", jobs: jobstore.NewMemoryStore(), plane: plane, nodes: nodes.NewMemoryStore(), mutate: func(e *dispatchEnv) { e.cert = missing }, wantErr: "key pair"},
		{name: "unreadable adapter registry", flag: "true", jobs: jobstore.NewMemoryStore(), plane: plane, nodes: nodes.NewMemoryStore(), mutate: func(e *dispatchEnv) { e.adapters = missing }, wantErr: "registry digest"},
		{name: "configured", flag: "true", jobs: jobstore.NewMemoryStore(), plane: plane, nodes: nodes.NewMemoryStore(), wantRun: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := good
			if tc.mutate != nil {
				tc.mutate(&env)
			}
			setDispatchEnv(t, tc.flag, env)
			if tc.attempts != "" {
				t.Setenv("UBAG_EXECUTOR_ATTEMPTS", tc.attempts)
			}
			runner, err := newHelperRemoteFromEnv(tc.jobs, tc.nodes, tc.plane, nil, executor.NoHelperPicker{})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if (runner != nil) != tc.wantRun {
				t.Fatalf("runner = %v, want runner: %v", runner, tc.wantRun)
			}
		})
	}
}

// Until a real picker is wired the runner places nothing: every job, whatever it
// is, runs on the local runner.
func TestHelperRemoteWithoutAPickerPlacesNothing(t *testing.T) {
	setDispatchEnv(t, "true", writeDispatchEnv(t))
	runner, err := newHelperRemoteFromEnv(jobstore.NewMemoryStore(), nodes.NewMemoryStore(), &helperPlane{}, nil, executor.NoHelperPicker{})
	if err != nil || runner == nil {
		t.Fatalf("runner = %v, err = %v", runner, err)
	}
	env := executor.DispatchEnvelope{
		JobID: "job_1", TenantID: "t1", AppID: "a1", TraceID: "tr1",
		Job: executor.DispatchJob{Target: "chatgpt_web", CommandType: "chat.prompt", Input: map[string]any{"prompt": "hi"}},
	}
	if placed, err := runner.Place(context.Background(), env); placed != nil || err != nil {
		t.Fatalf("Place = %v, %v, want no placement", placed, err)
	}
}

// The attempt budget is the local worker's; an invalid value fails dispatch
// startup, and with dispatch off the variable is not read at all.
func TestHelperRemoteReadsTheWorkerRuntimeOnlyWhenOn(t *testing.T) {
	good := writeDispatchEnv(t)
	t.Setenv("UBAG_WORKER_MAX_RUNTIME_MS", "bogus")

	setDispatchEnv(t, "true", good)
	if _, err := newHelperRemoteFromEnv(jobstore.NewMemoryStore(), nodes.NewMemoryStore(), &helperPlane{}, nil, executor.NoHelperPicker{}); err == nil ||
		!strings.Contains(err.Error(), "UBAG_WORKER_MAX_RUNTIME_MS") {
		t.Fatalf("err = %v, want the runtime variable named", err)
	}
	setDispatchEnv(t, "false", good)
	if runner, err := newHelperRemoteFromEnv(jobstore.NewMemoryStore(), nodes.NewMemoryStore(), &helperPlane{}, nil, executor.NoHelperPicker{}); runner != nil || err != nil {
		t.Fatalf("with dispatch off: %v, %v", runner, err)
	}
}

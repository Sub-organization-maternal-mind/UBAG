package helperclient

// Helper isolation acceptance (perf-fleet P7.3): the primary's RemoteWorkerRunner
// against TWO real ubag-helper services over real mutual TLS on loopback, with
// 1, 2, 5, 10 and 20 concurrent workloads from several tenants. Only the browser
// is a script (a helper.Runner); the dial, handshake, ledger, fenced ingest,
// lease renewals and the helper's own lease expiry are the production code.
//
// What is asserted (see docs/perf-fleet/slices/P7.3.md for the mapping to the
// plan's acceptance wording):
//
//   - zero cross-tenant events: no tenant's event listing, job, result or helper
//     request ever carries another tenant's data, and an idle tenant sees nothing;
//   - zero duplicate committed results: one completed event, contiguous
//     sequences, unique event ids and exactly one finished attempt per job, also
//     across a short network partition that heals (re-attach with replay);
//   - killed or partitioned helpers never produce a stale commit: the helper stops
//     its own attempt, the job is reassigned (before submission) or failed closed
//     (after it) and a late write from the old generation is fenced and changes
//     nothing.
//
// The env-dump scan of the helper's worker processes lives next to the runner
// (internal/helper/runner/isolation_test.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/helper"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// isoLadder is the workload ladder of the acceptance plan; -short keeps the ends.
func isoLadder() []int {
	if testing.Short() {
		return []int{1, 5}
	}
	return []int{1, 2, 5, 10, 20}
}

var tenantOf = [...]string{"tenant_a", "tenant_b"} // tenant_c is a bystander with no jobs

// ---- a network that can be cut ------------------------------------------------

// cutProxy forwards TCP to a helper and can sever it (a partition) and heal it.
type cutProxy struct {
	lis    net.Listener
	target string
	mu     sync.Mutex
	cut    bool
	conns  []net.Conn
}

func newCutProxy(t *testing.T, target string) *cutProxy {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &cutProxy{lis: lis, target: target}
	go p.accept()
	t.Cleanup(func() { _ = lis.Close(); p.Cut() })
	return p
}

func (p *cutProxy) Addr() string { return p.lis.Addr().String() }

func (p *cutProxy) accept() {
	for {
		c, err := p.lis.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.cut {
			p.mu.Unlock()
			_ = c.Close()
			continue
		}
		up, err := net.Dial("tcp", p.target)
		if err != nil {
			p.mu.Unlock()
			_ = c.Close()
			continue
		}
		p.conns = append(p.conns, c, up)
		p.mu.Unlock()
		go pipe(c, up)
		go pipe(up, c)
	}
}

func pipe(dst, src net.Conn) {
	_, _ = io.Copy(dst, src)
	_ = dst.Close()
	_ = src.Close()
}

// Cut drops every connection and refuses new ones until Heal.
func (p *cutProxy) Cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cut = true
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.conns = nil
}

func (p *cutProxy) Heal() {
	p.mu.Lock()
	p.cut = false
	p.mu.Unlock()
}

// ---- a helper node whose browser is a script ----------------------------------

type isoNode struct {
	rig      *rig      // nil for a helper that runs elsewhere (a container)
	proxy    *cutProxy // nil for the same
	nodeID   string    // the node identity the dial must authenticate
	endpoint string    // host:port the primary dials

	// forceOK makes every attempt on this node complete normally (the healthy node
	// a held-back job is reassigned to), whatever mode the job asks for.
	forceOK atomic.Bool

	mu      sync.Mutex
	specs   map[string][]helper.AttemptSpec // job id -> every attempt the helper was asked to run
	stopped map[string]error                // job id -> why a held attempt's context ended
	started chan string                     // job ids of held attempts that reached the hold (shared by the fleet)
}

type isoInput struct {
	Prompt string `json:"prompt"`
	Mode   string `json:"mode"`
}

const (
	modeOK       = "ok"
	modeHoldPre  = "hold_pre"  // started, then holds before the prompt is submitted
	modeHoldPost = "hold_post" // started and submitted, then holds
	modePaced    = "paced"     // slow tokens: long enough to straddle a short partition
)

func (n *isoNode) script(ctx context.Context, spec helper.AttemptSpec, emit helper.EmitFunc) error {
	n.mu.Lock()
	n.specs[spec.JobID] = append(n.specs[spec.JobID], spec)
	n.mu.Unlock()

	var in isoInput
	if err := json.Unmarshal([]byte(spec.InputJSON), &in); err != nil {
		return err
	}
	if n.forceOK.Load() {
		in.Mode = modeOK
	}
	send := func(typ helperv1.AttemptEventType, data string) error {
		return emit(helper.Event{Type: typ, DataJSON: data})
	}
	token := func(i int) error {
		return send(evToken, fmt.Sprintf(`{"delta":{"text":"tok-%d-%s"}}`, i, in.Prompt))
	}
	finish := func() error {
		return emit(helper.Event{Type: evTerminal, Outcome: &helperv1.AttemptOutcome{
			Status: helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED, Submitted: true,
			ResultJson: fmt.Sprintf(`{"type":"text","text":"done:%s"}`, in.Prompt)}})
	}

	if err := send(evStarted, `{"phase":"start"}`); err != nil {
		return err
	}
	switch in.Mode {
	case modeHoldPre, modeHoldPost:
		if in.Mode == modeHoldPost {
			if err := send(evSubmitted, ""); err != nil {
				return err
			}
		}
		n.started <- spec.JobID
		<-ctx.Done() // only the helper's own lease expiry or a shutdown ends it
		n.mu.Lock()
		n.stopped[spec.JobID] = context.Cause(ctx)
		n.mu.Unlock()
		return ctx.Err()
	case modePaced:
		if err := send(evSubmitted, ""); err != nil {
			return err
		}
		for i := range 8 {
			select {
			case <-time.After(150 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
			if err := token(i); err != nil {
				return err
			}
		}
	default:
		if err := send(evSubmitted, ""); err != nil {
			return err
		}
		for i := range 3 {
			if err := token(i); err != nil {
				return err
			}
		}
	}
	return finish()
}

func (n *isoNode) runsOf(jobID string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.specs[jobID])
}

func (n *isoNode) stoppedWhy(jobID string) (error, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	err, ok := n.stopped[jobID]
	return err, ok
}

// ---- the fleet ----------------------------------------------------------------

type pickerFunc func(context.Context, executor.HelperPickRequest) (executor.HelperPlacement, error)

func (f pickerFunc) Pick(ctx context.Context, req executor.HelperPickRequest) (executor.HelperPlacement, error) {
	return f(ctx, req)
}

type isoFleet struct {
	t       *testing.T
	store   *jobstore.MemoryStore
	nodes   []*isoNode
	runners []*executor.RemoteWorkerRunner
	jobSeq  int
	markers map[string]string // job id -> the marker its prompt carries
	tenants map[string]string // job id -> tenant
	// echo: the helper's browser is the test script, which returns "done:<marker>";
	// false for real workers, whose output is not under the test's control.
	echo bool
}

// helperMaxAttempts is the most attempts one helper admits (its own ceiling).
const helperMaxAttempts = 8

// helpersFor is how many helpers n workloads need (at least two, so isolation
// between helpers is always exercised); spare adds helpers that stay idle.
func helpersFor(n, spare int) int { return max(2, (n+helperMaxAttempts-1)/helperMaxAttempts) + spare }

// newIsoFleet starts helpers real ubag-helper services (each with its own CA,
// certificate and registry) and a primary store shared by one runner per helper.
func newIsoFleet(t *testing.T, helpers int, mut ...func(*executor.RemoteConfig)) *isoFleet {
	t.Helper()
	f := &isoFleet{t: t, store: jobstore.NewMemoryStore(), markers: map[string]string{}, tenants: map[string]string{}, echo: true}
	started := make(chan string, 256)
	for range helpers {
		n := &isoNode{specs: map[string][]helper.AttemptSpec{}, stopped: map[string]error{}, started: started, nodeID: testNode}
		n.rig = newRig(t, rigOptions{
			runner: helper.RunnerFunc(n.script),
			cfg:    func(c *helper.Config) { c.MaxAttempts = helperMaxAttempts },
		})
		n.proxy = newCutProxy(t, n.rig.addr)
		n.endpoint = n.proxy.Addr()
		f.addNode(t, n, testDigest, "w1", n.rig.dialer(n.rig.primary, nil, nil), mut...)
	}
	return f
}

// addNode adds a helper (n.nodeID at n.endpoint, reached through dialer) and the
// primary's runner for it.
func (f *isoFleet) addNode(t *testing.T, n *isoNode, digest, workload string, dialer *Dialer, mut ...func(*executor.RemoteConfig)) {
	t.Helper()
	cfg := executor.RemoteConfig{
		Store: f.store, WorkloadVersion: workload, RegistryDigest: digest,
		Picker: pickerFunc(func(_ context.Context, req executor.HelperPickRequest) (executor.HelperPlacement, error) {
			return executor.HelperPlacement{NodeID: n.nodeID, Endpoint: n.endpoint, ProfileRef: "pr_" + req.JobID}, nil
		}),
		Dialer: executor.HelperDialFunc(func(ctx context.Context, p executor.HelperPlacement) (executor.HelperConn, error) {
			conn, err := dialer.Dial(ctx, p.NodeID, p.Endpoint)
			if err != nil {
				return nil, err
			}
			return conn, nil
		}),
		RetryDelay: 20 * time.Millisecond, ReconnectDelay: 20 * time.Millisecond,
	}
	for _, m := range mut {
		m(&cfg)
	}
	runner, err := executor.NewRemoteWorkerRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.nodes = append(f.nodes, n)
	f.runners = append(f.runners, runner)
}

// job creates a job of tenant whose prompt carries a marker unique to it.
func (f *isoFleet) job(tenant, mode string) (jobstore.Job, executor.DispatchEnvelope) {
	f.t.Helper()
	f.jobSeq++
	marker := fmt.Sprintf("[mk:%s:%d]", strings.ToUpper(strings.TrimPrefix(tenant, "tenant_")), f.jobSeq)
	input := map[string]any{"prompt": marker}
	if mode != "" {
		input["mode"] = mode
	}
	job, err := f.store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: tenant, AppID: "app_" + tenant, IdempotencyKey: fmt.Sprintf("iso_key_%08d_%d", f.jobSeq, time.Now().UnixNano()),
		Target: "mock", CommandType: "submit", Input: input, TraceID: fmt.Sprintf("trace_iso_%d", f.jobSeq),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	f.markers[job.ID], f.tenants[job.ID] = marker, tenant
	return job, executor.EnvelopeFromJob(job)
}

// run places and runs one attempt on helper idx and returns what Run returned.
func (f *isoFleet) run(ctx context.Context, idx int, env executor.DispatchEnvelope) error {
	placed, err := f.runners[idx].Place(ctx, env)
	if err != nil || placed == nil {
		return fmt.Errorf("Place = %v, %v", placed, err)
	}
	return f.runners[idx].Run(ctx, env, placed)
}

// runAll runs every env concurrently, env k on helper pick(k), and returns the errors.
func (f *isoFleet) runAll(ctx context.Context, envs []executor.DispatchEnvelope, pick func(k int) int) []error {
	errs := make([]error, len(envs))
	var wg sync.WaitGroup
	for k := range envs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[k] = f.run(ctx, pick(k), envs[k])
		}()
	}
	wg.Wait()
	return errs
}

func (f *isoFleet) events(tenant string) []jobstore.Event {
	f.t.Helper()
	events, err := f.store.ListAllEvents(context.Background(), jobstore.EventListFilter{TenantID: tenant, Limit: 10000})
	if err != nil {
		f.t.Fatal(err)
	}
	return events
}

func (f *isoFleet) jobEvents(id string) []jobstore.Event {
	f.t.Helper()
	events, _, err := f.store.ListEvents(context.Background(), id, 0, 10000)
	if err != nil {
		f.t.Fatal(err)
	}
	return events
}

func (f *isoFleet) attempts(id string) []jobstore.Attempt {
	f.t.Helper()
	list, err := f.store.ListAttempts(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return list
}

// settled reports whether a job's status is an end of its (single) attempt: the
// scripted helpers complete every job; a real worker with no provider behind it
// ends its jobs failed, which isolation must hold for just the same.
func (f *isoFleet) settled(st jobstore.Status) bool {
	if f.echo {
		return st == jobstore.StatusCompleted
	}
	return jobstore.TerminalStatus(st) || st == jobstore.StatusFailedRetryable
}

// terminalEvent reports whether ev ends a job's attempt (the one result).
func (f *isoFleet) terminalEvent(ev jobstore.Event) bool {
	if f.echo {
		return ev.Type == "completed"
	}
	switch ev.Type {
	case "completed", "completed_with_warnings", "failed", "timed_out", "cancelled", "blocked":
		return true
	}
	return false
}

func (f *isoFleet) status(id string) jobstore.Status {
	f.t.Helper()
	job, found, err := f.store.Get(context.Background(), id)
	if err != nil || !found {
		f.t.Fatalf("Get %s = %v, %v", id, found, err)
	}
	return job.Status
}

var markerRe = regexp.MustCompile(`\[mk:[A-Z]:\d+\]`)

// ---- 1. cross-tenant and duplicate isolation under load ------------------------

func TestIsolationNoCrossTenantLeakAndNoDuplicateResultsUnderLoad(t *testing.T) {
	for _, n := range isoLadder() {
		t.Run(fmt.Sprintf("%d_workloads", n), func(t *testing.T) {
			f := newIsoFleet(t, helpersFor(n, 0))
			envs := make([]executor.DispatchEnvelope, n)
			ids := make([]string, n)
			for k := range n {
				job, env := f.job(tenantOf[k%2], modeOK)
				envs[k], ids[k] = env, job.ID
			}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()
			for k, err := range f.runAll(ctx, envs, func(k int) int { return k % len(f.nodes) }) {
				if err != nil {
					t.Fatalf("job %d: Run = %v", k, err)
				}
			}
			f.assertIsolated(t, ids, 1)

			// The helpers saw every job exactly once and nothing of the tenancy.
			for k, id := range ids {
				for h, node := range f.nodes {
					want := 0
					if h == k%len(f.nodes) {
						want = 1
					}
					if got := node.runsOf(id); got != want {
						t.Fatalf("job %s ran %d times on helper %d, want %d", id, got, h, want)
					}
				}
			}
		})
	}
}

// assertIsolated checks every property of a fleet in which all ids completed on
// their first attempt (generation want).
func (f *isoFleet) assertIsolated(t *testing.T, ids []string, wantGeneration uint64) {
	t.Helper()
	own := map[string][]string{} // tenant -> its job ids (every job of the fleet, not only this call's)
	for id, tenant := range f.tenants {
		own[tenant] = append(own[tenant], id)
	}

	for _, id := range ids {
		tenant, marker := f.tenants[id], f.markers[id]
		if got := f.status(id); !f.settled(got) {
			t.Fatalf("job %s status = %s", id, got)
		}
		job, _, _ := f.store.Get(context.Background(), id)
		raw, _ := json.Marshal(job.Result)
		if f.echo && !strings.Contains(string(raw), "done:"+marker) {
			t.Fatalf("job %s result = %s, want its own marker %s", id, raw, marker)
		}
		for _, m := range markerRe.FindAllString(string(raw), -1) {
			if m != marker {
				t.Fatalf("job %s result carries the foreign marker %s", id, m)
			}
		}

		// A foreign tenant cannot even look the job up.
		for _, other := range []string{"tenant_a", "tenant_b", "tenant_c"} {
			_, found, _ := f.store.GetScoped(context.Background(), id, other, "app_"+other)
			if found != (other == tenant) {
				t.Fatalf("GetScoped(%s) by %s found=%v", id, other, found)
			}
		}

		// Exactly one result: one terminal event, contiguous sequences, unique ids, one finished attempt.
		events, completed, seen := f.jobEvents(id), 0, map[string]bool{}
		for i, ev := range events {
			if f.terminalEvent(ev) {
				completed++
			}
			if ev.Sequence != i+1 || seen[ev.ID] {
				t.Fatalf("job %s: event %d has sequence %d or a repeated id %q", id, i, ev.Sequence, ev.ID)
			}
			seen[ev.ID] = true
		}
		if completed != 1 {
			t.Fatalf("job %s: %d terminal events, want exactly 1", id, completed)
		}
		attempts := f.attempts(id)
		if len(attempts) != 1 || attempts[0].State != jobstore.AttemptFinished || attempts[0].Generation != wantGeneration {
			t.Fatalf("job %s attempts = %+v", id, attempts)
		}
	}

	// Every tenant's event listing holds its own jobs' events and only its own markers.
	total := 0
	for _, tenant := range []string{"tenant_a", "tenant_b", "tenant_c"} {
		mine := map[string]bool{}
		for _, id := range own[tenant] {
			mine[id] = true
		}
		events := f.events(tenant)
		if len(own[tenant]) == 0 && len(events) != 0 {
			t.Fatalf("%s has no jobs but sees %d events", tenant, len(events))
		}
		for _, ev := range events {
			if !mine[ev.JobID] {
				t.Fatalf("%s sees an event of job %s", tenant, ev.JobID)
			}
			raw, _ := json.Marshal(ev.Data)
			for _, m := range markerRe.FindAllString(string(raw), -1) {
				if m != f.markers[ev.JobID] {
					t.Fatalf("%s: event %s of job %s carries the foreign marker %s", tenant, ev.ID, ev.JobID, m)
				}
			}
			if f.terminalEvent(ev) {
				total++
			}
		}
		if jobs, _ := f.store.List(context.Background(), jobstore.ListFilter{TenantID: tenant, Limit: 1000}); len(jobs) != len(own[tenant]) {
			t.Fatalf("%s lists %d jobs, owns %d", tenant, len(jobs), len(own[tenant]))
		}
	}
	if total != len(f.tenants) {
		t.Fatalf("%d terminal events for %d jobs: a result was committed twice or lost", total, len(f.tenants))
	}

	// What the helpers were sent carries the job's own prompt and no tenancy at all.
	for _, node := range f.nodes {
		node.mu.Lock()
		for id, specs := range node.specs {
			for _, sp := range specs {
				raw, _ := json.Marshal(sp)
				s := string(raw)
				if strings.Contains(s, "tenant_") || strings.Contains(s, "app_") ||
					len(markerRe.FindAllString(s, -1)) != 1 || !strings.Contains(s, f.markers[id]) || sp.IdentityRef != "pr_"+id {
					node.mu.Unlock()
					t.Fatalf("the helper was sent %s for job %s", s, id)
				}
			}
		}
		node.mu.Unlock()
	}
}

// ---- 2. a partition that heals commits every event once ------------------------

// The network between the primary and one of the helpers drops for less than the
// lease and comes back: the primary re-attaches and the helper replays. Each event lands
// once and the job ends with one result.
func TestIsolationHealedPartitionCommitsEveryEventExactlyOnce(t *testing.T) {
	for _, n := range isoLadder() {
		t.Run(fmt.Sprintf("%d_workloads", n), func(t *testing.T) {
			f := newIsoFleet(t, helpersFor(n, 0), func(c *executor.RemoteConfig) {
				c.LeaseTTL, c.RenewEvery, c.MaxClockSkew = 4*time.Second, 500*time.Millisecond, 100*time.Millisecond
			})
			envs := make([]executor.DispatchEnvelope, n)
			ids := make([]string, n)
			for k := range n {
				job, env := f.job(tenantOf[k%2], modePaced)
				envs[k], ids[k] = env, job.ID
			}
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()

			errs := make(chan []error, 1)
			go func() { errs <- f.runAll(ctx, envs, func(k int) int { return k % len(f.nodes) }) }()

			time.Sleep(400 * time.Millisecond) // the attempts are streaming
			f.nodes[0].proxy.Cut()
			time.Sleep(900 * time.Millisecond) // a partition well inside the 4 s lease
			f.nodes[0].proxy.Heal()

			for k, err := range <-errs {
				if err != nil {
					t.Fatalf("job %d: Run = %v", k, err)
				}
			}
			f.assertIsolated(t, ids, 1)
			for _, id := range ids {
				tokens := 0
				for _, ev := range f.jobEvents(id) {
					if ev.Type == "token" {
						tokens++
					}
				}
				if tokens != 8 {
					t.Fatalf("job %s committed %d token events, want 8 (a replay duplicated or dropped some)", id, tokens)
				}
			}
		})
	}
}

// ---- 3. killed or partitioned helpers never produce a stale commit --------------

// lostHow takes a helper away mid-attempt.
type lostHow struct {
	name string
	do   func(t *testing.T, n *isoNode)
}

var lostHows = []lostHow{
	{"killed", func(t *testing.T, n *isoNode) {
		// The helper process dies: its listener goes and its attempts are cancelled.
		n.rig.grpcSrv.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = n.rig.srv.Shutdown(ctx)
	}},
	{"partitioned", func(t *testing.T, n *isoNode) {
		// The helper keeps running, unreachable; only its own lease timer stops it.
		n.proxy.Cut()
	}},
}

func TestIsolationLostHelperNeverProducesAStaleCommit(t *testing.T) {
	for _, how := range lostHows {
		for _, submitted := range []bool{false, true} {
			phase := "before_submission"
			if submitted {
				phase = "after_submission"
			}
			// The four (how, phase) groups run side by side; the ladder inside each is sequential.
			t.Run(how.name+"/"+phase, func(t *testing.T) {
				t.Parallel()
				for _, n := range isoLadder() {
					t.Run(fmt.Sprintf("%d_workloads", n), func(t *testing.T) { lostHelperScenario(t, how, submitted, n) })
				}
			})
		}
	}
}

func lostHelperScenario(t *testing.T, how lostHow, submitted bool, n int) {
	// Every helper but the last is lost; the last one is healthy and takes reassigned jobs.
	//
	// The lease only has to be short enough that a lost helper's lease lapses
	// inside this test (see the reassignment step below); it does not have to be
	// shorter than the dispatcher takes to acquire, ship and start an attempt.
	// A flat 1.5s was fine for one or two workloads and too tight for ten on a
	// loaded -race runner: the attempts were fenced as attempt_expired before
	// the helper ever saw them, so the "did not all reach the helper" wait
	// expired. That is a harness limit, not the property under test, so scale
	// the TTL with the workload count and keep the 1/6 renew ratio. The sibling
	// scenario above already uses 4s for the same reason.
	leaseTTL := time.Duration(1500+250*n) * time.Millisecond
	f := newIsoFleet(t, helpersFor(n, 1), func(c *executor.RemoteConfig) {
		c.LeaseTTL, c.RenewEvery, c.MaxClockSkew = leaseTTL, leaseTTL/6, 100*time.Millisecond
	})
	victims, healthy := f.nodes[:len(f.nodes)-1], len(f.nodes)-1
	mode := modeHoldPre
	if submitted {
		mode = modeHoldPost
	}
	envs := make([]executor.DispatchEnvelope, n)
	ids := make([]string, n)
	for k := range n {
		job, env := f.job(tenantOf[k%2], mode)
		envs[k], ids[k] = env, job.ID
	}
	// Every wait in this scenario scales with n, because it runs n concurrent
	// attempts and the numbers tuned for one workload are not enough for ten on
	// an oversubscribed runner (go test runs several package binaries at once,
	// each with its own GOMAXPROCS, on a 2-core box). They are preconditions for
	// the assertions below, not the assertions: what an attempt must never do is
	// still checked, and it is still checked within a bounded time.
	budget := time.Duration(90+15*n) * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()

	errs := make(chan []error, 1)
	go func() { errs <- f.runAll(ctx, envs, func(k int) int { return k % len(victims) }) }()
	startDeadline := time.After(budget / 3) // one shared deadline, not n of them
	for range n {
		select {
		case <-f.nodes[0].started: // one channel shared by the fleet
		case <-startDeadline:
			t.Fatal("the attempts did not all reach the helper")
		}
	}
	time.Sleep(100 * time.Millisecond) // let the primary read the events
	for _, v := range victims {
		how.do(t, v)
	}

	var runErrs []error
	select {
	case runErrs = <-errs:
	case <-time.After(budget): // runAll is bounded by ctx, which is `budget`
		t.Fatal("Run did not return after the helper was lost")
	}
	for k, err := range runErrs {
		var held *executor.HelperRetryError
		switch {
		case submitted && !errors.Is(err, executor.ErrAmbiguous):
			t.Fatalf("job %d: Run = %v, want ErrAmbiguous (failed closed)", k, err)
		case !submitted && (!errors.As(err, &held) || held.Reason != "helper_lost"):
			t.Fatalf("job %d: Run = %v, want a job held back for reassignment", k, err)
		}
	}

	// The helper itself stopped every attempt once it lost its lease (or was killed).
	for k, id := range ids {
		deadline := time.Now().Add(15 * time.Second)
		for {
			if why, ok := victims[k%len(victims)].stoppedWhy(id); ok {
				if why == nil {
					t.Fatalf("job %s: the helper's attempt ended without a cause", id)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %s: the lost helper kept the attempt running", id)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Reassign (healthy helper 1) or refuse to replay. The first lease has to lapse
	// before the ledger lets anything else near the job.
	f.nodes[healthy].forceOK.Store(true)
	for k, id := range ids {
		deadline := time.Now().Add(30 * time.Second)
		for {
			err := f.run(ctx, healthy, envs[k])
			var held *executor.HelperRetryError
			if submitted && errors.Is(err, executor.ErrAmbiguous) {
				break
			}
			if !submitted && err == nil {
				break
			}
			if !errors.As(err, &held) || time.Now().After(deadline) {
				t.Fatalf("job %s: second delivery = %v (submitted=%v)", id, err, submitted)
			}
			time.Sleep(100 * time.Millisecond)
		}
		if submitted && f.nodes[healthy].runsOf(id) != 0 {
			t.Fatalf("job %s was replayed on the healthy helper after its prompt was submitted", id)
		}
	}

	// The sweeper (executor.StaleJobReaper) reaps lapsed leases in production.
	if submitted {
		if _, err := f.store.ExpireAttempts(context.Background(), 0); err != nil {
			t.Fatal(err)
		}
	}

	// The late writer: the lost helper's old generation tries to land a result.
	for k, id := range ids {
		before, eventsBefore := f.status(id), len(f.jobEvents(id))
		old := f.attempts(id)[0]
		if old.Generation != 1 || old.NodeID != testNode {
			t.Fatalf("job %s: attempts = %+v", id, f.attempts(id))
		}
		stale := jobstore.WorkerEvent{
			EventID: "stale-" + id, JobID: id, APIVersion: "2026-05-22", Type: "completed", Sequence: 9999,
			TraceID: fmt.Sprintf("trace_iso_%d", k+1),
			Data:    map[string]any{"result": map[string]any{"type": "text", "text": "STALE " + f.markers[id]}},
		}
		if _, err := f.store.CommitEvents(context.Background(), old.Ref(), []jobstore.WorkerEvent{stale}); !errors.Is(err, jobstore.ErrAttemptFenced) {
			t.Fatalf("job %s: a stale commit was not fenced: %v", id, err)
		}
		_, err := executor.OpenHelperIngest(context.Background(), executor.HelperIngestConfig{Store: f.store}, executor.HelperIngestBinding{
			TenantID: f.tenants[id], AppID: "app_" + f.tenants[id], JobID: id, AttemptID: old.AttemptID, Generation: old.Generation, NodeID: testNode,
		})
		if !errors.Is(err, jobstore.ErrAttemptFenced) {
			t.Fatalf("job %s: a late ingest session was not fenced: %v", id, err)
		}
		if submitted {
			if f.status(id) == jobstore.StatusCompleted || len(f.jobEvents(id)) != eventsBefore {
				t.Fatalf("job %s: a stale write changed a job that must fail closed", id)
			}
		} else if f.status(id) != before || len(f.jobEvents(id)) != eventsBefore {
			t.Fatalf("job %s: a stale write changed the job", id)
		}
		for _, ev := range f.jobEvents(id) {
			if raw, _ := json.Marshal(ev.Data); strings.Contains(string(raw), "STALE") {
				t.Fatalf("job %s: stale data was committed: %s", id, raw)
			}
		}
	}

	if !submitted {
		// Reassigned jobs finished once, at generation 2, on the healthy helper.
		for _, id := range ids {
			attempts := f.attempts(id)
			if len(attempts) != 2 || attempts[0].State != jobstore.AttemptExpired || attempts[1].Generation != 2 || attempts[1].State != jobstore.AttemptFinished {
				t.Fatalf("job %s attempts = %+v", id, attempts)
			}
		}
		f.assertOneResultEach(t, ids)
	}
}

// assertOneResultEach is assertIsolated for jobs that finished on a later
// generation: same isolation and uniqueness checks without the helper-spec scan
// of the lost helper's (held) attempts.
func (f *isoFleet) assertOneResultEach(t *testing.T, ids []string) {
	t.Helper()
	completed := 0
	for _, tenant := range []string{"tenant_a", "tenant_b", "tenant_c"} {
		mine := map[string]bool{}
		for _, id := range ids {
			if f.tenants[id] == tenant {
				mine[id] = true
			}
		}
		for _, ev := range f.events(tenant) {
			if !mine[ev.JobID] {
				t.Fatalf("%s sees an event of job %s", tenant, ev.JobID)
			}
			raw, _ := json.Marshal(ev.Data)
			for _, m := range markerRe.FindAllString(string(raw), -1) {
				if m != f.markers[ev.JobID] {
					t.Fatalf("%s: event %s carries the foreign marker %s", tenant, ev.ID, m)
				}
			}
			if ev.Type == "completed" {
				completed++
			}
		}
	}
	if completed != len(ids) {
		t.Fatalf("%d completed events for %d reassigned jobs", completed, len(ids))
	}
	for _, id := range ids {
		if got := f.status(id); got != jobstore.StatusCompleted {
			t.Fatalf("job %s status = %s", id, got)
		}
	}
}

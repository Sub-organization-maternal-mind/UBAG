package executor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
	voice "github.com/ubag/ubag/apps/gateway/internal/voice"
)

// The job side of P5.5: a live voice session on a browser excludes the jobs that
// drive that browser (and a running job excludes a voice admission), through the
// real voice store, the real topology, the real ConcurrencyRegistry and the real
// consumer. The voice-handler side is in httpapi/voice_lane_test.go.

const (
	laneWorkerEndpoint   = "http://browser:9222/"                         // the gateway/worker env spelling
	laneTopologyEndpoint = "ws://Browser:9222/devtools/browser/some-guid" // what the worker reports
)

type laneRig struct {
	t        *testing.T
	voice    *voice.MemoryStore
	registry *topology.ConcurrencyRegistry
	jobs     jobstore.Store
	consumer *WorkerConsumer
	runs     atomic.Int32
	lane     string
	seq      int
}

func newLaneRig(t *testing.T) *laneRig {
	t.Helper()
	t.Setenv("UBAG_REMOTE_BROWSER_ENDPOINT", laneWorkerEndpoint)

	topo := topology.NewMemoryStore()
	topo.AddInstance(topology.BrowserInstance{InstanceID: "browser-1", TenantID: "tenant_v", State: "ready", RemoteEndpoint: laneTopologyEndpoint})
	topo.AddInstance(topology.BrowserInstance{InstanceID: "browser-2", TenantID: "tenant_v", State: "ready", RemoteEndpoint: "http://other:9222"})
	r := &laneRig{
		t:        t,
		voice:    voice.NewMemoryStore(),
		registry: topology.NewConcurrencyRegistry(),
		jobs:     jobstore.NewMemoryStore(),
		lane:     topology.BrowserLaneKey(laneWorkerEndpoint),
	}
	r.consumer = &WorkerConsumer{
		Jobs:        r.jobs,
		Concurrency: r.registry,
		VoiceLanes:  &voice.LaneProbe{Store: r.voice, Topology: topo},

		VoiceLaneRetryDelay: 40 * time.Millisecond,
		Runner: WorkerRunFunc(func(_ context.Context, env DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			r.runs.Add(1)
			return completedEvents(env), nil
		}),
	}
	return r
}

// startVoice leases browser-1 (and so the lane) to a live session.
func (r *laneRig) startVoice(id string) {
	r.t.Helper()
	got, err := r.voice.Reserve(r.t.Context(), voice.ReserveRequest{
		SessionID: id, TenantID: "tenant_v", AppID: "app_v", Target: "chatgpt_web",
		Placements: []voice.Placement{{Identity: "acct-1", Instance: "browser-1"}}, LeaseTTL: time.Hour,
	})
	if err != nil || got.Status != voice.StatusConnecting {
		r.t.Fatalf("voice session %s: %+v err=%v", id, got, err)
	}
}

// runJob leases one job through the consumer and reports what happened to it.
func (r *laneRig) runJob(target, command string) (*fakeWorkerLease, jobstore.Job) {
	r.t.Helper()
	r.seq++
	job, err := r.jobs.Create(r.t.Context(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", IdempotencyKey: "lane_job_key_" + string(rune('a'+r.seq)),
		Target: target, CommandType: command, Input: map[string]any{"prompt": "hi"}, TraceID: "trace_lane",
	})
	if err != nil {
		r.t.Fatal(err)
	}
	lease := &fakeWorkerLease{jobID: job.ID, leaseID: "lease_lane", envelope: EnvelopeFromJob(job)}
	r.consumer.Queue = singleLeaseQueue{lease: lease}
	if processed, err := r.consumer.RunOnce(r.t.Context()); err != nil || !processed {
		r.t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	final, _, _ := r.jobs.Get(r.t.Context(), job.ID)
	return lease, final
}

func (r *laneRig) expectRan(lease *fakeWorkerLease, job jobstore.Job, what string) {
	r.t.Helper()
	if !lease.completed || lease.retried || job.Status != jobstore.StatusCompleted {
		r.t.Fatalf("%s: the job must run to completion: lease=%+v status=%s", what, lease, job.Status)
	}
}

func (r *laneRig) expectHeldBack(lease *fakeWorkerLease, job jobstore.Job, runsBefore int32, what string) {
	r.t.Helper()
	if !lease.retried || lease.completed || lease.failed || lease.cancelled || lease.poisoned {
		r.t.Fatalf("%s: a held-back job is only nacked back to the queue: %+v", what, lease)
	}
	if r.runs.Load() != runsBefore {
		r.t.Fatalf("%s: the job reached the worker under a live voice session", what)
	}
	if job.Status != jobstore.StatusQueued {
		r.t.Fatalf("%s: a job that never started must stay queued, is %s", what, job.Status)
	}
}

func (r *laneRig) holders(kind topology.LaneKind) int {
	r.t.Helper()
	n, err := r.registry.LaneHolders(r.t.Context(), kind, r.lane)
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

func TestVoiceSessionHoldsBackJobsOnItsBrowserAndFreesThemWhenItEnds(t *testing.T) {
	r := newLaneRig(t)

	lease, job := r.runJob("chatgpt_web", "submit")
	r.expectRan(lease, job, "no voice session")
	if r.holders(topology.LaneJob) != 0 {
		t.Fatal("a finished job must leave the lane")
	}

	r.startVoice("vs-1")
	before := r.runs.Load()
	begin := time.Now()
	lease, job = r.runJob("chatgpt_web", "submit")
	r.expectHeldBack(lease, job, before, "text job under a live voice session")
	if time.Since(begin) < 35*time.Millisecond {
		t.Fatal("the nack must be delayed, or the file spool would spin on it")
	}
	if r.holders(topology.LaneJob) != 0 {
		t.Fatal("a held-back job must not stay registered on the lane")
	}
	// A file job is the same: the exclusion is about the browser, not the input.
	lease, job = r.runJob("gemini_web", "submit")
	r.expectHeldBack(lease, job, before, "a job for another provider on the same browser")

	if err := r.voice.Terminate(t.Context(), "tenant_v", "vs-1", time.Now().UTC(), "terminated_by_client"); err != nil {
		t.Fatal(err)
	}
	lease, job = r.runJob("chatgpt_web", "submit")
	r.expectRan(lease, job, "after the voice session ended")
}

func TestJobsThatShareNoBrowserWithTheSessionAreNotHeldBack(t *testing.T) {
	r := newLaneRig(t)
	r.startVoice("vs-1")

	for _, c := range []struct{ target, command, what string }{
		{"mock", "submit", "the mock target"},
		{"antigravity_sdk", "submit", "an SDK target"},
		{"chatgpt_web", "voice.activate", "the session's own control job"},
		{"chatgpt_web", "voice.deactivate", "the session's own teardown job"},
	} {
		lease, job := r.runJob(c.target, c.command)
		r.expectRan(lease, job, c.what)
	}

	// A session on ANOTHER browser leaves this one alone.
	t.Setenv("UBAG_REMOTE_BROWSER_ENDPOINT", "http://third:9222")
	lease, job := r.runJob("chatgpt_web", "submit")
	r.expectRan(lease, job, "a job on a browser no session holds")
	// And with no remote browser each job launches its own: nothing to share.
	t.Setenv("UBAG_REMOTE_BROWSER_ENDPOINT", "")
	lease, job = r.runJob("chatgpt_web", "submit")
	r.expectRan(lease, job, "a job with no shared browser")
}

// A terminated session whose provider UI is still being torn down keeps the lane
// until the deactivate ack frees it.
func TestTerminatingHoldKeepsJobsOffTheBrowser(t *testing.T) {
	r := newLaneRig(t)
	r.startVoice("vs-1")
	if err := r.voice.BeginTerminate(t.Context(), "tenant_v", "vs-1", time.Now().UTC(), "terminated_by_client", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	before := r.runs.Load()
	lease, job := r.runJob("chatgpt_web", "submit")
	r.expectHeldBack(lease, job, before, "job during the terminating hold")

	if err := r.voice.ReleaseHold(t.Context(), "tenant_v", "vs-1", voice.LeaseFence{}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	lease, job = r.runJob("chatgpt_web", "submit")
	r.expectRan(lease, job, "after the deactivate ack")
}

func TestJobFailsClosedWhenTheLaneCannotBeChecked(t *testing.T) {
	r := newLaneRig(t)
	r.consumer.VoiceLanes = probeFunc(func(context.Context, string) (bool, error) { return false, errors.New("voice store down") })
	lease, job := r.runJob("chatgpt_web", "submit")
	r.expectHeldBack(lease, job, 0, "job with an unreadable voice store")
	if r.holders(topology.LaneJob) != 0 {
		t.Fatal("a failed check must not leave the job registered")
	}
}

// With cross-replica execution leases on, a held-back job gives its lease up
// before it goes back to the queue, so another consumer's redelivery is not
// mistaken for a duplicate while it waits.
func TestHeldBackJobReleasesItsExecutionLeaseBeforeTheRetry(t *testing.T) {
	r := newLaneRig(t)
	backend := execLeaseBackend(t)
	r.consumer.ExecLeases = backend
	r.startVoice("vs-1")
	job, err := r.jobs.Create(t.Context(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", IdempotencyKey: "lane_exec_key_0001",
		Target: "chatgpt_web", CommandType: "submit", Input: map[string]any{"prompt": "hi"}, TraceID: "trace_lane",
	})
	if err != nil {
		t.Fatal(err)
	}
	lease := &overloadProbeLease{
		fakeWorkerLease: fakeWorkerLease{jobID: job.ID, leaseID: "lease_exec", envelope: EnvelopeFromJob(job)},
		backend:         backend,
	}
	r.consumer.Queue = singleLeaseQueue{lease: lease}
	if processed, err := r.consumer.RunOnce(t.Context()); err != nil || !processed {
		t.Fatalf("RunOnce processed=%v err=%v", processed, err)
	}
	if !lease.retried || r.runs.Load() != 0 {
		t.Fatalf("the job must be nacked, not run: %+v runs=%d", lease.fakeWorkerLease, r.runs.Load())
	}
	if !lease.execFreeAtRetry {
		t.Fatal("the execution lease must be released before the lease goes back to the queue")
	}
}

// UBAG_VOICE_LANE_EXCLUSION unset leaves VoiceLanes nil: today's behaviour.
func TestNoProbeMeansNoExclusion(t *testing.T) {
	r := newLaneRig(t)
	r.startVoice("vs-1")
	r.consumer.VoiceLanes = nil
	lease, job := r.runJob("chatgpt_web", "submit")
	r.expectRan(lease, job, "voice exclusion switched off")
}

// A voice admission in flight holds the job back before the voice store shows
// anything: the registry half of the register-then-look protocol.
func TestVoiceAdmissionInFlightHoldsJobsBack(t *testing.T) {
	r := newLaneRig(t)
	admission, err := r.registry.EnterLane(t.Context(), topology.LaneVoice, r.lane)
	if err != nil {
		t.Fatal(err)
	}
	lease, job := r.runJob("chatgpt_web", "submit")
	r.expectHeldBack(lease, job, 0, "job during a voice admission")
	admission.Release()
	lease, job = r.runJob("chatgpt_web", "submit")
	r.expectRan(lease, job, "after the admission")
}

// While a job runs it is visible on its lane (that is what a voice admission
// looks at), and every way out of RunOnce leaves the lane.
func TestRunningJobIsRegisteredOnItsLaneUntilItEnds(t *testing.T) {
	r := newLaneRig(t)
	var during atomic.Int32
	r.consumer.Runner = WorkerRunFunc(func(_ context.Context, env DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
		n, _ := r.registry.LaneHolders(context.Background(), topology.LaneJob, r.lane)
		during.Store(int32(n))
		if env.Job.Options["boom"] == true {
			return nil, errors.New("worker crashed")
		}
		return completedEvents(env), nil
	})
	lease, job := r.runJob("chatgpt_web", "submit")
	r.expectRan(lease, job, "plain run")
	if during.Load() != 1 || r.holders(topology.LaneJob) != 0 {
		t.Fatalf("registered during the run=%d, after=%d; want 1 and 0", during.Load(), r.holders(topology.LaneJob))
	}

	r.seq++
	failing, err := r.jobs.Create(t.Context(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", IdempotencyKey: "lane_fail_key_0001",
		Target: "chatgpt_web", CommandType: "submit", Input: map[string]any{"prompt": "hi"},
		Options: map[string]any{"boom": true}, TraceID: "trace_lane",
	})
	if err != nil {
		t.Fatal(err)
	}
	failLease := &fakeWorkerLease{jobID: failing.ID, leaseID: "lease_fail", envelope: EnvelopeFromJob(failing)}
	r.consumer.Queue = singleLeaseQueue{lease: failLease}
	if _, err := r.consumer.RunOnce(t.Context()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !failLease.failed || r.holders(topology.LaneJob) != 0 {
		t.Fatalf("a crashed job must leave the lane: failed=%v holders=%d", failLease.failed, r.holders(topology.LaneJob))
	}
}

// A shared lane registration that lapses mid-run (the store was unreachable for
// its whole TTL) cancels the run, as a lost execution lease does.
func TestLapsedSharedLaneRegistrationCancelsTheRun(t *testing.T) {
	backend := execLeaseBackend(t)
	r := newLaneRig(t)
	r.registry.UseLaneBackend(backend)
	r.consumer.HeartbeatInterval = 20 * time.Millisecond
	entered, cancelled := make(chan struct{}), make(chan struct{})
	r.consumer.Runner = WorkerRunFunc(func(ctx context.Context, env DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
		close(entered)
		select {
		case <-ctx.Done():
			close(cancelled)
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return completedEvents(env), nil
		}
	})
	job, err := r.jobs.Create(t.Context(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", IdempotencyKey: "lane_lapse_key_0001",
		Target: "chatgpt_web", CommandType: "submit", Input: map[string]any{"prompt": "hi"}, TraceID: "trace_lane",
	})
	if err != nil {
		t.Fatal(err)
	}
	r.consumer.Queue = singleLeaseQueue{lease: &fakeWorkerLease{jobID: job.ID, leaseID: "lease_lapse", envelope: EnvelopeFromJob(job)}}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = r.consumer.RunOnce(context.Background()) }()
	<-entered
	time.Sleep(60 * time.Millisecond)
	if n, err := backend.SweepExpired(t.Context(), time.Now().UTC().Add(10*time.Minute)); err != nil || n != 1 {
		t.Fatalf("sweep n=%d err=%v, want the job's lane registration", n, err)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("the run kept going after its lane registration lapsed")
	}
	<-done
}

// A held-back job on the file spool must not spin: Retry re-queues instantly.
func TestHeldBackJobDoesNotSpinOnTheFileSpool(t *testing.T) {
	r := newLaneRig(t)
	r.consumer.VoiceLaneRetryDelay = 200 * time.Millisecond
	r.startVoice("vs-1")
	spool := NewFileSpoolDispatcher(t.TempDir())
	job, err := r.jobs.Create(t.Context(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", IdempotencyKey: "lane_spin_key_0001",
		Target: "chatgpt_web", CommandType: "submit", Input: map[string]any{"prompt": "hi"}, TraceID: "trace_lane",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.EnqueueJob(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	inner := r.consumer.VoiceLanes
	r.consumer.VoiceLanes = probeFunc(func(ctx context.Context, lane string) (bool, error) {
		probes.Add(1)
		return inner.VoiceHoldsLane(ctx, lane)
	})
	r.consumer.Queue = nil // earlier runJob calls left a one-lease queue behind; use the spool
	r.consumer.Spool = spool
	r.consumer.PollInterval = 5 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = r.consumer.Run(ctx)

	if n := probes.Load(); n < 2 || n > 6 {
		t.Fatalf("%d lane checks in 1s with a 200ms hold, want 2..6 (a busy loop would be hundreds)", n)
	}
	if r.runs.Load() != 0 {
		t.Fatal("the job ran under a live voice session")
	}
	if final, _, _ := r.jobs.Get(context.Background(), job.ID); jobstore.TerminalStatus(final.Status) {
		t.Fatalf("the held job must still be waiting, is %s", final.Status)
	}
}

type probeFunc func(ctx context.Context, lane string) (bool, error)

func (f probeFunc) VoiceHoldsLane(ctx context.Context, lane string) (bool, error) {
	return f(ctx, lane)
}

package executor

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// ---- a fake Helper Node ------------------------------------------------------
//
// fakeHelper behaves like the real service where the primary can tell: an attempt
// lives in a per-attempt event log (not in a stream), every RunAttempt replays
// that log from sequence 1, and the test scripts what the node emits and when.

type fakeAttempt struct {
	req    *helperv1.RunAttemptRequest
	events []*helperv1.AttemptEvent
}

type fakeHelper struct {
	helperv1.HelperServiceClient // nil: a method the primary must not call panics

	mu       sync.Mutex
	changed  chan struct{} // closed and replaced on every change
	attempts map[string]*fakeAttempt
	last     *fakeAttempt
	breakGen int

	hs           *helperv1.HandshakeResponse
	serverTime   func() time.Time
	handshakeErr error
	runErr       error
	renewErr     error
	dead         bool
	inspect      func(*helperv1.InspectAttemptRequest) (*helperv1.InspectAttemptResponse, error)

	runs    []*helperv1.RunAttemptRequest
	renews  []*helperv1.RenewAttemptRequest
	cancels []*helperv1.CancelAttemptRequest
	closes  atomic.Int32
}

const (
	testWorkload = "w1"
	testDigest   = "a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7"
)

func newFakeHelper() *fakeHelper {
	return &fakeHelper{
		changed: make(chan struct{}), attempts: map[string]*fakeAttempt{}, serverTime: time.Now,
		hs: &helperv1.HandshakeResponse{
			ProtocolVersion: HelperProtocolVersion, HelperVersion: "test", WorkloadVersion: testWorkload,
			NodeId: "node_a", RegistryDigest: testDigest,
		},
	}
}

func (h *fakeHelper) signalLocked() {
	close(h.changed)
	h.changed = make(chan struct{})
}

func (h *fakeHelper) Close() error { h.closes.Add(1); return nil }

func (h *fakeHelper) Handshake(_ context.Context, _ *helperv1.HandshakeRequest, _ ...grpc.CallOption) (*helperv1.HandshakeResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.handshakeErr != nil {
		return nil, h.handshakeErr
	}
	if h.dead {
		return nil, status.Error(codes.Unavailable, "down")
	}
	resp := proto.Clone(h.hs).(*helperv1.HandshakeResponse)
	resp.ServerTime = timestamppb.New(h.serverTime())
	return resp, nil
}

func (h *fakeHelper) RunAttempt(ctx context.Context, req *helperv1.RunAttemptRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[helperv1.RunAttemptResponse], error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runs = append(h.runs, req)
	if h.dead {
		return nil, status.Error(codes.Unavailable, "down")
	}
	if h.runErr != nil {
		return nil, h.runErr
	}
	id := req.GetFence().GetAttemptId()
	a := h.attempts[id]
	if a == nil {
		a = &fakeAttempt{req: req}
		h.attempts[id] = a
	}
	h.last = a
	h.signalLocked()
	return &fakeStream{h: h, a: a, ctx: ctx, gen: h.breakGen}, nil
}

type fakeStream struct {
	grpc.ClientStream
	h    *fakeHelper
	a    *fakeAttempt
	ctx  context.Context
	gen  int
	next int
}

func (s *fakeStream) Context() context.Context { return s.ctx }

func (s *fakeStream) Recv() (*helperv1.RunAttemptResponse, error) {
	for {
		s.h.mu.Lock()
		switch {
		case s.h.dead || s.h.breakGen != s.gen:
			s.h.mu.Unlock()
			return nil, status.Error(codes.Unavailable, "stream broken")
		case s.next < len(s.a.events):
			ev := s.a.events[s.next]
			s.next++
			s.h.mu.Unlock()
			return &helperv1.RunAttemptResponse{Event: ev}, nil
		case len(s.a.events) > 0 && s.a.events[len(s.a.events)-1].GetType() == helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL:
			s.h.mu.Unlock()
			return nil, io.EOF
		}
		wake := s.h.changed
		s.h.mu.Unlock()
		select {
		case <-wake:
		case <-s.ctx.Done():
			return nil, status.FromContextError(s.ctx.Err()).Err()
		}
	}
}

func (h *fakeHelper) RenewAttempt(_ context.Context, req *helperv1.RenewAttemptRequest, _ ...grpc.CallOption) (*helperv1.RenewAttemptResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.renews = append(h.renews, req)
	h.signalLocked()
	switch {
	case h.dead:
		return nil, status.Error(codes.Unavailable, "down")
	case h.renewErr != nil:
		return nil, h.renewErr
	}
	return &helperv1.RenewAttemptResponse{LeaseGeneration: req.GetFence().GetLeaseGeneration(), State: helperv1.AttemptState_ATTEMPT_STATE_RUNNING}, nil
}

func (h *fakeHelper) CancelAttempt(_ context.Context, req *helperv1.CancelAttemptRequest, _ ...grpc.CallOption) (*helperv1.CancelAttemptResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancels = append(h.cancels, req)
	h.signalLocked()
	if h.dead {
		return nil, status.Error(codes.Unavailable, "down")
	}
	return &helperv1.CancelAttemptResponse{State: helperv1.AttemptState_ATTEMPT_STATE_FINISHED}, nil
}

func (h *fakeHelper) InspectAttempt(_ context.Context, req *helperv1.InspectAttemptRequest, _ ...grpc.CallOption) (*helperv1.InspectAttemptResponse, error) {
	h.mu.Lock()
	inspect, dead := h.inspect, h.dead
	h.mu.Unlock()
	switch {
	case dead:
		return nil, status.Error(codes.Unavailable, "down")
	case inspect != nil:
		return inspect(req)
	}
	return &helperv1.InspectAttemptResponse{State: helperv1.AttemptState_ATTEMPT_STATE_UNKNOWN}, nil
}

// ---- scripting --------------------------------------------------------------

// send appends an event to the newest attempt's log and wakes its streams.
func (h *fakeHelper) send(typ helperv1.AttemptEventType, data string) {
	h.sendOutcome(typ, data, nil)
}

func (h *fakeHelper) sendOutcome(typ helperv1.AttemptEventType, data string, outcome *helperv1.AttemptOutcome) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a := h.last
	fence := a.req.GetFence()
	a.events = append(a.events, &helperv1.AttemptEvent{
		AttemptId: fence.GetAttemptId(), Sequence: uint64(len(a.events) + 1), LeaseGeneration: fence.GetLeaseGeneration(),
		Type: typ, DataJson: data, Outcome: outcome, CreatedAt: timestamppb.Now(),
	})
	h.signalLocked()
}

// sendStale appends an event that names an older lease generation.
func (h *fakeHelper) sendStale(typ helperv1.AttemptEventType, gen uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	a := h.last
	a.events = append(a.events, &helperv1.AttemptEvent{
		AttemptId: a.req.GetFence().GetAttemptId(), Sequence: uint64(len(a.events) + 1), LeaseGeneration: gen,
		Type: typ, DataJson: "{}", CreatedAt: timestamppb.Now(),
	})
	h.signalLocked()
}

func (h *fakeHelper) sendCompleted() {
	h.sendOutcome(evTerminal, "", &helperv1.AttemptOutcome{Status: stCompleted, ResultJson: `{"type":"text","text":"from the helper"}`})
}

// breakStreams ends every open stream with Unavailable (the attempt lives on).
func (h *fakeHelper) breakStreams() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.breakGen++
	h.signalLocked()
}

func (h *fakeHelper) setDead(dead bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dead = dead
	h.signalLocked()
}

func (h *fakeHelper) setRenewErr(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.renewErr = err
}

func (h *fakeHelper) counts() (runs, renews, cancels int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.runs), len(h.renews), len(h.cancels)
}

// waitFor polls until cond holds, failing the test after d.
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitRun waits for the n-th RunAttempt request and returns it.
func (h *fakeHelper) waitRun(t *testing.T, n int) *helperv1.RunAttemptRequest {
	t.Helper()
	waitFor(t, 10*time.Second, "a RunAttempt call", func() bool { r, _, _ := h.counts(); return r >= n })
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runs[n-1]
}

func (h *fakeHelper) waitRenews(t *testing.T, n int) {
	t.Helper()
	waitFor(t, 10*time.Second, "a RenewAttempt call", func() bool { _, r, _ := h.counts(); return r >= n })
}

func (h *fakeHelper) waitCancels(t *testing.T, n int) {
	t.Helper()
	waitFor(t, 10*time.Second, "a CancelAttempt call", func() bool { _, _, c := h.counts(); return c >= n })
}

func (h *fakeHelper) lastRun() *helperv1.RunAttemptRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runs[len(h.runs)-1]
}

// ---- a fake picker ----------------------------------------------------------

type fakePicker struct {
	mu       sync.Mutex
	picks    []HelperPickRequest
	placed   int
	released int
	err      error // returned by Pick when set (ErrNoHelper = run locally)
	place    HelperPlacement
}

func newFakePicker() *fakePicker {
	return &fakePicker{place: HelperPlacement{NodeID: "node_a", Endpoint: "10.0.0.2:7443", ProfileRef: "pr_abc123"}}
}

func (p *fakePicker) Pick(_ context.Context, req HelperPickRequest) (HelperPlacement, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.picks = append(p.picks, req)
	if p.err != nil {
		return HelperPlacement{}, p.err
	}
	p.placed++
	placed := p.place
	placed.Release = func() {
		p.mu.Lock()
		p.released++
		p.mu.Unlock()
	}
	return placed, nil
}

func (p *fakePicker) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

func (p *fakePicker) stats() (placed, released int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.placed, p.released
}

// ---- a ledger spy -----------------------------------------------------------

// ledgerSpy records lease renewals and can intercept them.
type ledgerSpy struct {
	*jobstore.MemoryStore
	mu    sync.Mutex
	ttls  []time.Duration
	hook  func(ref jobstore.AttemptRef) (handled bool, err error)
	begun []jobstore.BeginAttemptRequest
}

func (s *ledgerSpy) RenewAttempt(ctx context.Context, ref jobstore.AttemptRef, ttl time.Duration) (jobstore.Attempt, error) {
	s.mu.Lock()
	s.ttls = append(s.ttls, ttl)
	hook := s.hook
	s.mu.Unlock()
	if hook != nil {
		if handled, err := hook(ref); handled {
			return jobstore.Attempt{}, err
		}
	}
	return s.MemoryStore.RenewAttempt(ctx, ref, ttl)
}

func (s *ledgerSpy) BeginAttempt(ctx context.Context, req jobstore.BeginAttemptRequest) (jobstore.Attempt, error) {
	s.mu.Lock()
	s.begun = append(s.begun, req)
	s.mu.Unlock()
	return s.MemoryStore.BeginAttempt(ctx, req)
}

func (s *ledgerSpy) setHook(hook func(ref jobstore.AttemptRef) (bool, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hook = hook
}

func (s *ledgerSpy) renewTTLs() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.ttls...)
}

// ---- a fake lease clock -------------------------------------------------------

type fakeRemoteClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []chan time.Time
}

func newFakeRemoteClock() *fakeRemoteClock { return &fakeRemoteClock{now: time.Now()} }

func (c *fakeRemoteClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeRemoteClock) NewTicker(time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.tickers = append(c.tickers, ch)
	return ch, func() {}
}

func (c *fakeRemoteClock) waitTicker(t *testing.T) {
	t.Helper()
	waitFor(t, 10*time.Second, "the lease ticker", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.tickers) > 0
	})
}

// Advance moves time forward by d and fires one tick on every ticker.
func (c *fakeRemoteClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	for _, ch := range c.tickers {
		select {
		case ch <- c.now:
		default:
		}
	}
}

// ---- the fixture ------------------------------------------------------------

type remoteFixture struct {
	t         *testing.T
	store     *ledgerSpy
	audit     *audit.MemoryStore
	helper    *fakeHelper
	picker    *fakePicker
	runner    *RemoteWorkerRunner
	localRuns atomic.Int32
}

// newRemoteFixture builds a runner over a memory store with the real ledger and a
// fake helper. Timings are the real defaults (120 s lease, 20 s renewals) unless
// mut shrinks them; the retry and reconnect delays are always short.
func newRemoteFixture(t *testing.T, mut ...func(*RemoteConfig)) *remoteFixture {
	t.Helper()
	f := &remoteFixture{
		t: t, store: &ledgerSpy{MemoryStore: jobstore.NewMemoryStore()}, audit: audit.NewMemoryStore(),
		helper: newFakeHelper(), picker: newFakePicker(),
	}
	cfg := RemoteConfig{
		Store: f.store, Picker: f.picker, Audit: f.audit,
		Dialer:          HelperDialFunc(func(context.Context, HelperPlacement) (HelperConn, error) { return f.helper, nil }),
		WorkloadVersion: testWorkload, RegistryDigest: testDigest,
		RetryDelay: 20 * time.Millisecond, ReconnectDelay: 5 * time.Millisecond, CancelTimeout: time.Second,
	}
	for _, m := range mut {
		m(&cfg)
	}
	var err error
	if f.runner, err = NewRemoteWorkerRunner(cfg); err != nil {
		t.Fatal(err)
	}
	return f
}

// fastLease shrinks the lease so real-time tests run in a fraction of a second:
// 300 ms lease, renewed every 50 ms, 20 ms skew allowance.
func fastLease(c *RemoteConfig) {
	c.LeaseTTL, c.RenewEvery, c.MaxClockSkew = 300*time.Millisecond, 50*time.Millisecond, 20*time.Millisecond
}

// consumer wires a WorkerConsumer around the runner; the local runner counts its
// runs and completes whatever it is given.
func (f *remoteFixture) consumer(lease *fakeWorkerLease, cancels *CancelRegistry) *WorkerConsumer {
	return &WorkerConsumer{
		Queue: fakeWorkerQueue{lease: lease}, Jobs: f.store, Remote: f.runner, Cancels: cancels,
		Runner: WorkerRunFunc(func(_ context.Context, env DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			f.localRuns.Add(1)
			return completedEvents(env), nil
		}),
	}
}

var jobSeq atomic.Int64

func (f *remoteFixture) newJob() (jobstore.Job, *fakeWorkerLease) { return f.newJobWith(nil) }

// newJobWith creates a queued tenant_a job (target mock, command submit) after
// letting mut change the request, and a lease for it.
func (f *remoteFixture) newJobWith(mut func(*jobstore.CreateRequest)) (jobstore.Job, *fakeWorkerLease) {
	f.t.Helper()
	n := jobSeq.Add(1)
	req := jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", IdempotencyKey: fmt.Sprintf("remote_job_key_%08d", n),
		Target: "mock", CommandType: "submit", Input: map[string]any{"prompt": "hi"}, TraceID: fmt.Sprintf("trace_remote_%d", n),
	}
	if mut != nil {
		mut(&req)
	}
	job, err := f.store.Create(f.t.Context(), req)
	if err != nil {
		f.t.Fatal(err)
	}
	return job, &fakeWorkerLease{jobID: job.ID, leaseID: "lease_" + job.ID, envelope: EnvelopeFromJob(job)}
}

// runOnce runs the consumer once in the background and returns its result channel.
func runOnceAsync(ctx context.Context, c *WorkerConsumer) <-chan runResult {
	out := make(chan runResult, 1)
	go func() {
		processed, err := c.RunOnce(ctx)
		out <- runResult{processed, err}
	}()
	return out
}

type runResult struct {
	processed bool
	err       error
}

func awaitRun(t *testing.T, ch <-chan runResult) runResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(15 * time.Second):
		t.Fatal("RunOnce did not return")
		return runResult{}
	}
}

func (f *remoteFixture) job(id string) jobstore.Job {
	f.t.Helper()
	job, found, err := f.store.Get(context.Background(), id)
	if err != nil || !found {
		f.t.Fatalf("Get(%s) = %v, %v", id, found, err)
	}
	return job
}

// eventTypes lists the job's stored event types in order.
func (f *remoteFixture) eventTypes(id string) []string {
	f.t.Helper()
	events, found, err := f.store.ListEvents(context.Background(), id, 0, 1000)
	if err != nil || !found {
		f.t.Fatalf("ListEvents(%s) = %v, %v", id, found, err)
	}
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

func (f *remoteFixture) attempts(id string) []jobstore.Attempt {
	f.t.Helper()
	attempts, err := f.store.ListAttempts(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return attempts
}

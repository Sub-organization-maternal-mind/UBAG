package helper

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ubag/ubag/apps/gateway/internal/helperauth/authtest"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// ---- fake clock ------------------------------------------------------------

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*fakeTimer]struct{}
}

type fakeTimer struct {
	c     *fakeClock
	at    time.Time
	f     func()
	armed bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Now(), timers: map[*fakeTimer]struct{}{}}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, at: c.now.Add(d), f: f, armed: true}
	c.timers[t] = struct{}{}
	return t
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := t.armed
	t.armed = false
	delete(t.c.timers, t)
	return was
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := t.armed
	t.at, t.armed = t.c.now.Add(d), true
	t.c.timers[t] = struct{}{}
	return was
}

// Advance moves time forward, firing due timers in order (synchronously; a
// callback may arm further timers that are fired too if they are due).
func (c *fakeClock) Advance(d time.Duration) {
	target := c.Now().Add(d)
	for {
		c.mu.Lock()
		var next *fakeTimer
		for t := range c.timers {
			if t.armed && !t.at.After(target) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		if next.at.After(c.now) {
			c.now = next.at
		}
		next.armed = false
		delete(c.timers, next)
		f := next.f
		c.mu.Unlock()
		f()
	}
}

// ---- fake runner -----------------------------------------------------------

type fakeRunner struct {
	script func(ctx context.Context, spec AttemptSpec, emit EmitFunc) error

	mu     sync.Mutex
	calls  int
	specs  []AttemptSpec
	causes map[string]error // attempt id -> context.Cause when Run returned
}

func newFakeRunner(script func(ctx context.Context, spec AttemptSpec, emit EmitFunc) error) *fakeRunner {
	return &fakeRunner{script: script, causes: map[string]error{}}
}

func (f *fakeRunner) Run(ctx context.Context, spec AttemptSpec, emit EmitFunc) error {
	f.mu.Lock()
	f.calls++
	f.specs = append(f.specs, spec)
	f.mu.Unlock()
	err := f.script(ctx, spec, emit)
	f.mu.Lock()
	f.causes[spec.AttemptID] = context.Cause(ctx)
	f.mu.Unlock()
	return err
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeRunner) cause(attemptID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.causes[attemptID]
}

func ev(t helperv1.AttemptEventType, data string) Event { return Event{Type: t, DataJSON: data} }

func terminal(o *helperv1.AttemptOutcome) Event {
	return Event{Type: helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL, Outcome: o}
}

func completedOutcome() *helperv1.AttemptOutcome {
	return &helperv1.AttemptOutcome{Status: helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED, ResultJson: `{"text":"ok"}`}
}

const (
	evStarted   = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_STARTED
	evOpened    = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_BROWSER_OPENED
	evSubmitted = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_PROMPT_SUBMITTED
	evToken     = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TOKEN
	evManual    = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_MANUAL_ACTION_REQUIRED
	evTerminal  = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL
)

// blockingScript emits STARTED and then runs until its context ends.
func blockingScript(ctx context.Context, _ AttemptSpec, emit EmitFunc) error {
	if err := emit(ev(evStarted, `{"phase":"start"}`)); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

// ---- rig: a real mTLS gRPC server over bufconn ------------------------------

type fakeHost struct {
	mu   sync.Mutex
	hs   HostStats
	fail bool
}

func (h *fakeHost) Sample() (HostStats, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fail {
		return HostStats{}, errors.New("no cgroup")
	}
	return h.hs, nil
}

type rig struct {
	t       *testing.T
	ca      *authtest.CA
	clock   *fakeClock
	runner  *fakeRunner
	host    *fakeHost
	srv     *Server
	auth    *PrimaryAuth
	grpcSrv *grpc.Server
	lis     *bufconn.Listener
	node    *authtest.Leaf
	primary *authtest.Leaf
	client  helperv1.HelperServiceClient

	rejMu   sync.Mutex
	rejects []string
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

const testDigest = "a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7"

func newRig(t *testing.T, runner *fakeRunner, mut ...func(*Config)) *rig {
	t.Helper()
	r := &rig{t: t, runner: runner, clock: newFakeClock(), ca: authtest.NewCA(t), host: &fakeHost{hs: HostStats{
		CPUMillisTotal: 2000, CPUMillisUsed: 500, MemoryBytesTotal: 4 << 30, MemoryBytesUsed: 1 << 30,
	}}}
	long := time.Now().Add(48 * time.Hour)
	r.node = r.ca.Issue(t, authtest.Spec{NodeID: "helper-1", NotAfter: long})
	r.primary = r.ca.Issue(t, authtest.Spec{NodeID: "primary-1", URIs: []string{PrimaryURISAN("primary-1")}, NotAfter: long})
	cfg := Config{
		NodeID: "helper-1", WorkloadVersion: "w1", RegistryDigest: testDigest, Clock: r.clock, Logger: quietLogger(),
		Host: r.host, MaxAttempts: 2, KillGrace: 2 * time.Second, DeadlineGrace: 2 * time.Second,
	}
	if runner != nil {
		cfg.Runner = runner
	}
	for _, m := range mut {
		m(&cfg)
	}
	var err error
	if r.srv, err = NewServer(cfg); err != nil {
		t.Fatal(err)
	}
	if r.auth, err = NewPrimaryAuth(PrimaryURISAN("primary-1"), nil); err != nil {
		t.Fatal(err)
	}
	r.auth.Now = r.clock.Now
	r.auth.OnReject = func(reason string) {
		r.rejMu.Lock()
		r.rejects = append(r.rejects, reason)
		r.rejMu.Unlock()
	}
	tlsCfg := r.auth.ServerTLSConfig(r.ca.Pool, func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &r.node.TLS, nil })
	r.grpcSrv = r.auth.NewGRPCServer(tlsCfg, r.srv)
	r.lis = bufconn.Listen(1 << 20)
	go func() { _ = r.grpcSrv.Serve(r.lis) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.srv.Shutdown(ctx) // ends running attempts so their streams finish
		r.grpcSrv.Stop()
	})
	r.client, _ = r.dial(r.primary)
	return r
}

// dial connects as the peer holding leaf (nil = no client certificate),
// verifying the server certificate the way the primary must: chain to the CA
// and the node's own URI SAN.
func (r *rig) dial(leaf *authtest.Leaf) (helperv1.HelperServiceClient, *grpc.ClientConn) {
	return r.dialCA(leaf, r.ca)
}

func (r *rig) dialCA(leaf *authtest.Leaf, ca *authtest.CA) (helperv1.HelperServiceClient, *grpc.ClientConn) {
	r.t.Helper()
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: ca.Pool, ServerName: "localhost",
		VerifyConnection: func(cs tls.ConnectionState) error {
			if u := cs.PeerCertificates[0].URIs; len(u) != 1 || u[0].String() != NodeURISAN("helper-1") {
				return errors.New("server certificate does not name the expected node")
			}
			return nil
		},
	}
	if leaf != nil {
		cfg.Certificates = []tls.Certificate{leaf.TLS}
	}
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return r.lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = conn.Close() })
	return helperv1.NewHelperServiceClient(conn), conn
}

func (r *rig) rejected() []string {
	r.rejMu.Lock()
	defer r.rejMu.Unlock()
	return append([]string(nil), r.rejects...)
}

func (r *rig) ctx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	r.t.Cleanup(cancel)
	return ctx
}

func (r *rig) fence(attemptID string, gen uint64) *helperv1.Fence {
	return &helperv1.Fence{
		JobId: "job-1", AttemptId: attemptID, NodeId: "helper-1", LeaseGeneration: gen,
		LeaseExpiresAt:   timestamppb.New(r.clock.Now().Add(120 * time.Second)),
		InputFingerprint: "fp-1", WorkloadVersion: "w1",
	}
}

func (r *rig) runReq(attemptID string, gen uint64) *helperv1.RunAttemptRequest {
	return &helperv1.RunAttemptRequest{
		Fence: r.fence(attemptID, gen), Provider: "chatgpt_web", Target: "chatgpt_web", CommandType: "submit",
		IdentityRef: "ident-1", InputJson: `{"prompt":"hello"}`, OptionsJson: `{}`, TraceId: "trace-1", DeadlineSeconds: 900,
	}
}

// ---- run stream helper -----------------------------------------------------

type runStream struct {
	t      *testing.T
	s      helperv1.HelperService_RunAttemptClient
	cancel context.CancelFunc
}

func (r *rig) open(req *helperv1.RunAttemptRequest) *runStream {
	r.t.Helper()
	ctx, cancel := context.WithCancel(r.ctx())
	s, err := r.client.RunAttempt(ctx, req)
	if err != nil {
		cancel()
		r.t.Fatalf("RunAttempt: %v", err)
	}
	r.t.Cleanup(cancel)
	return &runStream{t: r.t, s: s, cancel: cancel}
}

func (rs *runStream) recv() (*helperv1.AttemptEvent, error) {
	resp, err := rs.s.Recv()
	if err != nil {
		return nil, err
	}
	return resp.GetEvent(), nil
}

// all reads until the server ends the stream. A nil error means a clean end.
func (rs *runStream) all() ([]*helperv1.AttemptEvent, error) {
	var out []*helperv1.AttemptEvent
	for {
		e, err := rs.recv()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, e)
	}
}

// admitted waits for the first event, which proves the attempt was accepted.
func (rs *runStream) admitted() *helperv1.AttemptEvent {
	rs.t.Helper()
	e, err := rs.recv()
	if err != nil {
		rs.t.Fatalf("attempt was not admitted: %v", err)
	}
	return e
}

func codeOf(err error) string {
	if err == nil {
		return "OK"
	}
	return status.Code(err).String()
}

// ---- small utilities -------------------------------------------------------

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitReleased waits until Run returned and the identity slot was freed.
func (r *rig) waitReleased(attemptID string) {
	r.t.Helper()
	r.srv.mu.Lock()
	a := r.srv.attempts[attemptID]
	r.srv.mu.Unlock()
	if a == nil {
		r.t.Fatalf("no attempt %s", attemptID)
	}
	select {
	case <-a.done:
	case <-time.After(10 * time.Second):
		r.t.Fatalf("attempt %s never released", attemptID)
	}
}

func (r *rig) inspect(jobID, attemptID string) *helperv1.InspectAttemptResponse {
	r.t.Helper()
	resp, err := r.client.InspectAttempt(r.ctx(), &helperv1.InspectAttemptRequest{JobId: jobID, AttemptId: attemptID})
	if err != nil {
		r.t.Fatalf("InspectAttempt: %v", err)
	}
	return resp
}

func (r *rig) capacity() *helperv1.ReportCapacityResponse {
	r.t.Helper()
	resp, err := r.client.ReportCapacity(r.ctx(), &helperv1.ReportCapacityRequest{})
	if err != nil {
		r.t.Fatalf("ReportCapacity: %v", err)
	}
	return resp
}

func terminalOf(t *testing.T, evs []*helperv1.AttemptEvent) *helperv1.AttemptOutcome {
	t.Helper()
	if len(evs) == 0 || evs[len(evs)-1].GetType() != evTerminal {
		t.Fatalf("stream did not end with a terminal event: %v", evs)
	}
	return evs[len(evs)-1].GetOutcome()
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

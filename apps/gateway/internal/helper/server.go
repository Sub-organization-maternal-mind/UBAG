// Package helper is the Helper Node service core (perf-fleet, ubag-helper): the
// gRPC server side of ubag.helper.v1. The primary dials it over WireGuard behind
// mTLS (decision D3) and drives attempts through Handshake, ReportCapacity,
// RunAttempt (event stream), RenewAttempt, CancelAttempt, InspectAttempt and
// Drain.
//
// The service owns everything that must hold no matter what runs the browser:
// attempt and identity admission, the Attempt Lease with generation fencing and
// local self-expiry (an attempt that is not renewed is killed by the helper
// itself), a bounded per-attempt event log that survives a broken stream, and
// the outcome rules of decision D4. The browser work sits behind Runner; P4.12
// plugs the warm-daemon pool in, tests plug a fake.
//
// The package is deliberately dependency-light: gRPC and the generated proto
// only. It must never import internal/executor, jobs, artifacts, nodes or any
// database/object-store/queue driver (a test enforces it); the asset staging
// client lives next door in package staging for that reason.
package helper

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// ProtocolVersion is the only protocol this helper speaks.
const ProtocolVersion = "ubag.helper.v1"

const (
	defaultMaxAttempts = 1
	defaultKillGrace   = 10 * time.Second
	defaultDeadlineGap = 15 * time.Second
	defaultRetain      = time.Hour
	defaultMaxRetained = 256
	defaultCancelWait  = 5 * time.Second

	// LeaseMaxTTL bounds how far ahead one RunAttempt/RenewAttempt can push the
	// local expiry. The Attempt Lease is 120 s (ADR-0007); the margin absorbs
	// clock skew without letting a wrong clock park an attempt for hours.
	defaultLeaseMaxTTL = 5 * time.Minute

	// DefaultDeadline applies when a request carries no deadline_seconds (the
	// production worker run timeout is 25 minutes); MaxDeadline is the refusal bound.
	DefaultDeadline = 25 * time.Minute
	MaxDeadline     = time.Hour

	// Drain grace bounds (ADR-0009): a missing grace never means "cancel now".
	DefaultDrainGrace = 5 * time.Minute
	MaxDrainGrace     = time.Hour

	maxInputBytes   = 512 << 10
	maxOptionsBytes = 64 << 10
	maxAssets       = 64
	maxAssetBytes   = 32 << 20
)

var (
	nodeIDRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	idTokenRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	nameRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	sha256Re   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	digestRe   = sha256Re
	attemptRe  = regexp.MustCompile(`^att_[A-Za-z0-9]{1,124}$`)
	versionRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	errNoRun   = status.Error(codes.Unavailable, "helper has no runner wired")
	errClosed  = status.Error(codes.Unavailable, "helper is shutting down")
	errDrained = status.Error(codes.Unavailable, "helper is draining")
)

// HostStats is what the node observes about its own budget (its cgroup).
type HostStats struct {
	CPUMillisTotal   int32
	CPUMillisUsed    int32
	MemoryBytesTotal int64
	MemoryBytesUsed  int64
}

// HostSampler reads HostStats. An error (or no sampler) makes ReportCapacity
// report zero totals, which the primary reads as worst-case pressure (ADR-0009:
// an unusable report fails closed), never as an idle node.
type HostSampler interface {
	Sample() (HostStats, error)
}

// Config configures a Server. Zero values take the documented defaults.
type Config struct {
	// NodeID is this node's identity; it must equal the URI SAN of its server
	// certificate. Every Fence and bare node_id is cross-checked against it.
	NodeID string
	// HelperVersion is the build version reported in Handshake (default "dev").
	HelperVersion string
	// WorkloadVersion identifies the worker workload (image + code); a Fence
	// naming another version is refused.
	WorkloadVersion string
	// RegistryDigest is the canonical adapter registry digest (RegistryDigest).
	RegistryDigest string

	// Runner executes attempts. Nil is allowed: every RunAttempt is then
	// refused `Unavailable`, which is how the binary behaves until P4.12.
	Runner Runner
	// Host observes CPU and memory; nil reports zero totals (fail closed).
	Host HostSampler
	// Clock defaults to the wall clock.
	Clock  Clock
	Logger *slog.Logger

	// MaxAttempts is browsers_max: concurrent attempts, 1..8 (default 1, the
	// size a new helper starts at).
	MaxAttempts int
	// LeaseMaxTTL bounds one lease extension (default 5m).
	LeaseMaxTTL time.Duration
	// KillGrace is how long a killed attempt's runner gets to end by itself
	// before the service writes the terminal (default 10s).
	KillGrace time.Duration
	// DeadlineGrace is how long past deadline_seconds the runner gets to end
	// the attempt itself before the service cuts it (default 15s).
	DeadlineGrace time.Duration
	// Retain is how long finished attempts stay inspectable (default 1h);
	// MaxRetained bounds how many (default 256).
	Retain      time.Duration
	MaxRetained int
	// CancelWait bounds how long CancelAttempt waits for the attempt to end
	// (default 5s).
	CancelWait time.Duration
}

func (c *Config) defaults() error {
	if !nodeIDRe.MatchString(c.NodeID) {
		return errors.New("helper: NodeID must match " + nodeIDRe.String())
	}
	if !versionRe.MatchString(c.WorkloadVersion) {
		return errors.New("helper: WorkloadVersion is required")
	}
	if !digestRe.MatchString(c.RegistryDigest) {
		return errors.New("helper: RegistryDigest must be 64 lowercase hex characters")
	}
	if c.HelperVersion == "" {
		c.HelperVersion = "dev"
	}
	if c.Clock == nil {
		c.Clock = realClock{}
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = defaultMaxAttempts
	}
	c.MaxAttempts = min(c.MaxAttempts, maxAttemptsCeiling)
	for p, d := range map[*time.Duration]time.Duration{
		&c.LeaseMaxTTL: defaultLeaseMaxTTL, &c.KillGrace: defaultKillGrace, &c.DeadlineGrace: defaultDeadlineGap,
		&c.Retain: defaultRetain, &c.CancelWait: defaultCancelWait,
	} {
		if *p <= 0 {
			*p = d
		}
	}
	if c.MaxRetained <= 0 {
		c.MaxRetained = defaultMaxRetained
	}
	return nil
}

// Server implements helperv1.HelperServiceServer.
type Server struct {
	helperv1.UnimplementedHelperServiceServer // StageManifest: see below

	cfg   Config
	clock Clock
	log   *slog.Logger
	gate  *gate
	wg    sync.WaitGroup // run goroutines

	mu         sync.Mutex
	attempts   map[string]*attempt // by attempt id
	jobs       map[string]*attempt // highest-generation attempt per job (the fence)
	draining   bool
	drainTimer Timer
	closed     bool
	hostWarned bool
}

var _ helperv1.HelperServiceServer = (*Server)(nil)

// NewServer validates cfg and returns a Server. It starts nothing.
func NewServer(cfg Config) (*Server, error) {
	if err := cfg.defaults(); err != nil {
		return nil, err
	}
	return &Server{
		cfg: cfg, clock: cfg.Clock, log: cfg.Logger.With("node_id", cfg.NodeID), gate: newGate(cfg.MaxAttempts),
		attempts: map[string]*attempt{}, jobs: map[string]*attempt{},
	}, nil
}

// StageManifest is not served. Assets are not pushed over this plane: the
// helper pulls each declared attachment from the primary under an attempt
// capability token, checksum-verified (P4.10, package staging), so there is no
// helper-side upload path to expose.
func (s *Server) StageManifest(context.Context, *helperv1.StageManifestRequest) (*helperv1.StageManifestResponse, error) {
	return nil, status.Error(codes.Unimplemented, "assets are pulled by the helper from the primary; StageManifest is not served")
}

// checkClaimedNode cross-checks a bare node_id field: empty or this node.
func (s *Server) checkClaimedNode(claimed string) error {
	if claimed != "" && claimed != s.cfg.NodeID {
		return status.Error(codes.PermissionDenied, "node_id does not match this Helper Node")
	}
	return nil
}

// Handshake negotiates the protocol before any attempt.
func (s *Server) Handshake(_ context.Context, req *helperv1.HandshakeRequest) (*helperv1.HandshakeResponse, error) {
	if err := s.checkClaimedNode(req.GetNodeId()); err != nil {
		return nil, err
	}
	if req.GetProtocolVersion() != ProtocolVersion {
		return nil, status.Errorf(codes.FailedPrecondition, "unsupported protocol version; this helper speaks %s", ProtocolVersion)
	}
	return &helperv1.HandshakeResponse{
		ProtocolVersion: ProtocolVersion,
		HelperVersion:   s.cfg.HelperVersion,
		WorkloadVersion: s.cfg.WorkloadVersion,
		NodeId:          s.cfg.NodeID,
		Features:        []string{"streaming_events", "attempt_reattach", "registry_digest"},
		ServerTime:      timestamppb.New(s.clock.Now()),
		RegistryDigest:  s.cfg.RegistryDigest,
	}, nil
}

// ReportCapacity is the 15 s heartbeat. The helper only reports what it
// observes; the primary applies min(manager grant, ceiling table).
func (s *Server) ReportCapacity(_ context.Context, req *helperv1.ReportCapacityRequest) (*helperv1.ReportCapacityResponse, error) {
	if err := s.checkClaimedNode(req.GetNodeId()); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.sweepLocked()
	draining := s.draining
	s.mu.Unlock()
	active := int32(s.gate.active())
	resp := &helperv1.ReportCapacityResponse{
		NodeId:          s.cfg.NodeID,
		ObservedAt:      timestamppb.New(s.clock.Now()),
		BrowsersMax:     int32(s.cfg.MaxAttempts),
		BrowsersActive:  active,
		AttemptsActive:  active,
		Draining:        draining,
		WorkloadVersion: s.cfg.WorkloadVersion,
		Slots:           s.gate.snapshot(),
		RegistryDigest:  s.cfg.RegistryDigest,
	}
	if s.cfg.Host != nil {
		hs, err := s.cfg.Host.Sample()
		s.mu.Lock()
		warn := err != nil && !s.hostWarned
		s.hostWarned = err != nil
		s.mu.Unlock()
		if warn {
			s.log.Warn("host stats unavailable; capacity reports carry zero totals (the primary treats that as pressure)", "error", err)
		}
		if err == nil {
			resp.CpuMillisTotal, resp.CpuMillisUsed = hs.CPUMillisTotal, hs.CPUMillisUsed
			resp.MemoryBytesTotal, resp.MemoryBytesUsed = hs.MemoryBytesTotal, hs.MemoryBytesUsed
		}
	}
	return resp, nil
}

// RunAttempt admits one attempt (or re-attaches to an existing one) and streams
// its events until the terminal. A failure to send ends only this stream; the
// attempt lives on its lease.
func (s *Server) RunAttempt(req *helperv1.RunAttemptRequest, stream helperv1.HelperService_RunAttemptServer) error {
	a, err := s.admit(req)
	if err != nil {
		return err
	}
	ctx := stream.Context()
	for next := 0; ; {
		evs, more, finished := a.next(next)
		for _, ev := range evs {
			if err := stream.Send(&helperv1.RunAttemptResponse{Event: ev}); err != nil {
				return err
			}
			next++
		}
		if finished {
			return nil
		}
		select {
		case <-more:
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		}
	}
}

// RenewAttempt extends the Attempt Lease. Only the exact (job, attempt,
// generation) the helper holds may renew; anything else is a stale writer.
func (s *Server) RenewAttempt(_ context.Context, req *helperv1.RenewAttemptRequest) (*helperv1.RenewAttemptResponse, error) {
	a, err := s.lookupFenced(req.GetFence())
	if err != nil {
		return nil, err
	}
	if !req.GetFence().GetLeaseExpiresAt().IsValid() {
		return nil, status.Error(codes.InvalidArgument, "fence.lease_expires_at is required")
	}
	expiry, ok := a.extendLease(req.GetFence().GetLeaseExpiresAt().AsTime())
	v := a.view()
	if !ok && v.state != helperv1.AttemptState_ATTEMPT_STATE_FINISHED {
		return nil, status.Error(codes.Aborted, "the attempt lease already expired")
	}
	return &helperv1.RenewAttemptResponse{LeaseGeneration: v.gen, LeaseExpiresAt: timestamppb.New(expiry), State: v.state}, nil
}

// CancelAttempt stops an attempt and waits (bounded) for it to end.
func (s *Server) CancelAttempt(ctx context.Context, req *helperv1.CancelAttemptRequest) (*helperv1.CancelAttemptResponse, error) {
	a, err := s.lookupFenced(req.GetFence())
	if err != nil {
		return nil, err
	}
	a.kill(errCancelled)
	a.waitFinished(ctx, s.cfg.CancelWait)
	v := a.view()
	return &helperv1.CancelAttemptResponse{State: v.state, Submitted: v.submitted}, nil
}

// InspectAttempt is the read-only reconcile primitive. It is not fenced (it
// changes nothing) and reports UNKNOWN, not an error, for an attempt this
// process has no record of: a helper restarted since, or one that was never
// here. The primary must then reconcile and never re-run an attempt its ledger
// says was submitted.
func (s *Server) InspectAttempt(_ context.Context, req *helperv1.InspectAttemptRequest) (*helperv1.InspectAttemptResponse, error) {
	if !idTokenRe.MatchString(req.GetJobId()) || !attemptRe.MatchString(req.GetAttemptId()) {
		return nil, status.Error(codes.InvalidArgument, "job_id and attempt_id are required")
	}
	s.mu.Lock()
	a := s.attempts[req.GetAttemptId()]
	s.mu.Unlock()
	if a == nil || a.jobID != req.GetJobId() {
		return &helperv1.InspectAttemptResponse{State: helperv1.AttemptState_ATTEMPT_STATE_UNKNOWN}, nil
	}
	v := a.view()
	return &helperv1.InspectAttemptResponse{
		State: v.state, LeaseGeneration: v.gen, LeaseExpiresAt: timestamppb.New(v.expiry),
		InputFingerprint: v.fingerprint, LastSequence: v.lastSeq, Submitted: v.submitted, Outcome: v.outcome,
	}, nil
}

// Drain stops new attempts. Running attempts finish; the ones still running
// when the grace elapses are cancelled. It is idempotent: the first call starts
// the clock. There is no way back short of a restart (the primary re-sends it
// every tick for as long as the grant says draining).
func (s *Server) Drain(_ context.Context, req *helperv1.DrainRequest) (*helperv1.DrainResponse, error) {
	if err := s.checkClaimedNode(req.GetNodeId()); err != nil {
		return nil, err
	}
	grace := time.Duration(req.GetGraceSeconds()) * time.Second
	if grace <= 0 {
		grace = DefaultDrainGrace
	}
	grace = min(grace, MaxDrainGrace)
	s.mu.Lock()
	if !s.draining {
		s.draining = true
		s.drainTimer = s.clock.AfterFunc(grace, s.onDrainGrace)
		s.log.Info("helper draining", "grace", grace.String())
	}
	s.mu.Unlock()
	return &helperv1.DrainResponse{Draining: true, AttemptsActive: int32(s.gate.active())}, nil
}

func (s *Server) onDrainGrace() {
	for _, a := range s.unfinished() {
		a.kill(errDrainGrace)
	}
}

// Shutdown stops accepting attempts, kills the running ones (they end `failed`
// with stream_end_reason "shutdown") and waits for the runners to return or ctx
// to end. Call it before stopping the gRPC server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed, s.draining = true, true
	if s.drainTimer != nil {
		s.drainTimer.Stop()
	}
	s.mu.Unlock()
	for _, a := range s.unfinished() {
		a.kill(errShutdown)
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) unfinished() []*attempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*attempt
	for _, a := range s.attempts {
		if !a.finished() {
			out = append(out, a)
		}
	}
	return out
}

// admit validates a RunAttempt request and registers the attempt, or re-attaches
// to the one already registered under the same fence.
func (s *Server) admit(req *helperv1.RunAttemptRequest) (*attempt, error) {
	f := req.GetFence()
	if err := s.checkFence(f); err != nil {
		return nil, err
	}
	if f.GetInputFingerprint() == "" || f.GetWorkloadVersion() == "" {
		return nil, status.Error(codes.InvalidArgument, "fence.input_fingerprint and fence.workload_version are required")
	}
	spec, deadline, err := validateRun(req)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	if !f.GetLeaseExpiresAt().IsValid() || !f.GetLeaseExpiresAt().AsTime().After(now) {
		return nil, status.Error(codes.Aborted, "the lease in the fence is already expired")
	}
	expiry := f.GetLeaseExpiresAt().AsTime()
	if limit := now.Add(s.cfg.LeaseMaxTTL); expiry.After(limit) {
		expiry = limit
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	s.sweepLocked()
	if a := s.attempts[f.GetAttemptId()]; a != nil {
		return a.reattach(f, expiry)
	}
	cur := s.jobs[f.GetJobId()]
	if cur != nil && f.GetLeaseGeneration() <= cur.gen {
		// A lower generation is a stale writer; the same generation under a
		// different attempt id is a second claim on one lease. Neither runs.
		return nil, status.Error(codes.Aborted, "stale lease generation or attempt_id mismatch")
	}
	if s.draining {
		return nil, errDrained
	}
	if s.cfg.Runner == nil {
		return nil, errNoRun
	}
	if cur != nil { // a newer generation fences the older holder out
		cur.mu.Lock()
		cur.superseded = true
		cur.mu.Unlock()
		cur.kill(errSuperseded)
	}
	key := identityKey{provider: spec.Provider, ref: spec.IdentityRef}
	switch err := s.gate.acquire(key, spec.AttemptID, now); {
	case errors.Is(err, errIdentityBusy), errors.Is(err, errCapacity):
		return nil, status.Error(codes.Unavailable, err.Error())
	case err != nil:
		return nil, status.Error(codes.Internal, "admission failed")
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	a := &attempt{
		s: s, id: spec.AttemptID, jobID: spec.JobID, gen: spec.Generation, fingerprint: f.GetInputFingerprint(),
		workload: f.GetWorkloadVersion(), key: key, spec: spec, ctx: ctx, cancel: cancel,
		state: helperv1.AttemptState_ATTEMPT_STATE_ACCEPTED, leaseExpiry: expiry,
		changed: make(chan struct{}), done: make(chan struct{}),
	}
	a.mu.Lock()
	a.armLease()
	a.deadlineTimer = s.clock.AfterFunc(deadline+s.cfg.DeadlineGrace, func() { a.kill(errDeadline) })
	a.mu.Unlock()
	s.attempts[a.id], s.jobs[a.jobID] = a, a
	s.wg.Add(1)
	go s.run(a)
	s.log.Info("helper attempt accepted", "job_id", a.jobID, "attempt_id", a.id, "generation", a.gen, "provider", key.provider)
	return a, nil
}

// reattach serves a repeated RunAttempt for a known attempt: same job,
// generation and fingerprint, or it is a stale or conflicting writer. It never
// starts a second run.
func (a *attempt) reattach(f *helperv1.Fence, expiry time.Time) (*attempt, error) {
	a.mu.Lock()
	superseded := a.superseded
	a.mu.Unlock()
	switch {
	case a.jobID != f.GetJobId() || a.gen != f.GetLeaseGeneration() || superseded:
		return nil, status.Error(codes.Aborted, "stale lease generation or attempt_id mismatch")
	case a.fingerprint != f.GetInputFingerprint() || a.workload != f.GetWorkloadVersion():
		return nil, status.Error(codes.FailedPrecondition, "input_fingerprint or workload_version differs from the attempt")
	}
	a.extendLease(expiry) // like RenewAttempt; a lapsed lease just lets the caller read the terminal
	return a, nil
}

// checkFence validates the shape of a Fence and cross-checks its node.
func (s *Server) checkFence(f *helperv1.Fence) error {
	switch {
	case f == nil:
		return status.Error(codes.InvalidArgument, "fence is required")
	case f.GetNodeId() != s.cfg.NodeID:
		return status.Error(codes.PermissionDenied, "fence.node_id does not match this Helper Node")
	case !idTokenRe.MatchString(f.GetJobId()) || !attemptRe.MatchString(f.GetAttemptId()) || f.GetLeaseGeneration() == 0:
		return status.Error(codes.InvalidArgument, "fence needs job_id, attempt_id (att_...) and lease_generation")
	case f.GetWorkloadVersion() != "" && f.GetWorkloadVersion() != s.cfg.WorkloadVersion:
		return status.Error(codes.FailedPrecondition, "workload_version differs from this Helper Node")
	}
	return nil
}

// lookupFenced resolves the attempt a mutating RPC names and applies the fence:
// the exact job, attempt and generation the helper holds, not superseded.
func (s *Server) lookupFenced(f *helperv1.Fence) (*attempt, error) {
	if err := s.checkFence(f); err != nil {
		return nil, err
	}
	s.mu.Lock()
	a := s.attempts[f.GetAttemptId()]
	s.mu.Unlock()
	if a == nil {
		return nil, status.Error(codes.NotFound, "unknown attempt")
	}
	a.mu.Lock()
	superseded := a.superseded
	a.mu.Unlock()
	switch {
	case a.jobID != f.GetJobId() || a.gen != f.GetLeaseGeneration() || superseded:
		return nil, status.Error(codes.Aborted, "stale lease generation or attempt_id mismatch")
	case (f.GetInputFingerprint() != "" && f.GetInputFingerprint() != a.fingerprint) ||
		(f.GetWorkloadVersion() != "" && f.GetWorkloadVersion() != a.workload):
		return nil, status.Error(codes.FailedPrecondition, "input_fingerprint or workload_version differs from the attempt")
	}
	return a, nil
}

// waitFinished blocks until the attempt has its terminal, ctx ends or d passes.
func (a *attempt) waitFinished(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		_, more, finished := a.next(0)
		if finished {
			return
		}
		select {
		case <-more:
		case <-t.C:
			return
		case <-ctx.Done():
			return
		}
	}
}

// sweepLocked forgets finished attempts that outlived Retain, and the oldest
// ones beyond MaxRetained, with their per-job fence entry. Callers hold s.mu.
func (s *Server) sweepLocked() {
	type done struct {
		a  *attempt
		at time.Time
	}
	now := s.clock.Now()
	var finished []done
	for _, a := range s.attempts {
		a.mu.Lock()
		gone, at := a.outcome != nil && a.released, a.finishedAt
		a.mu.Unlock()
		if !gone {
			continue
		}
		if now.Sub(at) >= s.cfg.Retain {
			s.forgetLocked(a)
			continue
		}
		finished = append(finished, done{a, at})
	}
	if len(finished) > s.cfg.MaxRetained {
		slices.SortFunc(finished, func(x, y done) int { return x.at.Compare(y.at) })
		for _, d := range finished[:len(finished)-s.cfg.MaxRetained] {
			s.forgetLocked(d.a)
		}
	}
}

func (s *Server) forgetLocked(a *attempt) {
	delete(s.attempts, a.id)
	if s.jobs[a.jobID] == a {
		delete(s.jobs, a.jobID)
	}
}

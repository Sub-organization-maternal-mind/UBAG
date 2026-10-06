package executor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ubag/ubag/apps/gateway/internal/audit"
	"github.com/ubag/ubag/apps/gateway/internal/helpermetrics"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// RemoteWorkerRunner runs a job's attempt on a Helper Node (UBAG_HELPER_DISPATCH,
// perf-fleet slice P4.14). The primary is the gRPC client of the helper (D3).
// One call to Run is one attempt of one job:
//
//  1. ledger: refuse to start while a previous attempt still holds its lease,
//     dial and handshake the helper (protocol, node, workload version, registry
//     digest, clock skew), then BeginAttempt: lease generation n+1 under a fresh
//     attempt id. The ledger is the fence; nothing below can write without it.
//  2. RunAttempt: the helper streams the attempt's events; every one goes
//     through HelperIngest, which validates it and commits it through the
//     fenced, idempotent CommitEvents (P4.9). The terminal event is the job's
//     terminal state; nothing is applied from the helper by any other route.
//  3. lease: every RenewEvery (20 s) the Attempt Lease (LeaseTTL, 120 s) is
//     renewed on the ledger first and then on the helper, so the helper's own
//     lease never outlives the ledger's. If renewals fail for a whole LeaseTTL
//     the helper is declared lost; if the ledger says the lease belongs to
//     someone else the runner stops without writing anything.
//  4. loss: a broken stream is re-attached (RunAttempt again replays from
//     sequence 1; replays already committed are skipped). A lost helper before
//     the prompt crossed the submission boundary is a held-back job (the lease
//     is retried after a delay and a new attempt, generation+1, starts); after
//     the boundary it is ErrAmbiguous: the job ends failed_terminal with
//     submitted and reconcile_required and is never replayed (D4).
//  5. stop: cancel, shutdown, a lost lease or a rejected attempt send a best
//     effort CancelAttempt, so a helper slot is not held until its lease lapses.
//
// The consumer calls Place first (lease-then-place, ADR-0011) and Run only for a
// job that was placed; everything else, and every job when no helper is
// available, runs on the local runner unchanged.
type RemoteWorkerRunner struct {
	cfg   RemoteConfig
	clock remoteClock
}

// HelperConn is one dialed, mutually authenticated connection to a Helper Node.
type HelperConn interface {
	helperv1.HelperServiceClient
	Close() error
}

// HelperDialer opens a HelperConn for a placement. The implementation owns the
// mTLS identity: the helper's certificate must carry the node's URI SAN and an
// SPKI pin from the registry (internal/helperclient).
type HelperDialer interface {
	Dial(ctx context.Context, p HelperPlacement) (HelperConn, error)
}

// HelperDialFunc adapts a function to HelperDialer.
type HelperDialFunc func(ctx context.Context, p HelperPlacement) (HelperConn, error)

func (f HelperDialFunc) Dial(ctx context.Context, p HelperPlacement) (HelperConn, error) {
	return f(ctx, p)
}

// RemoteStore is the store surface a remote attempt needs: the job read and the
// attempt ledger with its fenced commit. MemoryStore and PostgresStore satisfy
// it; SQLite does not (no ledger).
type RemoteStore interface {
	HelperIngestStore
	jobstore.AttemptStore
}

var (
	_ RemoteStore = (*jobstore.MemoryStore)(nil)
	_ RemoteStore = (*jobstore.PostgresStore)(nil)
)

// Remote runner defaults (ADR-0007: Attempt Lease 120 s, renewed every 20 s).
const (
	DefaultHelperRenewEvery       = 20 * time.Second
	DefaultHelperMaxClockSkew     = 10 * time.Second
	defaultHelperHandshakeTimeout = 5 * time.Second
	defaultHelperRPCTimeout       = 10 * time.Second
	defaultHelperCancelTimeout    = 5 * time.Second
	defaultHelperReconnectDelay   = 250 * time.Millisecond
	maxHelperReconnectDelay       = 5 * time.Second
	defaultHelperRetryDelay       = 5 * time.Second
	defaultHelperPollEvery        = 2 * time.Second
	// helperRunSlack is how long past MaxRuntime a run may take before the runner
	// gives up on the helper (the helper's own backstop is deadline + 15 s).
	helperRunSlack = 45 * time.Second
	// maxHelperDeadline is the helper's own deadline_seconds bound (1 h).
	maxHelperDeadline = time.Hour

	helperMaxInputBytes   = 512 << 10
	helperMaxOptionsBytes = 64 << 10
)

// RemoteConfig configures a RemoteWorkerRunner. Zero durations take the
// documented defaults.
type RemoteConfig struct {
	Store  RemoteStore
	Picker HelperPicker
	Dialer HelperDialer
	// Audit receives helper rejections (optional; see HelperIngestConfig).
	Audit audit.Store
	// WorkloadVersion is the helper workload (image + code) this primary
	// dispatches to. A helper reporting another version is not used.
	WorkloadVersion string
	// RegistryDigest is this primary's adapter registry digest (helper.RegistryDigest
	// of its adapters directory). A helper whose digest differs, or is empty, is
	// not used: its adapters would behave differently.
	RegistryDigest string
	Limits         HelperIngestLimits
	// Nodes resolves a node id to its endpoint (the node store's last accepted
	// allocation). Only the reconciler paths (Inspect, Resume, P4.18) read it; nil
	// makes them report the node as unreachable.
	Nodes NodeEndpoints

	LeaseTTL   time.Duration // default 120 s; at most jobstore.MaxAttemptLeaseTTL
	RenewEvery time.Duration // default 20 s; must be at most LeaseTTL/3
	// MaxRuntime is the attempt's wall-clock budget sent to the helper
	// (deadline_seconds); default defaultWorkerMaxRuntime, at most 1 h.
	MaxRuntime time.Duration
	// MaxClockSkew is the largest helper/primary clock difference tolerated
	// (default 10 s). The helper expires leases on its own clock, so a drifting
	// node is refused, and the lease sent to the helper is shortened by this much
	// so the helper's lease ends no later than the ledger's.
	MaxClockSkew     time.Duration
	HandshakeTimeout time.Duration
	RPCTimeout       time.Duration
	CancelTimeout    time.Duration
	ReconnectDelay   time.Duration
	// PollEvery is how often Resume asks the helper about a submitted attempt it
	// adopted (default 2 s).
	PollEvery time.Duration
	// RetryDelay is how long a job held back by a helper problem waits before its
	// lease is retried (default 5 s).
	RetryDelay time.Duration
}

func (c *RemoteConfig) defaults() error {
	switch {
	case c.Store == nil:
		return errors.New("remote runner: a store with the attempt ledger is required")
	case c.Picker == nil:
		return errors.New("remote runner: a picker is required")
	case c.Dialer == nil:
		return errors.New("remote runner: a dialer is required")
	case c.WorkloadVersion == "":
		return errors.New("remote runner: WorkloadVersion is required")
	case len(c.RegistryDigest) != 64:
		return errors.New("remote runner: RegistryDigest must be the 64-hex adapter registry digest")
	}
	for p, d := range map[*time.Duration]time.Duration{
		&c.LeaseTTL: jobstore.DefaultAttemptLeaseTTL, &c.RenewEvery: DefaultHelperRenewEvery,
		&c.MaxRuntime: defaultWorkerMaxRuntime, &c.MaxClockSkew: DefaultHelperMaxClockSkew,
		&c.HandshakeTimeout: defaultHelperHandshakeTimeout, &c.RPCTimeout: defaultHelperRPCTimeout,
		&c.CancelTimeout: defaultHelperCancelTimeout, &c.ReconnectDelay: defaultHelperReconnectDelay,
		&c.RetryDelay: defaultHelperRetryDelay, &c.PollEvery: defaultHelperPollEvery,
	} {
		if *p <= 0 {
			*p = d
		}
	}
	if c.LeaseTTL > jobstore.MaxAttemptLeaseTTL || c.RenewEvery*3 > c.LeaseTTL || c.MaxClockSkew >= c.LeaseTTL/2 {
		return errors.New("remote runner: need RenewEvery <= LeaseTTL/3, MaxClockSkew < LeaseTTL/2 and LeaseTTL <= 15 min")
	}
	c.MaxRuntime = min(c.MaxRuntime, maxHelperDeadline)
	return nil
}

// NewRemoteWorkerRunner validates cfg.
func NewRemoteWorkerRunner(cfg RemoteConfig) (*RemoteWorkerRunner, error) {
	if err := cfg.defaults(); err != nil {
		return nil, err
	}
	return &RemoteWorkerRunner{cfg: cfg, clock: wallClock{}}, nil
}

// remoteClock is the time source of the lease loop (tests drive a fake).
type remoteClock interface {
	Now() time.Time
	// NewTicker returns a tick channel and its stop function.
	NewTicker(d time.Duration) (<-chan time.Time, func())
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }
func (wallClock) NewTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

var (
	// ErrRemoteLeaseLost: the ledger no longer lists this runner as the holder of
	// the attempt (a newer generation superseded it, or its lease was reaped).
	// Nothing more may be written for the attempt, and nothing is: the job
	// belongs to the successor.
	ErrRemoteLeaseLost = errors.New("helper dispatch: attempt lease lost")

	errRunDeadline = errors.New("helper dispatch: run deadline passed")
)

// helperLostError says the helper can no longer be relied on for this attempt:
// unreachable for a whole lease, refusing the attempt, or breaking the contract.
type helperLostError struct {
	reason string
	cause  error
}

func (e *helperLostError) Error() string {
	if e.cause != nil {
		return "helper dispatch: helper lost (" + e.reason + "): " + e.cause.Error()
	}
	return "helper dispatch: helper lost (" + e.reason + ")"
}
func (e *helperLostError) Unwrap() error { return e.cause }

// streamBreak is a RunAttempt stream that ended without the attempt ending.
type streamBreak struct{ reason string }

func (e *streamBreak) Error() string { return "helper dispatch: event stream broke (" + e.reason + ")" }

// Place decides where a leased job runs. A nil placement (and nil error) means
// the job runs on the local runner: dispatch is off (nil runner), the job is not
// helper-eligible, or the picker has nothing for it (ErrNoHelper). An error is
// always a *HelperRetryError: the job is to be held back and retried.
func (r *RemoteWorkerRunner) Place(ctx context.Context, env DispatchEnvelope) (*Placement, error) {
	if r == nil || !helperEligible(env) {
		return nil, nil
	}
	got, err := r.cfg.Picker.Pick(ctx, HelperPickRequest{
		TenantID: env.TenantID, AppID: env.AppID, JobID: env.JobID,
		Target: env.Job.Target, CommandType: env.Job.CommandType, TraceID: env.TraceID,
	})
	if err != nil {
		if errors.Is(err, ErrNoHelper) {
			return nil, nil
		}
		var retry *HelperRetryError
		if errors.As(err, &retry) {
			return nil, retry
		}
		return nil, &HelperRetryError{Reason: "picker_error", RetryAfter: r.cfg.RetryDelay, Err: err}
	}
	placed := &Placement{HelperPlacement: got}
	if !validPlacement(got) {
		placed.Release()
		return nil, &HelperRetryError{Reason: "placement_invalid", RetryAfter: r.cfg.RetryDelay}
	}
	return placed, nil
}

// Run executes one attempt of the job on the placed helper and releases the
// placement when it returns.
//
// A nil error means the attempt reached an end the store already holds: the
// helper's terminal event was committed, or the gateway failed the attempt
// (over-limit or invalid output), or the job was already terminal; the caller
// reads the job and closes the lease. Otherwise:
//
//   - *HelperRetryError: the job did not reach a provider (or the lease moved to
//     another holder); hold it back and retry its lease.
//   - errors.Is(err, ErrAmbiguous): the prompt may have reached the provider and
//     the attempt cannot be completed. Fail the job closed; never replay it.
//   - context errors: the run was stopped from outside (cancel, shutdown). Wrapped
//     in ErrAmbiguous too when the prompt had been submitted.
//   - anything else: a failure of the gateway's own store or protocol.
func (r *RemoteWorkerRunner) Run(ctx context.Context, env DispatchEnvelope, placed *Placement) error {
	defer placed.Release()
	attemptID, err := newHelperAttemptID()
	if err != nil {
		return err
	}
	spec, body, err := projectHelperJob(env, attemptID, placed.ProfileRef)
	if err != nil {
		return fmt.Errorf("helper dispatch: %w", err)
	}
	a := &remoteAttempt{
		r: r, env: env, node: placed.NodeID, attemptID: attemptID, workload: r.cfg.WorkloadVersion, spec: spec, body: body,
		fingerprint: helperFingerprint(env.JobID, spec.Target, spec.CommandType, body),
	}
	return a.run(ctx, placed)
}

// helperJobBody is the JSON the helper receives for the job.
type helperJobBody struct{ inputJSON, optionsJSON string }

// projectHelperJob builds the helper spec and the two JSON bodies the proto
// carries. Everything in it is the P4.7 projection: no callbacks, client,
// context, local paths or profile options.
func projectHelperJob(env DispatchEnvelope, attemptID, profileRef string) (HelperAttemptSpec, helperJobBody, error) {
	spec, err := NewHelperAttemptSpec(env, attemptID, profileRef)
	if err != nil {
		return HelperAttemptSpec{}, helperJobBody{}, err
	}
	input, err := json.Marshal(nonNilMap(spec.Input))
	if err != nil || len(input) > helperMaxInputBytes {
		return HelperAttemptSpec{}, helperJobBody{}, fmt.Errorf("%w: input does not fit the helper contract", ErrHelperSpecInvalid)
	}
	body := helperJobBody{inputJSON: string(input)}
	if len(spec.Options) > 0 {
		options, err := json.Marshal(spec.Options)
		if err != nil || len(options) > helperMaxOptionsBytes {
			return HelperAttemptSpec{}, helperJobBody{}, fmt.Errorf("%w: options do not fit the helper contract", ErrHelperSpecInvalid)
		}
		body.optionsJSON = string(options)
	}
	return spec, body, nil
}

func nonNilMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// helperFingerprint binds an attempt to its input: a retried attempt of the same
// job cannot silently change what it runs (the ledger refuses it). json.Marshal
// sorts map keys, so the bodies are canonical.
func helperFingerprint(jobID, target, commandType string, b helperJobBody) string {
	sum := sha256.New()
	for _, part := range []string{jobID, target, commandType, b.inputJSON, b.optionsJSON} {
		_, _ = sum.Write([]byte(part))
		_, _ = sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// newHelperAttemptID returns att_ + 128 random bits (^att_[A-Za-z0-9]+$).
func newHelperAttemptID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("helper dispatch: attempt id: %w", err)
	}
	return "att_" + hex.EncodeToString(b[:]), nil
}

// remoteAttempt is the state of one Run.
type remoteAttempt struct {
	r           *RemoteWorkerRunner
	env         DispatchEnvelope
	node        string
	attemptID   string
	workload    string // the helper workload the attempt was leased under (the fence carries it)
	spec        HelperAttemptSpec
	body        helperJobBody
	fingerprint string

	conn       HelperConn
	ingest     *HelperIngest
	ref        jobstore.AttemptRef
	generation uint64

	// submitted is set the moment a PROMPT_SUBMITTED event is read from the
	// stream, before it is committed: the conservative direction (the ledger
	// boundary is recorded by the ingest right after).
	submitted atomic.Bool
	sawEvent  bool
	committed uint64 // highest event sequence committed (stream goroutine only)
}

func (a *remoteAttempt) cfg() *RemoteConfig { return &a.r.cfg }

func (a *remoteAttempt) log() *slog.Logger {
	return slog.With("job_id", a.env.JobID, "attempt_id", a.attemptID, "node_id", a.node)
}

func (a *remoteAttempt) run(ctx context.Context, placed *Placement) error {
	cfg := a.cfg()

	// A previous attempt that still holds its lease must lapse first: BeginAttempt
	// would refuse anyway, and asking first spares the helper a handshake.
	attempts, err := cfg.Store.ListAttempts(ctx, a.env.JobID)
	if err != nil {
		return fmt.Errorf("helper dispatch: list attempts: %w", err)
	}
	var latest uint64
	if n := len(attempts); n > 0 {
		last := attempts[n-1]
		latest = last.Generation
		if wait := time.Until(last.LeaseExpiresAt); last.State == jobstore.AttemptActive && wait > 0 {
			return &HelperRetryError{Reason: "attempt_lease_held", RetryAfter: wait + time.Second}
		}
	}

	if err := a.connect(ctx, placed); err != nil {
		return err
	}
	defer a.conn.Close()

	// attempt.granted is appended BEFORE the lease is taken (fail closed): if the
	// tenant's audit chain cannot take it, nothing was leased or sent, so the job
	// is simply held back. Without an audit store nothing is recorded.
	if cfg.Audit != nil {
		fleet := &audit.Fleet{Store: cfg.Audit}
		rec := a.auditRecord(fleet, audit.EventAttemptGranted, "ok", latest+1)
		if err := fleet.Guard(ctx, rec, func() error { return nil }); err != nil {
			a.log().Error("attempt.granted audit append failed; holding the job back", "error", err)
			return &HelperRetryError{Reason: "audit_unavailable", RetryAfter: cfg.RetryDelay, Err: err}
		}
	}

	att, err := cfg.Store.BeginAttempt(ctx, jobstore.BeginAttemptRequest{
		JobID: a.env.JobID, AttemptID: a.attemptID, NodeID: a.node, ExpectedGeneration: latest,
		TTL: cfg.LeaseTTL, InputFingerprint: a.fingerprint, WorkloadVersion: cfg.WorkloadVersion,
	})
	switch {
	case err == nil:
	case errors.Is(err, jobstore.ErrAttemptJobTerminal):
		return nil // nothing to run; the caller closes the lease on the job's own state
	case errors.Is(err, jobstore.ErrAttemptConflict):
		return &HelperRetryError{Reason: "attempt_conflict", RetryAfter: cfg.RetryDelay, Err: err}
	case errors.Is(err, jobstore.ErrAttemptSubmitted):
		return fmt.Errorf("%w: an earlier attempt already crossed the submission boundary: %w", ErrAmbiguous, err)
	default:
		return fmt.Errorf("helper dispatch: begin attempt: %w", err)
	}
	a.ref, a.generation = att.Ref(), att.Generation

	a.ingest, err = OpenHelperIngest(ctx, HelperIngestConfig{Store: cfg.Store, Audit: cfg.Audit, Limits: cfg.Limits},
		HelperIngestBinding{
			TenantID: a.env.TenantID, AppID: a.env.AppID, JobID: a.env.JobID, AttemptID: a.attemptID,
			Generation: att.Generation, NodeID: a.node, InputFingerprint: a.fingerprint, WorkloadVersion: cfg.WorkloadVersion,
		})
	if err != nil {
		return fmt.Errorf("helper dispatch: open ingest: %w", err)
	}

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	deadline := time.AfterFunc(cfg.MaxRuntime+helperRunSlack, func() { cancel(errRunDeadline) })
	defer deadline.Stop()
	keeperDone := make(chan struct{})
	go func() {
		defer close(keeperDone)
		a.keepLease(runCtx, cancel)
	}()

	err = a.stream(runCtx)
	cancel(nil)
	<-keeperDone
	if err = a.finish(err); err == nil && cfg.Audit != nil {
		// The job is already terminal here, so this is a record, not a gate.
		fleet := &audit.Fleet{Store: cfg.Audit}
		actx, acancel := detachedOpContext(ctx)
		defer acancel()
		if _, aerr := cfg.Audit.Append(actx, a.auditRecord(fleet, audit.EventAttemptCommitted, "ok", a.generation)); aerr != nil {
			a.log().Error("attempt.committed audit append failed", "error", aerr)
		}
	}
	return err
}

// auditRecord is the attempt's record on the job's own tenant chain: ids and the
// generation only, never prompt, attachment or output content.
func (a *remoteAttempt) auditRecord(f *audit.Fleet, action, outcome string, generation uint64) audit.Record {
	rec := f.AttemptEvent(a.env.TenantID, action, a.node, a.env.JobID, outcome,
		map[string]any{"attempt_id": a.attemptID, "lease_generation": generation, "node_id": a.node})
	rec.AppID = a.env.AppID
	return rec
}

// connect dials the helper and checks it is the helper this primary expects.
// A failure here happens before any ledger write, so the job is simply held
// back: nothing was leased and nothing can have been submitted.
func (a *remoteAttempt) connect(ctx context.Context, placed *Placement) error {
	cfg := a.cfg()
	held := func(reason string, cause error) error {
		a.log().Warn("helper not usable; holding the job back", "reason", reason)
		return &HelperRetryError{Reason: reason, RetryAfter: cfg.RetryDelay, Err: cause}
	}
	conn, err := cfg.Dialer.Dial(ctx, placed.HelperPlacement)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return held("helper_unreachable", err)
	}
	hctx, cancel := context.WithTimeout(ctx, cfg.HandshakeTimeout)
	defer cancel()
	hs, err := conn.Handshake(hctx, &helperv1.HandshakeRequest{
		ProtocolVersion: HelperProtocolVersion, PrimaryWorkloadVersion: cfg.WorkloadVersion, NodeId: a.node,
	})
	if err != nil {
		_ = conn.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return held("helper_unreachable", err)
	}
	if reason := a.r.incompatible(hs, a.node, false); reason != "" {
		_ = conn.Close()
		return held(reason, nil)
	}
	a.conn = conn
	return nil
}

// HelperProtocolVersion is the only protocol the runner speaks.
const HelperProtocolVersion = "ubag.helper.v1"

// incompatible names why a handshake answer rules the helper out ("" = usable).
// resume is the lenient form for a helper that already holds an attempt: the
// workload version and registry digest are the attempt's own business (its fence
// carries the version it was leased under), so only protocol, node and clock count.
func (r *RemoteWorkerRunner) incompatible(hs *helperv1.HandshakeResponse, node string, resume bool) string {
	cfg := &r.cfg
	skew := r.clock.Now().Sub(hs.GetServerTime().AsTime())
	if skew < 0 {
		skew = -skew
	}
	switch {
	case hs.GetProtocolVersion() != HelperProtocolVersion:
		return "helper_protocol"
	case hs.GetNodeId() != "" && hs.GetNodeId() != node:
		return "helper_node_mismatch" // a cross-check only: the certificate already named the node
	case !resume && hs.GetWorkloadVersion() != cfg.WorkloadVersion:
		return "helper_workload_version"
	case !resume && (hs.GetRegistryDigest() == "" || hs.GetRegistryDigest() != cfg.RegistryDigest):
		return "helper_registry_digest"
	case !hs.GetServerTime().IsValid() || skew > cfg.MaxClockSkew:
		return "helper_clock_skew"
	}
	return ""
}

// fence is the Fence every mutating RPC presents.
func (a *remoteAttempt) fence() *helperv1.Fence {
	return &helperv1.Fence{
		JobId: a.env.JobID, AttemptId: a.attemptID, NodeId: a.node, LeaseGeneration: a.generation,
		LeaseExpiresAt: timestamppb.New(a.helperLeaseExpiry()), InputFingerprint: a.fingerprint,
		WorkloadVersion: a.workload,
	}
}

// helperLeaseExpiry is what the helper is told. It is shorter than the ledger
// lease by the clock-skew bound, so the helper kills a lost attempt no later
// than the ledger lets a successor start.
func (a *remoteAttempt) helperLeaseExpiry() time.Time {
	cfg := a.cfg()
	return a.r.clock.Now().Add(cfg.LeaseTTL - cfg.MaxClockSkew)
}

func (a *remoteAttempt) request() *helperv1.RunAttemptRequest {
	deadline := int32(min(math.Ceil(a.cfg().MaxRuntime.Seconds()), maxHelperDeadline.Seconds()))
	return &helperv1.RunAttemptRequest{
		Fence:    a.fence(),
		Provider: a.spec.Target, Target: a.spec.Target, CommandType: a.spec.CommandType,
		IdentityRef: a.spec.ProfileRef, InputJson: a.body.inputJSON, OptionsJson: a.body.optionsJSON,
		TraceId: a.spec.TraceID, DeadlineSeconds: deadline,
	}
}

// ---- lease -----------------------------------------------------------------

// keepLease renews the Attempt Lease every RenewEvery until ctx ends. It stops
// the run (cancel with a cause) when the lease is lost for good: the ledger or
// the helper says it is not ours, or no renewal succeeded for a whole LeaseTTL.
func (a *remoteAttempt) keepLease(ctx context.Context, cancel context.CancelCauseFunc) {
	cfg := a.cfg()
	ticks, stop := a.r.clock.NewTicker(cfg.RenewEvery)
	defer stop()
	lastOK := a.r.clock.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
		err := a.renew(ctx)
		if ctx.Err() != nil {
			return
		}
		var lost *helperLostError
		switch {
		case err == nil:
			lastOK = a.r.clock.Now()
			continue
		case errors.Is(err, ErrRemoteLeaseLost):
			helpermetrics.RecordLeaseRenewFailure(helpermetrics.LeaseAttempt, helpermetrics.ReasonLost)
			cancel(err)
			return
		case errors.As(err, &lost):
			helpermetrics.RecordLeaseRenewFailure(helpermetrics.LeaseAttempt, helpermetrics.ReasonLost)
			cancel(err)
			return
		}
		helpermetrics.RecordLeaseRenewFailure(helpermetrics.LeaseAttempt, helpermetrics.ReasonError)
		if silent := a.r.clock.Now().Sub(lastOK); silent >= cfg.LeaseTTL {
			a.log().Warn("attempt lease not renewed for a whole lease; declaring the helper lost", "silent", silent)
			cancel(&helperLostError{reason: "lease_expired", cause: err})
			return
		}
		a.log().Warn("attempt lease renewal failed; will retry", "error", err)
	}
}

// renew extends the lease on the ledger and then on the helper. Order matters:
// the ledger first, so a failing ledger leaves both leases to lapse together
// and the helper's lease can never outlive the ledger's.
func (a *remoteAttempt) renew(ctx context.Context) error {
	cfg := a.cfg()
	rctx, cancel := context.WithTimeout(ctx, cfg.RPCTimeout)
	defer cancel()
	if _, err := cfg.Store.RenewAttempt(rctx, a.ref, cfg.LeaseTTL); err != nil {
		if errors.Is(err, jobstore.ErrAttemptFenced) {
			return fmt.Errorf("%w: %w", ErrRemoteLeaseLost, err)
		}
		return fmt.Errorf("renew ledger lease: %w", err)
	}
	if _, err := a.conn.RenewAttempt(rctx, &helperv1.RenewAttemptRequest{Fence: a.fence()}); err != nil {
		if refused(status.Code(err)) {
			return &helperLostError{reason: "helper_refused_renew", cause: err}
		}
		return fmt.Errorf("renew helper lease: %w", err)
	}
	return nil
}

// refused reports whether a gRPC code means the helper will not serve this
// attempt (as opposed to a transport problem that may heal).
func refused(c codes.Code) bool {
	switch c {
	case codes.Aborted, codes.NotFound, codes.PermissionDenied, codes.FailedPrecondition,
		codes.Unauthenticated, codes.Unimplemented:
		return true
	}
	return false
}

// ---- event stream ----------------------------------------------------------

// stream reads the attempt's events until it ends, re-attaching after a break.
// It returns nil when the attempt's end is in the store (terminal committed, or
// the gateway failed the attempt), or the reason it stopped.
func (a *remoteAttempt) stream(ctx context.Context) error {
	cfg := a.cfg()
	delay := cfg.ReconnectDelay
	for {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		before := a.committed
		err := a.attach(ctx)
		var broke *streamBreak
		if !errors.As(err, &broke) {
			return err
		}
		if a.committed > before {
			delay = cfg.ReconnectDelay
		}
		a.log().Info("helper event stream broke; re-attaching", "reason", broke.reason, "committed", a.committed)
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(delay):
		}
		delay = min(delay*2, maxHelperReconnectDelay)
	}
}

// attach opens RunAttempt (a repeated call re-attaches and replays from
// sequence 1) and commits events until the attempt ends or the stream breaks.
func (a *remoteAttempt) attach(ctx context.Context) error {
	stream, err := a.conn.RunAttempt(ctx, a.request())
	if err != nil {
		return a.classify(ctx, err)
	}
	for {
		resp, err := stream.Recv()
		if err != nil {
			return a.classify(ctx, err)
		}
		ev := resp.GetEvent()
		if ev == nil {
			return &streamBreak{reason: "empty_message"}
		}
		a.sawEvent = true
		if ev.GetSequence() != 0 && ev.GetSequence() <= a.committed {
			continue // a replay of what is already committed
		}
		if ev.GetType() == helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_PROMPT_SUBMITTED {
			a.submitted.Store(true)
		}
		done, err := a.accept(ctx, ev)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

// accept commits one event. done means the attempt ended.
func (a *remoteAttempt) accept(ctx context.Context, ev *helperv1.AttemptEvent) (done bool, err error) {
	var job jobstore.Job
	for try := 0; ; try++ {
		job, err = a.ingest.Accept(ctx, ev)
		// A transient store error, or a content failure whose own failure record
		// has not landed yet (the session stays open and the record is idempotent):
		// try again a few times, briefly.
		retry := err != nil && ctx.Err() == nil && try < 4 && !a.ingest.Done() &&
			(transientIngestError(err) || errors.Is(err, ErrHelperEventInvalid) || errors.Is(err, ErrHelperOutputLimit))
		if !retry {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(100<<try) * time.Millisecond):
		}
	}
	switch {
	case err == nil:
		a.committed = ev.GetSequence()
		switch {
		case a.ingest.Done():
			return true, nil
		case jobstore.TerminalStatus(job.Status):
			// The job ended under the attempt (cancel, reaper): stop the helper.
			return false, fmt.Errorf("%w: the job ended under the attempt", context.Canceled)
		}
		return false, nil
	case errors.Is(err, jobstore.ErrAttemptFenced):
		return false, a.fencedCause(ctx, err)
	case errors.Is(err, ErrHelperEventInvalid), errors.Is(err, ErrHelperOutputLimit):
		if a.ingest.Done() {
			// The gateway failed the attempt (failed_terminal is in the store). Stop
			// the helper; the output it was told to produce is refused.
			a.cancelHelper("output_rejected")
			return true, nil
		}
		return false, fmt.Errorf("helper dispatch: record helper failure: %w", err)
	case errors.Is(err, ErrHelperIngestScope):
		return false, &helperLostError{reason: "scope_violation", cause: err}
	case errors.Is(err, ErrHelperSequenceGap):
		return false, &streamBreak{reason: "sequence_gap"}
	case ctx.Err() != nil:
		return false, context.Cause(ctx)
	}
	return false, fmt.Errorf("helper dispatch: commit helper event: %w", err)
}

// transientIngestError is true for an Accept error that is neither a verdict on
// the helper nor a fence: a store hiccup worth a short retry.
func transientIngestError(err error) bool {
	for _, verdict := range []error{
		jobstore.ErrAttemptFenced, ErrHelperIngestScope, ErrHelperIngestClosed, ErrHelperSequenceGap,
		ErrHelperEventInvalid, ErrHelperOutputLimit,
	} {
		if errors.Is(err, verdict) {
			return false
		}
	}
	return true
}

// fencedCause tells a lost lease from a confused helper. A fenced commit means
// our attempt is no longer the active one only if the ledger agrees; an event
// that names a stale generation or another attempt while the ledger still lists
// ours as active is the helper's fault, and the successor does not exist.
func (a *remoteAttempt) fencedCause(ctx context.Context, fenced error) error {
	attempts, err := a.cfg().Store.ListAttempts(ctx, a.env.JobID)
	if err == nil {
		for _, att := range attempts {
			if att.AttemptID == a.attemptID && att.Generation == a.generation && att.State == jobstore.AttemptActive {
				return &helperLostError{reason: "helper_fenced_event", cause: fenced}
			}
		}
	}
	return fmt.Errorf("%w: %w", ErrRemoteLeaseLost, fenced)
}

// classify turns a RunAttempt/Recv error into what the stream loop does next.
func (a *remoteAttempt) classify(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if errors.Is(err, io.EOF) {
		return &streamBreak{reason: "eof"}
	}
	code := status.Code(err)
	switch {
	case code == codes.InvalidArgument:
		// A malformed request fails the same way on every helper; do not loop.
		return fmt.Errorf("helper dispatch: the helper rejected the attempt request (%s)", code)
	case refused(code):
		return &helperLostError{reason: "helper_refused", cause: err}
	}
	// Unavailable is both "the helper refused to admit it" (draining, over
	// capacity, identity busy) and "the connection broke". Before any event, ask
	// the helper: no record means it never took the attempt.
	if code == codes.Unavailable && !a.sawEvent && a.committed == 0 && a.helperHasNoRecord(ctx) {
		return &helperLostError{reason: "helper_refused", cause: err}
	}
	return &streamBreak{reason: code.String()}
}

func (a *remoteAttempt) helperHasNoRecord(ctx context.Context) bool {
	ictx, cancel := context.WithTimeout(ctx, a.cfg().RPCTimeout)
	defer cancel()
	resp, err := a.conn.InspectAttempt(ictx, &helperv1.InspectAttemptRequest{JobId: a.env.JobID, AttemptId: a.attemptID})
	return err == nil && resp.GetState() == helperv1.AttemptState_ATTEMPT_STATE_UNKNOWN
}

// ---- ending ----------------------------------------------------------------

// finish turns how the stream stopped into the error Run returns, and stops the
// helper when the attempt should not go on.
func (a *remoteAttempt) finish(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errRunDeadline) {
		err = &helperLostError{reason: "run_deadline", cause: err}
	}
	var lost *helperLostError
	switch {
	case errors.Is(err, ErrRemoteLeaseLost):
		// The successor owns the job and the helper: no write, no cancel (a stale
		// fence would be refused anyway).
		return &HelperRetryError{Reason: "attempt_lease_lost", RetryAfter: a.cfg().RetryDelay, Err: err}
	case errors.As(err, &lost):
		a.cancelHelper("helper_lost")
		if a.submitted.Load() {
			a.log().Error("helper lost after the prompt was submitted; the job needs reconciling", "reason", lost.reason)
			return fmt.Errorf("%w: helper lost after prompt submission: %w", ErrAmbiguous, err)
		}
		a.log().Warn("helper lost before the prompt was submitted; the job will be reassigned", "reason", lost.reason)
		return &HelperRetryError{Reason: "helper_lost", RetryAfter: a.cfg().RetryDelay, Err: err}
	}
	// Stopped from outside (cancel, shutdown) or a gateway-side failure.
	a.cancelHelper("cancelled")
	if a.submitted.Load() && !errors.Is(err, ErrAmbiguous) {
		return fmt.Errorf("%w: attempt interrupted after prompt submission: %w", ErrAmbiguous, err)
	}
	return err
}

// cancelHelper asks the helper to stop the attempt, best effort, on a context
// that outlives a cancelled run.
func (a *remoteAttempt) cancelHelper(reason string) {
	if a.conn == nil || a.generation == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.cfg().CancelTimeout)
	defer cancel()
	if _, err := a.conn.CancelAttempt(ctx, &helperv1.CancelAttemptRequest{Fence: a.fence(), Reason: reason}); err != nil {
		a.log().Debug("helper cancel did not land; its lease will lapse", "reason", reason, "code", status.Code(err).String())
	}
}

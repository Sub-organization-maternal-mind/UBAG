package executor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// Attempt reconciliation, effect side (UBAG_HELPER_DISPATCH, perf-fleet slice P4.18).
// nodes.Reconciler decides; this file is how the primary asks a helper about an
// attempt (Inspect) and how it completes a submitted attempt that no live runner
// holds any more (Resume), after a gateway crash or restart.
//
// Resume never calls RunAttempt. RunAttempt means "admit, or re-attach": a helper
// that lost its record (a restart) would admit it as a NEW run, which is exactly
// the blind resubmit this slice exists to prevent. Resume only uses calls that can
// never start anything: InspectAttempt (read-only), RenewAttempt and CancelAttempt
// (both fenced, and NotFound for an attempt the helper does not hold). A finished
// attempt's outcome comes from InspectAttempt and is committed as the attempt's
// terminal event through the same fenced ingest as any streamed event; the
// provisional events the primary never saw (tokens) are not replayed.

// NodeEndpoints resolves where a node listens: the node store's last accepted
// allocation (nodes.Store satisfies it). The manager is not asked.
type NodeEndpoints interface {
	GetAllocation(ctx context.Context, nodeID string) (nodes.Allocation, error)
}

var errNoNodeEndpoint = errors.New("helper reconcile: no endpoint is known for the node")

// dialNode dials a node by id at its last accepted endpoint. handshake adds the
// lenient handshake (protocol, node, clock) a lease-bearing call needs.
func (r *RemoteWorkerRunner) dialNode(ctx context.Context, nodeID string, handshake bool) (HelperConn, error) {
	if r.cfg.Nodes == nil {
		return nil, errNoNodeEndpoint
	}
	alloc, err := r.cfg.Nodes.GetAllocation(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("helper reconcile: node %s: %w", nodeID, err)
	}
	conn, err := r.cfg.Dialer.Dial(ctx, HelperPlacement{NodeID: nodeID, Endpoint: alloc.Endpoint})
	if err != nil {
		return nil, err
	}
	if !handshake {
		return conn, nil
	}
	hctx, cancel := context.WithTimeout(ctx, r.cfg.HandshakeTimeout)
	defer cancel()
	hs, err := conn.Handshake(hctx, &helperv1.HandshakeRequest{
		ProtocolVersion: HelperProtocolVersion, PrimaryWorkloadVersion: r.cfg.WorkloadVersion, NodeId: nodeID,
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if reason := r.incompatible(hs, nodeID, true); reason != "" {
		_ = conn.Close()
		return nil, fmt.Errorf("helper reconcile: helper not usable (%s)", reason)
	}
	return conn, nil
}

// Inspect implements nodes.Inspector: it dials the node that holds an attempt and
// asks what it knows. Read-only; any failure to get an answer is an error.
func (r *RemoteWorkerRunner) Inspect(ctx context.Context, nodeID, jobID, attemptID string) (nodes.HelperView, error) {
	if r == nil {
		return nodes.HelperView{}, errNoNodeEndpoint
	}
	ctx, cancel := context.WithTimeout(ctx, r.cfg.RPCTimeout)
	defer cancel()
	conn, err := r.dialNode(ctx, nodeID, false)
	if err != nil {
		return nodes.HelperView{}, err
	}
	defer conn.Close()
	resp, err := conn.InspectAttempt(ctx, &helperv1.InspectAttemptRequest{JobId: jobID, AttemptId: attemptID})
	if err != nil {
		return nodes.HelperView{}, err
	}
	return helperViewOf(resp), nil
}

// helperViewOf trims an InspectAttempt answer to what the reconcile policy judges.
func helperViewOf(r *helperv1.InspectAttemptResponse) nodes.HelperView {
	v := nodes.HelperView{
		Generation: r.GetLeaseGeneration(), Fingerprint: r.GetInputFingerprint(),
		Submitted: r.GetSubmitted() || r.GetOutcome().GetSubmitted(), LastSequence: r.GetLastSequence(),
		HasOutcome: r.GetOutcome() != nil,
	}
	switch r.GetState() {
	case helperv1.AttemptState_ATTEMPT_STATE_ACCEPTED, helperv1.AttemptState_ATTEMPT_STATE_RUNNING,
		helperv1.AttemptState_ATTEMPT_STATE_SUBMITTED:
		v.State = nodes.HelperAttemptRunning
	case helperv1.AttemptState_ATTEMPT_STATE_FINISHED:
		v.State = nodes.HelperAttemptFinished
	default: // UNKNOWN and anything unrecognised: no record
		v.State = nodes.HelperAttemptUnknown
	}
	return v
}

// Resume completes a submitted attempt the ledger still lists as ours but no
// runner holds (a gateway restart): it adopts the lease, waits for the helper to
// finish the attempt and commits its outcome. The return value follows Run: nil
// means the attempt reached an end the store holds (the caller reads the job and
// closes the lease), errors.Is(err, ErrAmbiguous) means fail the job closed, and a
// *HelperRetryError means hold it and ask again. by is the end of the reconcile
// window (zero: none): a helper that still cannot be dialed by then fails the job
// closed instead of holding it for ever.
func (r *RemoteWorkerRunner) Resume(ctx context.Context, env DispatchEnvelope, att jobstore.Attempt, by time.Time) error {
	if r == nil {
		return errNoNodeEndpoint
	}
	a := &remoteAttempt{
		r: r, env: env, node: att.NodeID, attemptID: att.AttemptID, workload: att.WorkloadVersion,
		fingerprint: att.InputFingerprint, ref: att.Ref(), generation: att.Generation,
	}
	// The ledger says submitted, or the helper does: either way a failure from here
	// on is post-submit and is never replayed.
	a.submitted.Store(true)
	return a.resume(ctx, att, by)
}

func (a *remoteAttempt) resume(ctx context.Context, att jobstore.Attempt, by time.Time) error {
	cfg := a.cfg()
	conn, err := a.r.dialNode(ctx, a.node, true)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !by.IsZero() && !a.r.clock.Now().Before(by) {
			a.log().Error("helper not usable for resuming the attempt and the reconcile window is over")
			return &reconcileFailure{reason: nodes.ReconcileHelperUnreachable}
		}
		a.log().Warn("helper not usable for resuming the attempt; holding the job back")
		return &HelperRetryError{Reason: "helper_unreachable", RetryAfter: cfg.RetryDelay, Err: err}
	}
	a.conn = conn
	defer conn.Close()

	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	// The helper stops an attempt at its own deadline; do not wait on one past it.
	budget := max(time.Until(att.CreatedAt.Add(cfg.MaxRuntime+helperRunSlack)), helperRunSlack)
	deadline := time.AfterFunc(budget, func() { cancel(errRunDeadline) })
	defer deadline.Stop()
	keeperDone := make(chan struct{})
	go func() {
		defer close(keeperDone)
		a.keepLease(runCtx, cancel)
	}()

	err = a.collect(runCtx, att)
	cancel(nil)
	<-keeperDone
	return a.finish(err)
}

// collect polls the helper until the attempt has ended, then commits its outcome.
// While it runs, the lease is taken over at once and kept by keepLease.
func (a *remoteAttempt) collect(ctx context.Context, att jobstore.Attempt) error {
	cfg := a.cfg()
	lastOK := a.r.clock.Now()
	renewed := false
	for {
		resp, err := a.inspect(ctx)
		switch {
		case ctx.Err() != nil:
			return context.Cause(ctx)
		case err != nil:
			if silent := a.r.clock.Now().Sub(lastOK); silent >= cfg.LeaseTTL {
				return &helperLostError{reason: "inspect_failed", cause: err}
			}
			a.log().Warn("helper did not answer an inspection; will retry")
		default:
			lastOK = a.r.clock.Now()
			v := helperViewOf(resp)
			switch {
			case v.State == nodes.HelperAttemptUnknown:
				return &helperLostError{reason: "helper_no_record"}
			case !v.Consistent(att):
				return &helperLostError{reason: "stale_helper_result"}
			case v.State == nodes.HelperAttemptFinished:
				return a.commitOutcome(ctx, att, resp)
			case !renewed:
				// First sight of a live attempt: take its lease over now, not at the
				// first renewal tick, so it does not lapse under the new holder.
				rerr := a.renew(ctx)
				var lost *helperLostError
				switch {
				case rerr == nil:
					renewed = true
				case errors.Is(rerr, ErrRemoteLeaseLost), errors.As(rerr, &lost):
					return rerr
				default:
					a.log().Warn("attempt lease renewal failed while resuming; will retry", "error", rerr)
				}
			}
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(cfg.PollEvery):
		}
	}
}

func (a *remoteAttempt) inspect(ctx context.Context) (*helperv1.InspectAttemptResponse, error) {
	ictx, cancel := context.WithTimeout(ctx, a.cfg().RPCTimeout)
	defer cancel()
	return a.conn.InspectAttempt(ictx, &helperv1.InspectAttemptRequest{JobId: a.env.JobID, AttemptId: a.attemptID})
}

// commitOutcome commits the finished attempt's terminal event, built from the
// inspection, through the fenced ingest. The ingest validates it exactly like a
// streamed terminal (outcome shape, size, a completed outcome needs a result, a
// failure after submission is failed_terminal with reconcile_required).
func (a *remoteAttempt) commitOutcome(ctx context.Context, att jobstore.Attempt, resp *helperv1.InspectAttemptResponse) error {
	cfg := a.cfg()
	lostFence := func(op string, err error) error {
		if errors.Is(err, jobstore.ErrAttemptFenced) {
			return fmt.Errorf("%w: %w", ErrRemoteLeaseLost, err)
		}
		return fmt.Errorf("helper reconcile: %s: %w", op, err)
	}
	// The helper saw the prompt leave before the primary read the event (the
	// primary was lost in between): record the boundary first, so the ingest judges
	// the terminal as submitted.
	if (resp.GetSubmitted() || resp.GetOutcome().GetSubmitted()) && !att.Submitted() {
		if _, err := cfg.Store.MarkSubmitted(ctx, a.ref); err != nil {
			return lostFence("mark submitted", err)
		}
	}
	ingest, err := OpenHelperIngest(ctx, HelperIngestConfig{Store: cfg.Store, Audit: cfg.Audit, Limits: cfg.Limits},
		HelperIngestBinding{
			TenantID: a.env.TenantID, AppID: a.env.AppID, JobID: a.env.JobID, AttemptID: a.attemptID,
			Generation: a.generation, NodeID: a.node, InputFingerprint: a.fingerprint, WorkloadVersion: a.workload,
			// Only the terminal event is committed: it is the attempt's last sequence.
			ResumeAfter: resp.GetLastSequence() - 1,
		})
	if err != nil {
		return lostFence("open ingest", err)
	}
	a.ingest = ingest
	done, err := a.accept(ctx, &helperv1.AttemptEvent{
		AttemptId: a.attemptID, Sequence: resp.GetLastSequence(), LeaseGeneration: resp.GetLeaseGeneration(),
		Type: helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL, CreatedAt: timestamppb.Now(), Outcome: resp.GetOutcome(),
	})
	if err != nil {
		return err
	}
	if !done {
		return errors.New("helper reconcile: the outcome did not end the attempt")
	}
	return nil
}

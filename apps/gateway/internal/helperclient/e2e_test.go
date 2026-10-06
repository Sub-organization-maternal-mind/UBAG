package helperclient

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/helper"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// These tests run the primary's RemoteWorkerRunner against the real ubag-helper
// service over real mutual TLS on loopback: the dial, the handshake checks, the
// ledger, the fenced ingest, the lease renewals and the stop all cross a real
// gRPC connection. Only the browser is fake (a helper.Runner script).

const (
	evStarted   = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_STARTED
	evSubmitted = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_PROMPT_SUBMITTED
	evToken     = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TOKEN
	evTerminal  = helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL
)

type onePicker struct{ place executor.HelperPlacement }

func (p onePicker) Pick(context.Context, executor.HelperPickRequest) (executor.HelperPlacement, error) {
	return p.place, nil
}

type e2e struct {
	t      *testing.T
	rig    *rig
	store  *jobstore.MemoryStore
	runner *executor.RemoteWorkerRunner
}

func newE2E(t *testing.T, script helper.RunnerFunc, mut ...func(*executor.RemoteConfig)) *e2e {
	t.Helper()
	r := newRig(t, rigOptions{runner: script})
	dialer := r.dialer(r.primary, nil, nil)
	store := jobstore.NewMemoryStore()
	cfg := executor.RemoteConfig{
		Store: store, WorkloadVersion: "w1", RegistryDigest: testDigest,
		Picker: onePicker{executor.HelperPlacement{NodeID: testNode, Endpoint: r.addr, ProfileRef: "pr_e2e"}},
		Dialer: executor.HelperDialFunc(func(ctx context.Context, p executor.HelperPlacement) (executor.HelperConn, error) {
			conn, err := dialer.Dial(ctx, p.NodeID, p.Endpoint)
			if err != nil {
				return nil, err
			}
			return conn, nil
		}),
		RetryDelay: 20 * time.Millisecond, ReconnectDelay: 10 * time.Millisecond,
	}
	for _, m := range mut {
		m(&cfg)
	}
	runner, err := executor.NewRemoteWorkerRunner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &e2e{t: t, rig: r, store: store, runner: runner}
}

var e2eJobs atomic.Int64

func (e *e2e) job() (jobstore.Job, executor.DispatchEnvelope) {
	e.t.Helper()
	n := e2eJobs.Add(1)
	job, err := e.store.Create(context.Background(), jobstore.CreateRequest{
		APIVersion: "2026-05-22", TenantID: "tenant_a", AppID: "app_a", IdempotencyKey: fmt.Sprintf("e2e_job_key_%08d", n),
		Target: "mock", CommandType: "submit", Input: map[string]any{"prompt": "hi"}, TraceID: fmt.Sprintf("trace_e2e_%d", n),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return job, executor.EnvelopeFromJob(job)
}

// run places and runs one attempt and returns what Run returned.
func (e *e2e) run(ctx context.Context, env executor.DispatchEnvelope) error {
	e.t.Helper()
	placed, err := e.runner.Place(ctx, env)
	if err != nil || placed == nil {
		e.t.Fatalf("Place = %v, %v", placed, err)
	}
	return e.runner.Run(ctx, env, placed)
}

func completes(text string) helper.RunnerFunc {
	return func(_ context.Context, _ helper.AttemptSpec, emit helper.EmitFunc) error {
		for _, ev := range []helper.Event{
			{Type: evStarted, DataJSON: `{"phase":"start"}`},
			{Type: evSubmitted},
			{Type: evToken, DataJSON: `{"delta":{"text":"hi"}}`},
			{Type: evTerminal, Outcome: &helperv1.AttemptOutcome{Status: helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED, ResultJson: `{"type":"text","text":"` + text + `"}`}},
		} {
			if err := emit(ev); err != nil {
				return err
			}
		}
		return nil
	}
}

func (e *e2e) status(id string) jobstore.Status {
	e.t.Helper()
	job, found, err := e.store.Get(context.Background(), id)
	if err != nil || !found {
		e.t.Fatalf("Get = %v, %v", found, err)
	}
	return job.Status
}

func TestRemoteRunnerCompletesAJobOnTheRealHelperService(t *testing.T) {
	var seen helper.AttemptSpec
	script := completes("from the real helper")
	e := newE2E(t, func(ctx context.Context, spec helper.AttemptSpec, emit helper.EmitFunc) error {
		seen = spec
		return script(ctx, spec, emit)
	})
	job, env := e.job()
	if err := e.run(t.Context(), env); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := e.status(job.ID); got != jobstore.StatusCompleted {
		t.Fatalf("job status = %s", got)
	}
	if seen.Provider != "mock" || seen.Target != "mock" || seen.CommandType != "submit" || seen.IdentityRef != "pr_e2e" ||
		seen.JobID != job.ID || seen.Generation != 1 || seen.InputJSON != `{"prompt":"hi"}` {
		t.Fatalf("the helper received %+v", seen)
	}
	attempts, _ := e.store.ListAttempts(context.Background(), job.ID)
	if len(attempts) != 1 || attempts[0].State != jobstore.AttemptFinished || !attempts[0].Submitted() || attempts[0].NodeID != testNode {
		t.Fatalf("attempts = %+v", attempts)
	}

	// The helper agrees: the attempt is finished and completed.
	conn, err := e.rig.dialer(e.rig.primary, nil, nil).Dial(t.Context(), testNode, e.rig.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	insp, err := conn.InspectAttempt(t.Context(), &helperv1.InspectAttemptRequest{JobId: job.ID, AttemptId: attempts[0].AttemptID})
	if err != nil || insp.GetState() != helperv1.AttemptState_ATTEMPT_STATE_FINISHED ||
		insp.GetOutcome().GetStatus() != helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED || !insp.GetSubmitted() {
		t.Fatalf("InspectAttempt = %v, %v", insp, err)
	}
}

// With a lease of 600 ms renewed every 100 ms, an attempt that runs for 1.5 s
// lives only if the primary's renewals really extend the helper's own lease: the
// helper kills an attempt that is not renewed.
func TestRemoteRunnerRenewalsKeepTheHelperAttemptAlive(t *testing.T) {
	e := newE2E(t, func(ctx context.Context, _ helper.AttemptSpec, emit helper.EmitFunc) error {
		_ = emit(helper.Event{Type: evStarted})
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		return emit(helper.Event{Type: evTerminal, Outcome: &helperv1.AttemptOutcome{
			Status: helperv1.AttemptStatus_ATTEMPT_STATUS_COMPLETED, ResultJson: `{"type":"text","text":"alive"}`}})
	}, func(c *executor.RemoteConfig) {
		c.LeaseTTL, c.RenewEvery, c.MaxClockSkew = 600*time.Millisecond, 100*time.Millisecond, 50*time.Millisecond
	})
	job, env := e.job()
	if err := e.run(t.Context(), env); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := e.status(job.ID); got != jobstore.StatusCompleted {
		t.Fatalf("job status = %s: the helper let the attempt lapse despite the renewals", got)
	}
}

func TestRemoteRunnerCancelStopsTheRealHelperAttempt(t *testing.T) {
	cause := make(chan error, 1)
	started := make(chan struct{})
	e := newE2E(t, func(ctx context.Context, _ helper.AttemptSpec, emit helper.EmitFunc) error {
		_ = emit(helper.Event{Type: evStarted})
		close(started)
		<-ctx.Done()
		cause <- context.Cause(ctx)
		return ctx.Err()
	})
	_, env := e.job()
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() { errc <- e.run(ctx, env) }()
	<-started
	cancel()

	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want the cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	select {
	case c := <-cause:
		if c == nil {
			t.Fatal("the helper's runner was stopped without a cause")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the helper's attempt was not stopped (it would run until its lease lapsed)")
	}
}

func TestRemoteRunnerHelperLostOverARealConnection(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		name := "before submission is held back for reassignment"
		if submitted {
			name = "after submission fails closed"
		}
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			e := newE2E(t, func(ctx context.Context, _ helper.AttemptSpec, emit helper.EmitFunc) error {
				_ = emit(helper.Event{Type: evStarted})
				if submitted {
					_ = emit(helper.Event{Type: evSubmitted})
				}
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}, func(c *executor.RemoteConfig) {
				c.LeaseTTL, c.RenewEvery, c.MaxClockSkew = 600*time.Millisecond, 100*time.Millisecond, 50*time.Millisecond
			})
			_, env := e.job()
			errc := make(chan error, 1)
			go func() { errc <- e.run(t.Context(), env) }()
			<-started
			time.Sleep(50 * time.Millisecond) // let the primary read the events
			e.rig.grpcSrv.Stop()              // the node's network goes away

			var err error
			select {
			case err = <-errc:
			case <-time.After(15 * time.Second):
				t.Fatal("Run did not return after the helper was lost")
			}
			var held *executor.HelperRetryError
			switch {
			case submitted && !errors.Is(err, executor.ErrAmbiguous):
				t.Fatalf("Run = %v, want ErrAmbiguous", err)
			case !submitted && (!errors.As(err, &held) || held.Reason != "helper_lost"):
				t.Fatalf("Run = %v, want a held-back job", err)
			}
		})
	}
}

// A second attempt for an identity the helper is already using is refused by the
// helper's own admission gate; the primary learns it at once (InspectAttempt:
// no such attempt) and holds the job back instead of waiting out a lease.
func TestRemoteRunnerHelperAdmissionRefusalIsHeldBackAtOnce(t *testing.T) {
	started := make(chan struct{}, 1)
	e := newE2E(t, func(ctx context.Context, _ helper.AttemptSpec, emit helper.EmitFunc) error {
		_ = emit(helper.Event{Type: evStarted})
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	})
	_, first := e.job()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = e.run(ctx, first) }()
	<-started

	_, second := e.job()
	begin := time.Now()
	err := e.run(t.Context(), second)
	var held *executor.HelperRetryError
	if !errors.As(err, &held) || held.Reason != "helper_lost" {
		t.Fatalf("Run = %v, want a held-back job", err)
	}
	if time.Since(begin) > 5*time.Second {
		t.Fatal("a refusal waited for a lease to lapse")
	}
}

func TestRemoteRunnerRefusesAHelperWithAnotherAdapterRegistry(t *testing.T) {
	e := newE2E(t, completes("never"), func(c *executor.RemoteConfig) {
		c.RegistryDigest = "b3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7a3c1e5f7"
	})
	job, env := e.job()
	err := e.run(t.Context(), env)
	var held *executor.HelperRetryError
	if !errors.As(err, &held) || held.Reason != "helper_registry_digest" {
		t.Fatalf("Run = %v, want a held-back job (registry digest)", err)
	}
	if attempts, _ := e.store.ListAttempts(context.Background(), job.ID); len(attempts) != 0 {
		t.Fatal("an attempt was leased on a helper with another adapter registry")
	}
}

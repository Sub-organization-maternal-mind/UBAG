package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// The attempt ledger contract suite. It runs against the memory store here and
// against Postgres in postgres_attempts_test.go (env-gated), so the two stores
// are held to one behaviour: stale-generation and inactive-attempt commits are
// rejected, replays are no-ops, BeginAttempt is a generation CAS.

type attemptTestStore interface {
	Store
	AttemptStore
}

type attemptClock struct {
	mu sync.Mutex
	t  time.Time
}

func newAttemptClock() *attemptClock {
	return &attemptClock{t: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
}

func (c *attemptClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *attemptClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type attemptEnv struct {
	store  attemptTestStore
	clock  *attemptClock
	newJob func(t *testing.T) Job
}

func TestMemoryAttemptStoreContract(t *testing.T) {
	runAttemptStoreContract(t, func(t *testing.T) attemptEnv {
		clock := newAttemptClock()
		store := NewMemoryStore()
		store.now = clock.Now
		var mu sync.Mutex
		n := 0
		return attemptEnv{store: store, clock: clock, newJob: func(t *testing.T) Job {
			mu.Lock()
			n++
			id := n
			mu.Unlock()
			return createAttemptTestJob(t, store, "tenant_attempts", fmt.Sprintf("trace_attempt_%d", id))
		}}
	})
}

func createAttemptTestJob(t *testing.T, store Store, tenant, trace string) Job {
	t.Helper()
	job, err := store.Create(context.Background(), CreateRequest{
		APIVersion:  "2026-05-22",
		TenantID:    tenant,
		AppID:       "app_attempts",
		Target:      "mock",
		CommandType: "submit",
		Input:       map[string]any{"prompt": "hello"},
		TraceID:     trace,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return job
}

func attemptEvent(job Job, id string, seq int, eventType string, data map[string]any) WorkerEvent {
	return WorkerEvent{EventID: id, JobID: job.ID, APIVersion: job.APIVersion, Type: eventType, Sequence: seq, TraceID: job.TraceID, Data: data}
}

func wantAttemptErr(t *testing.T, err error, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("err = %v, want %v", err, target)
	}
}

func eventCount(t *testing.T, store Store, jobID string) int {
	t.Helper()
	events, found, err := store.ListEvents(context.Background(), jobID, 0, 1000)
	if err != nil || !found {
		t.Fatalf("ListEvents found=%v err=%v", found, err)
	}
	return len(events)
}

func runAttemptStoreContract(t *testing.T, mk func(t *testing.T) attemptEnv) {
	ctx := context.Background()
	begin := func(t *testing.T, env attemptEnv, job Job, id string, expected uint64, ttl time.Duration) Attempt {
		t.Helper()
		a, err := env.store.BeginAttempt(ctx, BeginAttemptRequest{
			JobID: job.ID, AttemptID: id, NodeID: "node_a", ExpectedGeneration: expected,
			TTL: ttl, InputFingerprint: "fp1", WorkloadVersion: "w1",
		})
		if err != nil {
			t.Fatalf("BeginAttempt %s: %v", id, err)
		}
		return a
	}

	t.Run("begin is a generation CAS and replays are no-ops", func(t *testing.T) {
		env := mk(t)
		job := env.newJob(t)
		req := BeginAttemptRequest{JobID: job.ID, AttemptID: "att_one", NodeID: "node_a", InputFingerprint: "fp1", WorkloadVersion: "w1"}
		a, err := env.store.BeginAttempt(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if a.Generation != 1 || a.State != AttemptActive || a.NodeID != "node_a" || a.Submitted() {
			t.Fatalf("first attempt = %#v", a)
		}
		if want := env.clock.Now().Add(DefaultAttemptLeaseTTL); !a.LeaseExpiresAt.Equal(want) {
			t.Fatalf("lease expires %s, want %s", a.LeaseExpiresAt, want)
		}

		replay, err := env.store.BeginAttempt(ctx, req)
		if err != nil || replay.Generation != 1 || replay.AttemptID != "att_one" {
			t.Fatalf("replay = %#v err=%v", replay, err)
		}
		if list, _ := env.store.ListAttempts(ctx, job.ID); len(list) != 1 {
			t.Fatalf("replay created a row: %d attempts", len(list))
		}

		other := BeginAttemptRequest{JobID: job.ID, AttemptID: "att_two", NodeID: "node_b", InputFingerprint: "fp1", WorkloadVersion: "w1"}
		_, err = env.store.BeginAttempt(ctx, other)
		wantAttemptErr(t, err, ErrAttemptConflict) // generation 0 is no longer the next one: CAS lost
		other.ExpectedGeneration = 1
		_, err = env.store.BeginAttempt(ctx, other)
		wantAttemptErr(t, err, ErrAttemptConflict) // right generation, but att_one still holds the lease
		req.NodeID = "node_c"
		_, err = env.store.BeginAttempt(ctx, req)
		wantAttemptErr(t, err, ErrAttemptConflict) // id reused with other parameters

		_, err = env.store.BeginAttempt(ctx, BeginAttemptRequest{JobID: job.ID, AttemptID: "bad id"})
		wantAttemptErr(t, err, ErrAttemptInvalid)
		_, err = env.store.BeginAttempt(ctx, BeginAttemptRequest{JobID: job.ID, AttemptID: "att_x", TTL: MaxAttemptLeaseTTL + time.Second})
		wantAttemptErr(t, err, ErrAttemptInvalid)
		_, err = env.store.BeginAttempt(ctx, BeginAttemptRequest{JobID: "job_missing", AttemptID: "att_x"})
		wantAttemptErr(t, err, ErrAttemptNotFound)
	})

	t.Run("renew extends but never shortens and is fenced", func(t *testing.T) {
		env := mk(t)
		job := env.newJob(t)
		a := begin(t, env, job, "att_one", 0, time.Minute)

		renewed, err := env.store.RenewAttempt(ctx, a.Ref(), 5*time.Minute)
		if err != nil || !renewed.LeaseExpiresAt.Equal(env.clock.Now().Add(5*time.Minute)) {
			t.Fatalf("renew = %#v err=%v", renewed, err)
		}
		shorter, err := env.store.RenewAttempt(ctx, a.Ref(), time.Second)
		if err != nil || !shorter.LeaseExpiresAt.Equal(renewed.LeaseExpiresAt) {
			t.Fatalf("a shorter renew shortened the lease: %v err=%v", shorter.LeaseExpiresAt, err)
		}

		stale := a.Ref()
		stale.Generation = 2
		_, err = env.store.RenewAttempt(ctx, stale, 0)
		wantAttemptErr(t, err, ErrAttemptStale)
		wantAttemptErr(t, err, ErrAttemptFenced)
		unknown := a.Ref()
		unknown.AttemptID = "att_unknown"
		_, err = env.store.RenewAttempt(ctx, unknown, 0)
		wantAttemptErr(t, err, ErrAttemptNotFound)
	})

	t.Run("a lapsed lease is superseded and the old writer is fenced", func(t *testing.T) {
		env := mk(t)
		job := env.newJob(t)
		a := begin(t, env, job, "att_one", 0, 30*time.Second)
		before := eventCount(t, env.store, job.ID)

		env.clock.Advance(29 * time.Second)
		if _, err := env.store.BeginAttempt(ctx, BeginAttemptRequest{JobID: job.ID, AttemptID: "att_two", ExpectedGeneration: 1, InputFingerprint: "fp1", WorkloadVersion: "w1"}); !errors.Is(err, ErrAttemptConflict) {
			t.Fatalf("successor before the lease lapsed: err = %v", err)
		}
		env.clock.Advance(2 * time.Second)
		b := begin(t, env, job, "att_two", 1, 0)
		if b.Generation != 2 {
			t.Fatalf("successor generation = %d", b.Generation)
		}
		list, err := env.store.ListAttempts(ctx, job.ID)
		if err != nil || len(list) != 2 || list[0].State != AttemptExpired || list[0].EndedAt == nil || list[1].State != AttemptActive {
			t.Fatalf("history after supersede = %#v err=%v", list, err)
		}

		running := []WorkerEvent{attemptEvent(job, "stale_running", 2, "running", map[string]any{"status": "running"})}
		_, err = env.store.CommitEvents(ctx, a.Ref(), running)
		wantAttemptErr(t, err, ErrAttemptInactive)
		wantAttemptErr(t, err, ErrAttemptFenced)
		wrongGen := b.Ref()
		wrongGen.Generation = 1
		_, err = env.store.CommitEvents(ctx, wrongGen, running)
		wantAttemptErr(t, err, ErrAttemptStale)
		_, err = env.store.RenewAttempt(ctx, a.Ref(), 0)
		wantAttemptErr(t, err, ErrAttemptFenced)
		_, err = env.store.MarkSubmitted(ctx, a.Ref())
		wantAttemptErr(t, err, ErrAttemptFenced)
		if got := eventCount(t, env.store, job.ID); got != before {
			t.Fatalf("a fenced commit wrote %d events", got-before)
		}

		got, err := env.store.CommitEvents(ctx, b.Ref(), []WorkerEvent{attemptEvent(job, "fresh_running", 2, "running", map[string]any{"status": "running"})})
		if err != nil || got.Status != StatusRunning {
			t.Fatalf("current attempt commit: status=%s err=%v", got.Status, err)
		}
	})

	t.Run("commit replay is a no-op", func(t *testing.T) {
		env := mk(t)
		job := env.newJob(t)
		a := begin(t, env, job, "att_one", 0, 0)
		batch := []WorkerEvent{
			attemptEvent(job, "e_running", 2, "running", map[string]any{"status": "running"}),
			attemptEvent(job, "e_token", 3, "token", map[string]any{"status": "token_streaming", "delta": map[string]any{"text": "hi"}}),
		}
		first, err := env.store.CommitEvents(ctx, a.Ref(), batch)
		if err != nil {
			t.Fatal(err)
		}
		applied := eventCount(t, env.store, job.ID)
		if applied != 3 || first.Status != StatusTokenStreaming {
			t.Fatalf("applied=%d status=%s", applied, first.Status)
		}
		replay, err := env.store.CommitEvents(ctx, a.Ref(), batch)
		if err != nil || replay.Status != first.Status {
			t.Fatalf("replay status=%s err=%v", replay.Status, err)
		}
		if got := eventCount(t, env.store, job.ID); got != applied {
			t.Fatalf("replay changed the event log: %d -> %d", applied, got)
		}
	})

	t.Run("commit is all or nothing", func(t *testing.T) {
		env := mk(t)
		job := env.newJob(t)
		a := begin(t, env, job, "att_one", 0, 0)
		good := attemptEvent(job, "e_good", 2, "running", map[string]any{"status": "running"})
		bad := attemptEvent(job, "e_bad", 3, "token", nil)
		bad.APIVersion = "1999-01-01" // passes the shape check, fails against the job
		if _, err := env.store.CommitEvents(ctx, a.Ref(), []WorkerEvent{good, bad}); err == nil {
			t.Fatal("batch with a mismatching event committed")
		}
		if got := eventCount(t, env.store, job.ID); got != 1 {
			t.Fatalf("partial batch left %d events", got)
		}
		if got, _, _ := env.store.Get(ctx, job.ID); got.Status != StatusQueued {
			t.Fatalf("partial batch moved the job to %s", got.Status)
		}
		// The rolled-back event's dedupe key must be gone too.
		if _, err := env.store.CommitEvents(ctx, a.Ref(), []WorkerEvent{good}); err != nil {
			t.Fatal(err)
		}
		if got := eventCount(t, env.store, job.ID); got != 2 {
			t.Fatalf("event after rollback not applied: %d events", got)
		}
	})

	t.Run("commit validates scope and size", func(t *testing.T) {
		env := mk(t)
		job := env.newJob(t)
		other := env.newJob(t)
		a := begin(t, env, job, "att_one", 0, 0)

		_, err := env.store.CommitEvents(ctx, a.Ref(), []WorkerEvent{attemptEvent(other, "e1", 2, "running", nil)})
		wantAttemptErr(t, err, ErrAttemptEventMismatch)
		_, err = env.store.CommitEvents(ctx, a.Ref(), []WorkerEvent{attemptEvent(job, "e2", 2, "running", map[string]any{"attempt_id": "att_other"})})
		wantAttemptErr(t, err, ErrAttemptEventMismatch)
		if _, err = env.store.CommitEvents(ctx, a.Ref(), []WorkerEvent{attemptEvent(job, "e3", 2, "running", map[string]any{"attempt_id": "att_one"})}); err != nil {
			t.Fatalf("event stamped by the committing attempt: %v", err)
		}
		_, err = env.store.CommitEvents(ctx, a.Ref(), nil)
		wantAttemptErr(t, err, ErrAttemptInvalid)
		huge := make([]WorkerEvent, MaxAttemptCommitEvents+1)
		for i := range huge {
			huge[i] = attemptEvent(job, fmt.Sprintf("big_%d", i), i+10, "running", nil)
		}
		_, err = env.store.CommitEvents(ctx, a.Ref(), huge)
		wantAttemptErr(t, err, ErrAttemptInvalid)
		_, err = env.store.CommitEvents(ctx, a.Ref(), []WorkerEvent{{JobID: job.ID, Type: "running"}})
		if err == nil {
			t.Fatal("event without api_version/trace_id committed")
		}
	})

	t.Run("the submission boundary blocks a blind retry", func(t *testing.T) {
		env := mk(t)
		job := env.newJob(t)
		a := begin(t, env, job, "att_one", 0, 30*time.Second)
		first, err := env.store.MarkSubmitted(ctx, a.Ref())
		if err != nil || !first.Submitted() {
			t.Fatalf("MarkSubmitted = %#v err=%v", first, err)
		}
		env.clock.Advance(time.Second)
		second, err := env.store.MarkSubmitted(ctx, a.Ref())
		if err != nil || !second.SubmittedAt.Equal(*first.SubmittedAt) {
			t.Fatalf("MarkSubmitted replay moved the boundary: %v err=%v", second.SubmittedAt, err)
		}

		env.clock.Advance(time.Minute)
		req := BeginAttemptRequest{JobID: job.ID, AttemptID: "att_two", ExpectedGeneration: 1, InputFingerprint: "fp1", WorkloadVersion: "w1"}
		_, err = env.store.BeginAttempt(ctx, req)
		wantAttemptErr(t, err, ErrAttemptSubmitted)
		if list, _ := env.store.ListAttempts(ctx, job.ID); len(list) != 1 || list[0].State != AttemptActive {
			t.Fatalf("a refused begin must not touch the predecessor: %#v", list)
		}
		req.AllowAfterSubmitted = true
		if b, err := env.store.BeginAttempt(ctx, req); err != nil || b.Generation != 2 {
			t.Fatalf("explicit reconcile retry = %#v err=%v", b, err)
		}
	})

	t.Run("a retried attempt cannot change its input", func(t *testing.T) {
		env := mk(t)
		job := env.newJob(t)
		begin(t, env, job, "att_one", 0, 30*time.Second)
		env.clock.Advance(time.Minute)
		_, err := env.store.BeginAttempt(ctx, BeginAttemptRequest{JobID: job.ID, AttemptID: "att_two", ExpectedGeneration: 1, InputFingerprint: "fp2"})
		wantAttemptErr(t, err, ErrAttemptFingerprint)
		if b := begin(t, env, job, "att_two", 1, 0); b.Generation != 2 {
			t.Fatalf("same fingerprint retry = %#v", b)
		}
	})

	t.Run("a terminal commit finishes the attempt", func(t *testing.T) {
		env := mk(t)
		job := env.newJob(t)
		a := begin(t, env, job, "att_one", 0, 0)
		done := []WorkerEvent{attemptEvent(job, "e_done", 2, "completed", map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "done"}})}
		got, err := env.store.CommitEvents(ctx, a.Ref(), done)
		if err != nil || got.Status != StatusCompleted {
			t.Fatalf("terminal commit status=%s err=%v", got.Status, err)
		}
		list, _ := env.store.ListAttempts(ctx, job.ID)
		if len(list) != 1 || list[0].State != AttemptFinished || list[0].EndedAt == nil {
			t.Fatalf("attempt after terminal commit = %#v", list)
		}
		before := eventCount(t, env.store, job.ID)
		replay, err := env.store.CommitEvents(ctx, a.Ref(), done) // lost ack, helper retries
		if err != nil || replay.Status != StatusCompleted || eventCount(t, env.store, job.ID) != before {
			t.Fatalf("replay against a finished attempt: status=%s err=%v", replay.Status, err)
		}
		_, err = env.store.RenewAttempt(ctx, a.Ref(), 0)
		wantAttemptErr(t, err, ErrAttemptInactive)
		_, err = env.store.BeginAttempt(ctx, BeginAttemptRequest{JobID: job.ID, AttemptID: "att_two", ExpectedGeneration: 1, InputFingerprint: "fp1"})
		wantAttemptErr(t, err, ErrAttemptJobTerminal)
	})

	t.Run("expire reaps only lapsed active attempts", func(t *testing.T) {
		env := mk(t)
		short, long := env.newJob(t), env.newJob(t)
		a := begin(t, env, short, "att_short", 0, 30*time.Second)
		b := begin(t, env, long, "att_long", 0, 10*time.Minute)
		env.clock.Advance(time.Minute)

		ours := func(list []Attempt) (out []Attempt) {
			for _, x := range list {
				if x.JobID == short.ID || x.JobID == long.ID {
					out = append(out, x)
				}
			}
			return out
		}
		reaped, err := env.store.ExpireAttempts(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := ours(reaped); len(got) != 1 || got[0].AttemptID != "att_short" || got[0].State != AttemptExpired || got[0].EndedAt == nil {
			t.Fatalf("reaped = %#v", got)
		}
		again, err := env.store.ExpireAttempts(ctx, 0)
		if err != nil || len(ours(again)) != 0 {
			t.Fatalf("second sweep reaped %#v err=%v", again, err)
		}
		_, err = env.store.CommitEvents(ctx, a.Ref(), []WorkerEvent{attemptEvent(short, "late", 2, "running", nil)})
		wantAttemptErr(t, err, ErrAttemptFenced)
		if _, err := env.store.RenewAttempt(ctx, b.Ref(), 0); err != nil {
			t.Fatalf("a live lease was reaped: %v", err)
		}
	})

	t.Run("concurrent begin has a single winner", func(t *testing.T) {
		env := mk(t)
		job := env.newJob(t)
		const racers = 8
		start := make(chan struct{})
		errs := make([]error, racers)
		var wg sync.WaitGroup
		for i := range racers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = env.store.BeginAttempt(ctx, BeginAttemptRequest{
					JobID: job.ID, AttemptID: fmt.Sprintf("att_race%d", i), InputFingerprint: "fp1",
				})
			}()
		}
		close(start)
		wg.Wait()
		winners := 0
		for _, err := range errs {
			switch {
			case err == nil:
				winners++
			case !errors.Is(err, ErrAttemptConflict):
				t.Fatalf("loser failed with %v, want ErrAttemptConflict", err)
			}
		}
		if list, _ := env.store.ListAttempts(ctx, job.ID); winners != 1 || len(list) != 1 {
			t.Fatalf("winners=%d attempts=%d, want exactly 1", winners, len(list))
		}
	})

	t.Run("concurrent stale-generation commits are rejected", func(t *testing.T) {
		env := mk(t)
		job := env.newJob(t)
		a := begin(t, env, job, "att_old", 0, 30*time.Second)
		env.clock.Advance(time.Minute)
		b := begin(t, env, job, "att_new", 1, 0)

		const writers = 8
		start := make(chan struct{})
		staleErrs := make([]error, writers)
		freshErrs := make([]error, writers)
		var wg sync.WaitGroup
		for i := range writers {
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, staleErrs[i] = env.store.CommitEvents(ctx, a.Ref(), []WorkerEvent{attemptEvent(job, fmt.Sprintf("stale_%d", i), 100+i, "running", map[string]any{"status": "running"})})
			}()
			go func() {
				defer wg.Done()
				<-start
				_, freshErrs[i] = env.store.CommitEvents(ctx, b.Ref(), []WorkerEvent{attemptEvent(job, fmt.Sprintf("fresh_%d", i), 200+i, "running", map[string]any{"status": "running"})})
			}()
		}
		close(start)
		wg.Wait()
		for i := range writers {
			wantAttemptErr(t, staleErrs[i], ErrAttemptFenced)
			if freshErrs[i] != nil {
				t.Fatalf("current attempt commit %d failed: %v", i, freshErrs[i])
			}
		}
		events, _, err := env.store.ListEvents(ctx, job.ID, 0, 1000)
		if err != nil || len(events) != 1+writers {
			t.Fatalf("events = %d err=%v, want %d", len(events), err, 1+writers)
		}
		for _, event := range events {
			if wm, _ := event.Data["worker_event"].(map[string]any); wm != nil && strings.HasPrefix(fmt.Sprint(wm["event_id"]), "stale_") {
				t.Fatalf("a stale-generation event reached the job: %#v", event)
			}
		}
	})

	t.Run("a fenced commit leaves the job untouched even with a terminal event", func(t *testing.T) {
		env := mk(t)
		job := env.newJob(t)
		a := begin(t, env, job, "att_old", 0, 30*time.Second)
		if _, err := env.store.CommitEvents(ctx, a.Ref(), []WorkerEvent{attemptEvent(job, "old_running", 2, "running", map[string]any{"status": "running"})}); err != nil {
			t.Fatal(err)
		}
		env.clock.Advance(time.Minute)
		b := begin(t, env, job, "att_new", 1, 0)
		before, _, err := env.store.Get(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		eventsBefore := eventCount(t, env.store, job.ID)

		done := attemptEvent(job, "stale_done", 3, "completed", map[string]any{"status": "completed", "result": map[string]any{"text": "stale answer"}})
		_, err = env.store.CommitEvents(ctx, a.Ref(), []WorkerEvent{done})
		wantAttemptErr(t, err, ErrAttemptFenced)
		forged := a.Ref()
		forged.Generation = b.Generation // right generation, wrong attempt id
		_, err = env.store.CommitEvents(ctx, forged, []WorkerEvent{done})
		wantAttemptErr(t, err, ErrAttemptFenced)

		after, _, err := env.store.Get(ctx, job.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("a fenced commit mutated the job:\nbefore %#v\nafter  %#v (err=%v)", before, after, err)
		}
		if after.Status != StatusRunning || after.Result != nil {
			t.Fatalf("job = %s result=%v, want running with no result", after.Status, after.Result)
		}
		if got := eventCount(t, env.store, job.ID); got != eventsBefore {
			t.Fatalf("a fenced commit wrote %d events", got-eventsBefore)
		}
		// The rejected event did not consume its dedupe key: the current attempt
		// can still commit the same key.
		got, err := env.store.CommitEvents(ctx, b.Ref(), []WorkerEvent{done})
		if err != nil || got.Status != StatusCompleted {
			t.Fatalf("current attempt could not use the key a stale writer was refused: status=%s err=%v", got.Status, err)
		}
	})

	t.Run("commit redacts secret-bearing data and rejects secret values", func(t *testing.T) {
		env := mk(t)
		job := env.newJob(t)
		a := begin(t, env, job, "att_one", 0, 0)
		secret := attemptEvent(job, "e_secret", 2, "token", map[string]any{
			"status":  "token_streaming",
			"delta":   map[string]any{"text": "hi"},
			"cookie":  "sessionid=abc123",
			"api_key": "sk-live-AAAA",
			"nested":  map[string]any{"password": "hunter2", "note": "fine"},
			"list":    []any{map[string]any{"storage_state": "state-blob"}},
		})
		if _, err := env.store.CommitEvents(ctx, a.Ref(), []WorkerEvent{secret}); err != nil {
			t.Fatal(err)
		}
		events, _, err := env.store.ListEvents(ctx, job.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		var committed Event
		for _, event := range events {
			if event.Type == "token" {
				committed = event
			}
		}
		if committed.Data["cookie"] != "[redacted]" || committed.Data["api_key"] != "[redacted]" {
			t.Fatalf("secret keys were not redacted: %#v", committed.Data)
		}
		if nested, _ := committed.Data["nested"].(map[string]any); nested["password"] != "[redacted]" || nested["note"] != "fine" {
			t.Fatalf("nested data = %#v", committed.Data["nested"])
		}
		raw, _ := json.Marshal(events)
		for _, leak := range []string{"sessionid=abc123", "sk-live-AAAA", "hunter2", "state-blob"} {
			if strings.Contains(string(raw), leak) {
				t.Fatalf("secret %q reached the event log: %s", leak, raw)
			}
		}

		// A secret-looking VALUE (not just key) is refused outright, all or nothing.
		before := eventCount(t, env.store, job.ID)
		leaky := attemptEvent(job, "e_leaky", 3, "token", map[string]any{"delta": map[string]any{"text": "Bearer abcdefghijklmnopqrstuvwxyz"}})
		if _, err := env.store.CommitEvents(ctx, a.Ref(), []WorkerEvent{leaky}); err == nil {
			t.Fatal("a bearer-token value was committed")
		}
		if got := eventCount(t, env.store, job.ID); got != before {
			t.Fatalf("refused secret value wrote %d events", got-before)
		}
	})
}

func TestValidAttemptID(t *testing.T) {
	for id, want := range map[string]bool{
		"att_01HXY5ZS3KAFQ3PZ8NQ8C7H1AV": true,
		"att_a":                          true,
		"att_":                           false,
		"att":                            false,
		"":                               false,
		"lease_123":                      false,
		"att_a-b":                        false,
		"att_a b":                        false,
		"att_" + strings.Repeat("a", MaxAttemptIDLength):   false, // 4+128 > 128
		"att_" + strings.Repeat("a", MaxAttemptIDLength-4): true,
	} {
		if got := ValidAttemptID(id); got != want {
			t.Errorf("ValidAttemptID(%q) = %v, want %v", id, got, want)
		}
	}
}

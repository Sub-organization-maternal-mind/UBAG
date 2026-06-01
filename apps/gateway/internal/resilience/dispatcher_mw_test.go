package resilience

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// mockDispatcher implements executor.Dispatcher for testing.
type mockDispatcher struct {
	mu          sync.Mutex
	enqueueErr  error
	enqueued    int
	enqueueCh   chan struct{} // closed after first enqueue; nil if unused
	receiptFunc func(job jobstore.Job) executor.Receipt
}

func (m *mockDispatcher) Ready(context.Context) error { return nil }

func (m *mockDispatcher) EnqueueJob(_ context.Context, job jobstore.Job) (executor.Receipt, error) {
	m.mu.Lock()
	m.enqueued++
	if m.enqueueCh != nil {
		select {
		case m.enqueueCh <- struct{}{}:
		default:
		}
	}
	m.mu.Unlock()
	if m.enqueueErr != nil {
		return executor.Receipt{}, m.enqueueErr
	}
	if m.receiptFunc != nil {
		return m.receiptFunc(job), nil
	}
	return executor.Receipt{
		Backend:    "mock",
		QueueName:  "jobs",
		MessageID:  job.ID,
		EnqueuedAt: time.Now().UTC(),
	}, nil
}

func (m *mockDispatcher) CancelJob(context.Context, jobstore.Job, string) error { return nil }

func (m *mockDispatcher) Stats(context.Context) (executor.Stats, error) {
	return executor.Stats{QueueName: "mock"}, nil
}

func (m *mockDispatcher) enqueuedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enqueued
}

// Test 1: nil registry returns next unchanged (passthrough).
func TestDispatcherMiddleware_NilRegistry_Passthrough(t *testing.T) {
	t.Parallel()
	mock := &mockDispatcher{}
	wrapped := DispatcherMiddleware(mock, nil)
	if wrapped != executor.Dispatcher(mock) {
		t.Fatal("expected passthrough: DispatcherMiddleware(mock, nil) should return mock unchanged")
	}
}

// Test 2: open breaker short-circuits EnqueueJob and returns *BreakerOpenError.
func TestDispatcherMiddleware_OpenBreaker_ShortCircuits(t *testing.T) {
	t.Parallel()

	cfg := Config{
		FailureThreshold:    1, // trip after 1 failure
		SuccessBudget:       1,
		CooldownBase:        60 * time.Second,
		CooldownMax:         120 * time.Second,
		HalfOpenMaxInflight: 1,
	}
	reg := NewRegistry(cfg)
	mock := &mockDispatcher{enqueueErr: fmt.Errorf("backend unavailable")}
	wrapped := DispatcherMiddleware(mock, reg)

	job := jobstore.Job{ID: "job-1", TenantID: "tenant-x", Target: "target-a"}

	// First call: should pass through and record failure, tripping the breaker.
	_, err := wrapped.EnqueueJob(context.Background(), job)
	if err == nil {
		t.Fatal("expected error from first enqueue, got nil")
	}
	// Breaker is now open; second call should be short-circuited.
	mock.enqueueErr = nil // underlying dispatcher is now healthy
	_, err = wrapped.EnqueueJob(context.Background(), job)
	if err == nil {
		t.Fatal("expected BreakerOpenError, got nil")
	}

	var breakerErr *BreakerOpenError
	if !errors.As(err, &breakerErr) {
		t.Fatalf("expected *BreakerOpenError via errors.As; got %T: %v", err, err)
	}
	if breakerErr.Target != "tenant-x/target-a" {
		t.Errorf("expected Target=tenant-x/target-a, got %q", breakerErr.Target)
	}
	if breakerErr.RetryAfter <= 0 {
		t.Errorf("expected positive RetryAfter, got %s", breakerErr.RetryAfter)
	}

	// Verify underlying dispatcher was NOT called for the second request.
	if got := mock.enqueuedCount(); got != 1 {
		t.Errorf("expected next.EnqueueJob called exactly once (for the failure), got %d", got)
	}
}

// Test 3: closed breaker calls through to next and records success.
func TestDispatcherMiddleware_ClosedBreaker_CallsThrough(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(DefaultConfig())
	mock := &mockDispatcher{}
	wrapped := DispatcherMiddleware(mock, reg)

	job := jobstore.Job{ID: "job-2", TenantID: "tenant-y", Target: "target-b"}
	receipt, err := wrapped.EnqueueJob(context.Background(), job)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if receipt.Backend != "mock" {
		t.Errorf("expected receipt.Backend=mock, got %q", receipt.Backend)
	}
	if mock.enqueuedCount() != 1 {
		t.Errorf("expected next.EnqueueJob called once, got %d", mock.enqueuedCount())
	}

	// Breaker should still be closed (no failures recorded).
	b := reg.Get(KindUpstream, "tenant-y/target-b")
	if b.State() != StateClosed {
		t.Errorf("expected breaker closed after success, got %s", b.State())
	}
}

// Test 4: failed EnqueueJob calls RecordFailure and error propagates.
func TestDispatcherMiddleware_FailedEnqueue_RecordsFailure(t *testing.T) {
	t.Parallel()

	cfg := Config{
		FailureThreshold:    5, // needs 5 failures to open
		SuccessBudget:       2,
		CooldownBase:        5 * time.Second,
		CooldownMax:         60 * time.Second,
		HalfOpenMaxInflight: 1,
	}
	reg := NewRegistry(cfg)
	wantErr := fmt.Errorf("queue full")
	mock := &mockDispatcher{enqueueErr: wantErr}
	wrapped := DispatcherMiddleware(mock, reg)

	job := jobstore.Job{ID: "job-3", TenantID: "tenant-z", Target: "target-c"}
	_, err := wrapped.EnqueueJob(context.Background(), job)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped error chain to contain wantErr, got %v", err)
	}

	// After 1 failure the breaker should still be closed (threshold=5).
	b := reg.Get(KindUpstream, "tenant-z/target-c")
	if b.State() != StateClosed {
		t.Errorf("expected breaker still closed after 1 of 5 failures, got %s", b.State())
	}
}

// Test 5: concurrent EnqueueJob calls for the same target are safe (race detector).
func TestDispatcherMiddleware_Concurrent_RaceSafe(t *testing.T) {
	t.Parallel()

	reg := NewRegistry(DefaultConfig())
	mock := &mockDispatcher{}
	wrapped := DispatcherMiddleware(mock, reg)

	const workers = 20
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			job := jobstore.Job{ID: fmt.Sprintf("job-%d", i), Target: "shared-target"}
			_, _ = wrapped.EnqueueJob(context.Background(), job)
		}(i)
	}
	wg.Wait()

	if got := mock.enqueuedCount(); got != workers {
		t.Errorf("expected %d enqueues, got %d", workers, got)
	}
}

package executor

import (
	"context"
	"sync"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// CancelRegistry is the in-process cancel path for remote attempts
// (UBAG_HELPER_DISPATCH, P4.14).
//
// A local run is stopped by polling the job store every 250 ms for a cancel. A
// remote attempt holds a helper's only browser slot and its provider session, so
// the stop has to reach the helper (CancelAttempt) quickly, and polling the
// store at that cadence for every remote attempt is the wrong cost. The cancel
// API already runs in this process: it calls Dispatcher.CancelJob first and
// writes the canceled status second. NewCancelNotifier hooks that call, and the
// registry turns it into a wake-up for the consumer that is supervising the job.
//
// The signal is a HINT, not the truth. CancelJob runs BEFORE the status write,
// and a cancel that fails after it must not stop anything, so the consumer
// re-reads the job (jobCanceledWithin) before acting on a hint. The store stays
// the only authority; a cancel from another gateway process, or the stale-job
// reaper, never touches this registry and is still found by the slow
// safety-net read (cancelWatchFallback).
type CancelRegistry struct {
	mu       sync.Mutex
	watchers map[string]map[chan struct{}]struct{}
}

// NewCancelRegistry returns an empty registry.
func NewCancelRegistry() *CancelRegistry {
	return &CancelRegistry{watchers: map[string]map[chan struct{}]struct{}{}}
}

// Watch registers interest in a job. The channel receives a hint (capacity 1,
// the sender never blocks) when a cancel for the job is in flight; release
// removes the registration. Both are safe on a nil registry (a nil channel
// blocks forever and release does nothing).
func (r *CancelRegistry) Watch(jobID string) (<-chan struct{}, func()) {
	if r == nil {
		return nil, func() {}
	}
	ch := make(chan struct{}, 1)
	r.mu.Lock()
	if r.watchers[jobID] == nil {
		r.watchers[jobID] = map[chan struct{}]struct{}{}
	}
	r.watchers[jobID][ch] = struct{}{}
	r.mu.Unlock()
	return ch, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.watchers[jobID], ch)
		if len(r.watchers[jobID]) == 0 {
			delete(r.watchers, jobID)
		}
	}
}

// Hint wakes every watcher of the job and reports whether there was one.
func (r *CancelRegistry) Hint(jobID string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	woke := false
	for ch := range r.watchers[jobID] {
		select {
		case ch <- struct{}{}:
		default: // a hint is already pending
		}
		woke = true
	}
	return woke
}

// Watching reports how many jobs have a watcher (tests and diagnostics).
func (r *CancelRegistry) Watching() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.watchers)
}

type cancelNotifier struct {
	Dispatcher
	reg *CancelRegistry
}

// NewCancelNotifier wraps next so that a successful CancelJob hints the
// registry. A nil registry returns next unchanged.
func NewCancelNotifier(next Dispatcher, reg *CancelRegistry) Dispatcher {
	if reg == nil {
		return next
	}
	return &cancelNotifier{Dispatcher: next, reg: reg}
}

func (n *cancelNotifier) CancelJob(ctx context.Context, job jobstore.Job, reason string) error {
	if err := n.Dispatcher.CancelJob(ctx, job, reason); err != nil {
		return err
	}
	n.reg.Hint(job.ID)
	return nil
}

package jobs

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// DefaultEventFallbackInterval is the safety-net poll cadence used while the
// per-job wake hub is on (UBAG_EVENT_NOTIFY=local). The hub only sees writes
// made by this process, so a write by another gateway (shared Postgres) or a
// dropped wake is recovered within one fallback interval.
const DefaultEventFallbackInterval = 2 * time.Second

// eventHub is an in-process, per-job wake hub for SQL-store event waiters. It
// carries job ids only, never data: a wake is a hint to re-read the store, so a
// lost or spurious wake is harmless. Each waiter owns a cap-1 channel that is
// signalled with a non-blocking send (the same lossy-wake pattern as the file
// spool enqueue channel).
type eventHub struct {
	mu      sync.Mutex
	waiters map[string]map[chan struct{}]struct{}
}

func newEventHub() *eventHub {
	return &eventHub{waiters: make(map[string]map[chan struct{}]struct{})}
}

// subscribe registers a waiter for jobID. The caller must subscribe BEFORE its
// first store read so a commit landing between the read and the wait is not
// lost, and must call the returned cancel (defer) to unregister.
func (h *eventHub) subscribe(jobID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	set := h.waiters[jobID]
	if set == nil {
		set = make(map[chan struct{}]struct{})
		h.waiters[jobID] = set
	}
	set[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if set := h.waiters[jobID]; set != nil {
			delete(set, ch)
			if len(set) == 0 {
				delete(h.waiters, jobID)
			}
		}
	}
}

// notify wakes every waiter of jobID without ever blocking. Safe on a nil hub
// (flag off), so write sites call it unconditionally after a successful commit.
func (h *eventHub) notify(jobID string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	for ch := range h.waiters[jobID] {
		select {
		case ch <- struct{}{}:
		default: // a wake is already pending; one re-read covers both
		}
	}
	h.mu.Unlock()
}

// jitteredFallback spreads waiter re-reads by up to +20% so idle streams that
// started together do not poll in lockstep.
func jitteredFallback(d time.Duration) time.Duration {
	return d + time.Duration(rand.Int64N(int64(d)/5+1))
}

// waitEventsLoop is the shared SQL-store WaitEvents body. With hub == nil it is
// exactly the legacy fixed-interval poll; with a hub it re-reads on a wake and
// otherwise only at the (jittered) fallback cadence.
func waitEventsLoop(ctx context.Context, hub *eventHub, fallback, interval time.Duration, jobID string, read func() ([]Event, bool, error)) ([]Event, bool, error) {
	var wake <-chan struct{}
	if hub != nil {
		var cancel func()
		wake, cancel = hub.subscribe(jobID)
		defer cancel()
		interval = fallback
	}
	for {
		events, found, err := read()
		if err != nil || !found || len(events) > 0 {
			return events, found, err
		}
		wait := interval
		if hub != nil {
			wait = jitteredFallback(interval)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, true, ctx.Err()
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// ParseEventNotify maps UBAG_EVENT_NOTIFY to "hub on?". "" and "off" keep the
// legacy poll. "postgres" is reserved (LISTEN/NOTIFY is deferred) and, like any
// unknown value, reports ok=false so the caller can warn and stay on the poll.
func ParseEventNotify(mode string) (on bool, ok bool) {
	switch mode {
	case "", "off":
		return false, true
	case "local":
		return true, true
	}
	return false, false
}

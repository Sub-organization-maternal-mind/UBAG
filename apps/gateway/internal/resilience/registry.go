package resilience

import (
	"sync"
	"time"
)

// Kind categorises the dependency type for the circuit breaker.
type Kind string

const (
	KindAdapter  Kind = "adapter"
	KindWebhook  Kind = "webhook"
	KindUpstream Kind = "upstream"
)

// BreakerSnapshot is a point-in-time view of a single breaker for metric export.
//
// The caller (serve/httpapi layer) is expected to expose a gauge named
// "ubag_circuit_breaker_state" keyed by {kind, target} with values:
//
//	0 = closed (StateClosed)
//	1 = open   (StateOpen)
//	2 = half-open (StateHalfOpen)
type BreakerSnapshot struct {
	Kind   Kind
	Target string
	State  State
}

// defaultMaxBreakers bounds the registry. Targets are caller-supplied strings
// (webhook URLs, adapter names), so an unbounded map lets unvalidated targets
// grow it forever.
const defaultMaxBreakers = 10000

// registryEntry stores the metadata and breaker for a single (kind, target) pair.
type registryEntry struct {
	kind      Kind
	target    string
	b         *Breaker
	createdAt time.Time
}

// Registry is a concurrency-safe, lazy-initialising store of *Breaker instances
// keyed by (kind, target). It is bounded at defaultMaxBreakers — the
// oldest-created breaker is evicted first — so unvalidated target strings
// cannot grow it without limit. An evicted breaker that is used again is
// simply re-created closed.
type Registry struct {
	mu         sync.Mutex
	cfg        Config
	entries    map[string]registryEntry // key: kind + "\x00" + target
	order      []string                 // insertion order, oldest first
	maxEntries int
}

// NewRegistry creates a Registry that uses cfg for every breaker it creates.
func NewRegistry(cfg Config) *Registry {
	return &Registry{
		cfg:        cfg,
		entries:    make(map[string]registryEntry),
		maxEntries: defaultMaxBreakers,
	}
}

// registryKey returns the canonical map key for a (kind, target) pair.
// NUL byte (\x00) is used as a separator because it is not valid in Kind or
// target strings, making the key unambiguous without any reverse-parse.
func registryKey(kind Kind, target string) string {
	return string(kind) + "\x00" + target
}

// Get returns the *Breaker for the given (kind, target) pair, creating it
// lazily on the first call.  Get is concurrency-safe.
func (r *Registry) Get(kind Kind, target string) *Breaker {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := registryKey(kind, target)
	if e, ok := r.entries[k]; ok {
		return e.b
	}
	// Evict the oldest-created breaker when at the bound. A breaker that is
	// still in use and gets re-created restarts closed — acceptable at this
	// bound size, and strictly better than unbounded growth.
	if r.maxEntries > 0 && len(r.entries) >= r.maxEntries {
		for len(r.order) > 0 {
			oldest := r.order[0]
			r.order = r.order[1:]
			if _, stillThere := r.entries[oldest]; stillThere {
				delete(r.entries, oldest)
				break
			}
		}
	}
	r.entries[k] = registryEntry{kind: kind, target: target, b: New(r.cfg), createdAt: time.Now()}
	r.order = append(r.order, k)
	return r.entries[k].b
}

// Snapshot returns a point-in-time view of every breaker currently in the
// registry.  It returns an empty (non-nil) slice when no breakers have been
// created yet.  Snapshot is concurrency-safe.
//
// The registry lock is held only long enough to collect (kind, target, *Breaker)
// tuples; b.State() is called in a second pass outside r.mu to avoid a
// lock-ordering deadlock between r.mu and each Breaker's internal mutex.
func (r *Registry) Snapshot() []BreakerSnapshot {
	r.mu.Lock()
	entries := make([]registryEntry, 0, len(r.entries))
	for _, e := range r.entries {
		entries = append(entries, e)
	}
	r.mu.Unlock()

	snaps := make([]BreakerSnapshot, 0, len(entries))
	for _, e := range entries {
		snaps = append(snaps, BreakerSnapshot{
			Kind:   e.kind,
			Target: e.target,
			State:  e.b.State(),
		})
	}
	return snaps
}

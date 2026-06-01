package resilience

import "sync"

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

// Registry is a concurrency-safe, lazy-initialising store of *Breaker instances
// keyed by (kind, target).
type Registry struct {
	mu       sync.Mutex
	cfg      Config
	breakers map[string]*Breaker // key: kind+":"+target
}

// NewRegistry creates a Registry that uses cfg for every breaker it creates.
func NewRegistry(cfg Config) *Registry {
	return &Registry{
		cfg:      cfg,
		breakers: make(map[string]*Breaker),
	}
}

// registryKey returns the canonical map key for a (kind, target) pair.
func registryKey(kind Kind, target string) string {
	return string(kind) + ":" + target
}

// Get returns the *Breaker for the given (kind, target) pair, creating it
// lazily on the first call.  Get is concurrency-safe.
func (r *Registry) Get(kind Kind, target string) *Breaker {
	key := registryKey(kind, target)

	r.mu.Lock()
	defer r.mu.Unlock()

	if b, ok := r.breakers[key]; ok {
		return b
	}
	b := New(r.cfg)
	r.breakers[key] = b
	return b
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
	entries := make([]struct {
		kind   Kind
		target string
		b      *Breaker
	}, 0, len(r.breakers))
	for k, b := range r.breakers {
		kind, target := splitKey(k)
		entries = append(entries, struct {
			kind   Kind
			target string
			b      *Breaker
		}{kind, target, b})
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

// splitKey separates the first colon-delimited segment (kind) from the
// remainder (target).  This is the inverse of registryKey.
func splitKey(key string) (Kind, string) {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return Kind(key[:i]), key[i+1:]
		}
	}
	// Malformed key (no colon); treat the whole string as kind, empty target.
	return Kind(key), ""
}

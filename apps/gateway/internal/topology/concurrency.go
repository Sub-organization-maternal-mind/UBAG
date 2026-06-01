package topology

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// defaultConcurrencyCap is used when no AIMD cap has been reported for a
	// (target, identityRef) pair. It acts as a permissive bootstrap ceiling.
	defaultConcurrencyCap = 100
)

// ConcurrencyView is a read-only projection of the adaptive (AIMD) concurrency
// ceiling the worker enforces for a given provider/target + identity pair. The
// worker owns the live AIMD controller; the gateway is the control plane and
// only observes the most recently reported ceiling.
//
// Sourcing: the gateway never computes AIMD state. A ConcurrencyRegistry is
// updated via Report when the worker reports a cap change (the worker-event
// ingestion path calls Report when an AIMD cap-change event arrives). Until the
// worker reports, the registry is empty / seeded from configured defaults. No
// HTTP write endpoint mutates the registry — reads only, to avoid bypassing
// auth.
type ConcurrencyView struct {
	Target           string    `json:"target"`
	IdentityRef      string    `json:"identity_ref"`
	CurrentCap       int       `json:"current_cap"`
	Min              int       `json:"min"`
	Max              int       `json:"max"`
	InFlight         int       `json:"in_flight"`
	LastChangeReason string    `json:"last_change_reason,omitempty"`
	LastChangeAt     time.Time `json:"last_change_at"`
}

// ConcurrencyRegistry is an in-memory, tenant-scoped registry of the latest
// AIMD ceilings reported by workers. It is safe for concurrent use and every
// method is nil-safe.
//
// Beyond observability, the registry acts as a lightweight gateway-side
// in-flight token pool: Acquire increments the in-flight count and returns
// false if the ceiling would be exceeded; Release decrements it.
type ConcurrencyRegistry struct {
	mu       sync.RWMutex
	byTenant map[string]map[string]ConcurrencyView
	// inFlight is the gateway-side in-flight counter, separate from the
	// AIMD view which reflects worker-reported counts.
	inFlight map[string]map[string]int // [tenantID][key]count
}

// NewConcurrencyRegistry returns an empty registry.
func NewConcurrencyRegistry() *ConcurrencyRegistry {
	return &ConcurrencyRegistry{
		byTenant: map[string]map[string]ConcurrencyView{},
		inFlight: map[string]map[string]int{},
	}
}

func concurrencyKey(target, identityRef string) string {
	return target + "\x1f" + identityRef
}

// Report records (or replaces) the latest ceiling for a tenant's
// target+identity pair. It is the entry point a future worker-event ingestion
// path uses to push AIMD cap changes into the gateway's read view.
func (r *ConcurrencyRegistry) Report(tenantID string, view ConcurrencyView) {
	if r == nil {
		return
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return
	}
	if view.LastChangeAt.IsZero() {
		view.LastChangeAt = time.Now().UTC()
	} else {
		view.LastChangeAt = view.LastChangeAt.UTC()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byTenant == nil {
		r.byTenant = map[string]map[string]ConcurrencyView{}
	}
	tenant := r.byTenant[tenantID]
	if tenant == nil {
		tenant = map[string]ConcurrencyView{}
		r.byTenant[tenantID] = tenant
	}
	tenant[concurrencyKey(view.Target, view.IdentityRef)] = view
}

// List returns the ceilings reported for a tenant, ordered by target then
// identity. It returns an empty slice (never nil) for unknown tenants.
func (r *ConcurrencyRegistry) List(tenantID string) []ConcurrencyView {
	out := make([]ConcurrencyView, 0)
	if r == nil {
		return out
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, view := range r.byTenant[tenantID] {
		out = append(out, view)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		return out[i].IdentityRef < out[j].IdentityRef
	})
	return out
}

// Acquire attempts to reserve a concurrency token for a (tenant, target,
// identityRef) tuple. It returns true and increments the in-flight count if
// the current in-flight count is below the ceiling. It returns false if the
// ceiling would be exceeded — the caller should return UBAG-CONCURRENCY-001.
//
// When no AIMD ceiling has been reported for the pair, defaultConcurrencyCap
// is used so the gateway remains permissive during bootstrap.
func (r *ConcurrencyRegistry) Acquire(tenantID, target, identityRef string) bool {
	if r == nil {
		return true
	}
	tenantID = strings.TrimSpace(tenantID)
	target = strings.TrimSpace(target)
	identityRef = strings.TrimSpace(identityRef)

	r.mu.Lock()
	defer r.mu.Unlock()

	cap := defaultConcurrencyCap
	if tenant, ok := r.byTenant[tenantID]; ok {
		if view, ok := tenant[concurrencyKey(target, identityRef)]; ok && view.CurrentCap > 0 {
			cap = view.CurrentCap
		}
	}

	if r.inFlight == nil {
		r.inFlight = map[string]map[string]int{}
	}
	if r.inFlight[tenantID] == nil {
		r.inFlight[tenantID] = map[string]int{}
	}
	key := concurrencyKey(target, identityRef)
	if r.inFlight[tenantID][key] >= cap {
		return false
	}
	r.inFlight[tenantID][key]++
	return true
}

// Release decrements the in-flight count for a (tenant, target, identityRef)
// tuple. It is a no-op on nil registries or for unknown pairs.
func (r *ConcurrencyRegistry) Release(tenantID, target, identityRef string) {
	if r == nil {
		return
	}
	tenantID = strings.TrimSpace(tenantID)
	target = strings.TrimSpace(target)
	identityRef = strings.TrimSpace(identityRef)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.inFlight == nil {
		return
	}
	if r.inFlight[tenantID] == nil {
		return
	}
	key := concurrencyKey(target, identityRef)
	if r.inFlight[tenantID][key] > 0 {
		r.inFlight[tenantID][key]--
	}
}

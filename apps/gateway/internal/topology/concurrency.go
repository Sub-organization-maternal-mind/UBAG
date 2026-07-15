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
	// held maps a job ID to the lane whose in-flight token it holds. It is the
	// source of truth for per-job, exactly-once, balanced release: MarkAcquired
	// records the association once a created job's token is live, and
	// ReleaseForJob returns exactly that token at most once. Jobs that never
	// acquired a token (e.g. gRPC- or workflow-created jobs) are absent, so
	// releasing them is a safe no-op — this is what keeps the shared lane count
	// balanced and immune to double-release across cancel / worker / reaper.
	held map[string]heldToken // [jobID]lane
}

// heldToken is the lane a job's in-flight token belongs to.
type heldToken struct {
	tenantID    string
	target      string
	identityRef string
}

// NewConcurrencyRegistry returns an empty registry.
func NewConcurrencyRegistry() *ConcurrencyRegistry {
	return &ConcurrencyRegistry{
		byTenant: map[string]map[string]ConcurrencyView{},
		inFlight: map[string]map[string]int{},
		held:     map[string]heldToken{},
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
//
// Release is lane-scoped and NOT idempotent per job: use it only to undo a token
// the caller just acquired but could not yet associate with a job ID (e.g. job
// creation failed after Acquire). For every terminal transition of a *created*
// job, use ReleaseForJob so release is exactly-once and balanced.
func (r *ConcurrencyRegistry) Release(tenantID, target, identityRef string) {
	if r == nil {
		return
	}
	tenantID = strings.TrimSpace(tenantID)
	target = strings.TrimSpace(target)
	identityRef = strings.TrimSpace(identityRef)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.decrementLocked(tenantID, concurrencyKey(target, identityRef))
}

// decrementLocked lowers the in-flight count for a lane, clamped at zero. The
// caller must hold r.mu.
func (r *ConcurrencyRegistry) decrementLocked(tenantID, key string) {
	if r.inFlight == nil || r.inFlight[tenantID] == nil {
		return
	}
	if r.inFlight[tenantID][key] > 0 {
		r.inFlight[tenantID][key]--
	}
}

// MarkAcquired associates an already-acquired token (from a prior Acquire that
// returned true) with a job ID, once the job exists. Call it immediately after
// the job is created; from then on the token is released via ReleaseForJob. It
// is nil-safe and idempotent.
func (r *ConcurrencyRegistry) MarkAcquired(jobID, tenantID, target, identityRef string) {
	if r == nil {
		return
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.held == nil {
		r.held = map[string]heldToken{}
	}
	r.held[jobID] = heldToken{
		tenantID:    strings.TrimSpace(tenantID),
		target:      strings.TrimSpace(target),
		identityRef: strings.TrimSpace(identityRef),
	}
}

// ReleaseForJob returns the in-flight token held by jobID to its lane exactly
// once. It is nil-safe and idempotent: a job that never acquired a token (e.g.
// gRPC/workflow-created jobs) or whose token was already released is a no-op.
// This makes release safe to call from every terminal path — worker
// completion/failure, the cancel API, and the stale-job reaper — without
// double-counting the shared lane.
func (r *ConcurrencyRegistry) ReleaseForJob(jobID string) {
	if r == nil {
		return
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	token, ok := r.held[jobID]
	if !ok {
		return
	}
	delete(r.held, jobID)
	r.decrementLocked(token.tenantID, concurrencyKey(token.target, token.identityRef))
}

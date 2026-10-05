package topology

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// defaultConcurrencyCap is used when no AIMD cap has been reported for a
	// (target, identityRef) pair. It acts as a permissive bootstrap ceiling.
	// A lane's tokens span QUEUED and running jobs, so this ceiling is also the
	// deepest queue one (tenant, target, app) lane can hold: it must sit above
	// the queue depth the deployment is expected to absorb (the 1,000-queued-job
	// acceptance target). Override with UBAG_ADMISSION_DEFAULT_LANE_CAP.
	defaultConcurrencyCap = 2000
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

	// backend, when set, makes admission shared: tokens live in a database so
	// every gateway replica admits against one authority (see TokenBackend).
	// The process-local inFlight/held maps are then unused.
	// backend and limits are set once, by UseBackend, before the registry
	// serves traffic (they are read without r.mu afterwards).
	backend TokenBackend
	limits  LaneLimits
	pmu     sync.Mutex // guards pending
	// defaultCap overrides defaultConcurrencyCap when > 0 (SetDefaultLaneCap).
	defaultCap int
	// pending holds this process's unassociated token ids per lane, FIFO.
	// Tokens on one lane are interchangeable, so pairing the oldest pending
	// token with the next created job is safe.
	pending map[string][]string
}

// LaneLimits are the optional shared budgets layered over the per-lane
// ceiling (0 disables one): per (tenant, app), per tenant, and global
// in-flight jobs.
type LaneLimits struct {
	App    int
	Tenant int
	Global int
}

const (
	// unassociatedTokenTTL bounds how long an acquired-but-unassociated token
	// survives: a gateway that dies between Acquire and job creation cannot
	// leak capacity longer than this.
	unassociatedTokenTTL = 2 * time.Minute
	backendTimeout       = 5 * time.Second
)

// UseBackend switches the registry to shared, database-backed admission.
func (r *ConcurrencyRegistry) UseBackend(backend TokenBackend, limits LaneLimits) {
	if r == nil {
		return
	}
	r.backend, r.limits = backend, limits
	r.pending = map[string][]string{}
}

// SetDefaultLaneCap sets the ceiling used for lanes with no worker-reported
// cap. Call it before the registry serves traffic.
func (r *ConcurrencyRegistry) SetDefaultLaneCap(n int) {
	if r != nil && n > 0 {
		r.defaultCap = n
	}
}

func laneKeyFor(tenantID, target, identityRef string) string {
	return "lane:" + tenantID + "|" + concurrencyKey(target, identityRef)
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
	if r.backend != nil && view.CurrentCap > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), backendTimeout)
		defer cancel()
		if err := r.backend.PutLaneCap(ctx, laneKeyFor(tenantID, view.Target, view.IdentityRef), view.CurrentCap, time.Now().UTC()); err != nil {
			slog.Warn("admission: persisting lane cap failed", "error", err)
		}
	}
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

	if r.backend != nil {
		return r.acquireShared(tenantID, target, identityRef)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	cap := r.laneCapLocked(tenantID, target, identityRef)

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

// laneCapLocked is the worker-reported ceiling for the lane, or the
// permissive bootstrap default. Callers hold r.mu (read or write).
func (r *ConcurrencyRegistry) laneCapLocked(tenantID, target, identityRef string) int {
	if tenant, ok := r.byTenant[tenantID]; ok {
		if view, ok := tenant[concurrencyKey(target, identityRef)]; ok && view.CurrentCap > 0 {
			return view.CurrentCap
		}
	}
	if r.defaultCap > 0 {
		return r.defaultCap
	}
	return defaultConcurrencyCap
}

// acquireShared admits against the shared backend: the lane ceiling plus any
// configured app / tenant / global budgets, all-or-nothing. A backend failure
// denies admission (fail closed) rather than guessing. The backend path never
// holds r.mu across database IO, so concurrent admissions are not serialized
// behind one process-wide lock.
func (r *ConcurrencyRegistry) acquireShared(tenantID, target, identityRef string) bool {
	r.mu.RLock()
	cap := r.laneCapLocked(tenantID, target, identityRef)
	r.mu.RUnlock()
	lanes := []Lane{{Key: laneKeyFor(tenantID, target, identityRef), Cap: cap, Dynamic: true}}
	if r.limits.App > 0 {
		lanes = append(lanes, Lane{Key: "app:" + tenantID + "|" + identityRef, Cap: r.limits.App})
	}
	if r.limits.Tenant > 0 {
		lanes = append(lanes, Lane{Key: "tenant:" + tenantID, Cap: r.limits.Tenant})
	}
	if r.limits.Global > 0 {
		lanes = append(lanes, Lane{Key: "global:all", Cap: r.limits.Global})
	}
	ctx, cancel := context.WithTimeout(context.Background(), backendTimeout)
	defer cancel()
	id, ok, err := r.backend.AcquireToken(ctx, lanes, unassociatedTokenTTL, time.Now().UTC())
	if err != nil {
		slog.Error("admission: shared token acquire failed", "error", err)
		return false
	}
	if !ok {
		return false
	}
	key := tenantID + "|" + concurrencyKey(target, identityRef)
	r.pmu.Lock()
	r.pending[key] = append(r.pending[key], id)
	r.pmu.Unlock()
	return true
}

// popPending returns this process's oldest unassociated token for the lane,
// if any.
func (r *ConcurrencyRegistry) popPending(tenantID, target, identityRef string) (string, bool) {
	r.pmu.Lock()
	defer r.pmu.Unlock()
	key := tenantID + "|" + concurrencyKey(target, identityRef)
	queue := r.pending[key]
	if len(queue) == 0 {
		return "", false
	}
	id := queue[0]
	if len(queue) == 1 {
		delete(r.pending, key)
	} else {
		r.pending[key] = queue[1:]
	}
	return id, true
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

	if r.backend != nil {
		if id, ok := r.popPending(tenantID, target, identityRef); ok {
			ctx, cancel := context.WithTimeout(context.Background(), backendTimeout)
			defer cancel()
			if err := r.backend.ReleaseToken(ctx, id); err != nil {
				slog.Warn("admission: releasing token failed (it will expire)", "error", err)
			}
		}
		return
	}
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
	if r.backend != nil {
		if id, ok := r.popPending(strings.TrimSpace(tenantID), strings.TrimSpace(target), strings.TrimSpace(identityRef)); ok {
			ctx, cancel := context.WithTimeout(context.Background(), backendTimeout)
			defer cancel()
			if err := r.backend.AssociateToken(ctx, id, jobID); err != nil {
				slog.Warn("admission: associating token with job failed (it will expire)", "job_id", jobID, "error", err)
			}
		}
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
	if r.backend != nil {
		// The database is authoritative, so a terminal path running on ANY
		// replica releases the token regardless of which one admitted it.
		ctx, cancel := context.WithTimeout(context.Background(), backendTimeout)
		defer cancel()
		if err := r.backend.ReleaseJobToken(ctx, jobID); err != nil {
			slog.Error("admission: releasing job token failed", "job_id", jobID, "error", err)
		}
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

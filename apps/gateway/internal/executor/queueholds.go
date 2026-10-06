package executor

import (
	"errors"
	"regexp"
	"sync"
	"time"
)

// Coarse queue reasons (job-response.schema.json queue_reason), the tenant-safe
// face of the fine reasons the consumer holds a lease for (decision D8).
const (
	QueueWaitingForWorker       = "waiting_for_worker"
	QueueWaitingForIdentity     = "waiting_for_identity"
	QueueWaitingForCapacity     = "waiting_for_capacity"
	QueueWaitingForNode         = "waiting_for_node"
	QueueTemporarilyUnavailable = "temporarily_unavailable"
)

// CoarseQueueReason maps a fine hold reason (the Reason token of a PoolOverloadError,
// LaneBusyError or HelperRetryError) to the coarse value a tenant may see. The fine
// set is open: anything unlisted is temporarily_unavailable, the honest answer for a
// hold the gateway cannot describe without leaking placement detail.
//
// ponytail: retry_backoff is part of the closed coarse set but nothing is held under
// it today (a retryable failure is a new attempt, not a lease hold); map it here
// when the retry delay becomes observable.
func CoarseQueueReason(fine string) string {
	switch fine {
	case "local_slots_busy", "queue_full", "wait_expired", "overloaded":
		return QueueWaitingForWorker
	case "identity_busy", "voice_session_active", "voice_admission_in_flight":
		return QueueWaitingForIdentity
	case "no_capacity":
		return QueueWaitingForCapacity
	case "no_eligible_node", "affinity_unavailable":
		return QueueWaitingForNode
	case "retry_backoff":
		return "retry_backoff"
	}
	return QueueTemporarilyUnavailable
}

const (
	// maxHolds bounds the board. The consumer holds at most AsyncHolds (64) leases
	// off the worker plus one per worker, so this never fills in practice; past it
	// a new hold is simply not shown (the job reads as waiting for a worker).
	maxHolds = 1024
	// holdTTL is how long an entry outlives its last refresh. A held lease is
	// retried every <= 30 s and noted again each time, so an entry older than this
	// belongs to a consumer that stopped (shutdown, lost lease).
	holdTTL = 2 * time.Minute
	// maxHoldReasons bounds the distinct fine reasons counted (the OpenAPI
	// held_by_reason limit); the rest fold into other_hold.
	maxHoldReasons = 64
)

var holdReasonRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// HoldInfo is one queued job the consumer is holding.
type HoldInfo struct {
	TenantID string
	AppID    string
	// Fine is the fine-grained reason (operator detail, never shown to a tenant).
	Fine string
	// Since is when the current reason began; it restarts only when the reason changes.
	Since time.Time
	// Assigned marks a hold noted after the job was set to assigned (a worker
	// pool refusal). Tenants see only queued jobs by reason, so the tenant
	// summary skips these; the operator fleet summary counts them.
	Assigned bool
	seen     time.Time
}

// HoldBoard is the bounded in-memory view of the leased jobs this gateway is
// holding back before they start (decision D8: the queue reason is computed at
// read time from live state, nothing is stored or migrated). The consumer notes a
// hold each time it refuses a placement and clears it when the job is assigned;
// the API reads it. A nil board is a no-op, which is today's gateway.
//
// ponytail: in-process, so it sees only the holds of this gateway's own consumer
// (one primary per fleet, same as the Placer). A second replica would need the
// holds in the shared store.
type HoldBoard struct {
	mu  sync.Mutex
	m   map[string]HoldInfo
	now func() time.Time
}

// NewHoldBoard returns an empty board.
func NewHoldBoard() *HoldBoard { return &HoldBoard{m: map[string]HoldInfo{}, now: time.Now} }

// Note records that jobID is held for the fine reason. A reason that is not a short
// identifier token is recorded as other_hold.
func (b *HoldBoard) Note(jobID, tenantID, appID, fine string) {
	b.note(jobID, tenantID, appID, fine, false)
}

// NoteAssigned is Note for a job already set to assigned whose worker slot was
// refused (pool saturation): operators see the hold, the job stays assigned.
func (b *HoldBoard) NoteAssigned(jobID, tenantID, appID, fine string) {
	b.note(jobID, tenantID, appID, fine, true)
}

func (b *HoldBoard) note(jobID, tenantID, appID, fine string, assigned bool) {
	if b == nil || jobID == "" {
		return
	}
	if !holdReasonRe.MatchString(fine) {
		fine = "other_hold"
	}
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	cur, ok := b.m[jobID]
	if !ok && len(b.m) >= maxHolds {
		b.pruneLocked(now)
		if len(b.m) >= maxHolds {
			return
		}
	}
	since := now
	if ok && cur.Fine == fine && now.Sub(cur.seen) < holdTTL {
		since = cur.Since
	}
	b.m[jobID] = HoldInfo{TenantID: tenantID, AppID: appID, Fine: fine, Since: since, Assigned: assigned, seen: now}
}

// Clear forgets a job (it was assigned, or is no longer held).
func (b *HoldBoard) Clear(jobID string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	delete(b.m, jobID)
	b.mu.Unlock()
}

// Lookup returns the live hold for jobID.
func (b *HoldBoard) Lookup(jobID string) (HoldInfo, bool) {
	if b == nil {
		return HoldInfo{}, false
	}
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	h, ok := b.m[jobID]
	if !ok || now.Sub(h.seen) >= holdTTL {
		return HoldInfo{}, false
	}
	return h, true
}

// Counts returns the live holds per fine reason of jobs that still read as queued. An empty tenantID counts every
// tenant (the operator view); otherwise only that tenant's holds, and only that
// app's when appID is set.
func (b *HoldBoard) Counts(tenantID, appID string) map[string]int {
	return b.counts(tenantID, appID, false)
}

// AllCounts is the operator view: every tenant, including the holds on jobs
// already assigned (Counts skips those, since they no longer read as queued).
func (b *HoldBoard) AllCounts() map[string]int {
	return b.counts("", "", true)
}

func (b *HoldBoard) counts(tenantID, appID string, withAssigned bool) map[string]int {
	out := map[string]int{}
	if b == nil {
		return out
	}
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range b.m {
		if now.Sub(h.seen) >= holdTTL || (tenantID != "" && h.TenantID != tenantID) || (appID != "" && h.AppID != appID) || (h.Assigned && !withAssigned) {
			continue
		}
		key := h.Fine
		if _, seen := out[key]; !seen && len(out) >= maxHoldReasons {
			key = "other_hold"
		}
		out[key]++
	}
	return out
}

func (b *HoldBoard) pruneLocked(now time.Time) {
	for id, h := range b.m {
		if now.Sub(h.seen) >= holdTTL {
			delete(b.m, id)
		}
	}
}

// holdReason is the fine reason of a refusal, the same token holdPlan logs.
func holdReason(cause error) string {
	var overload *PoolOverloadError
	var busy *LaneBusyError
	var helperHold *HelperRetryError
	switch {
	case errors.As(cause, &overload):
		return overload.Reason
	case errors.As(cause, &busy):
		return busy.Reason
	case errors.As(cause, &helperHold):
		return helperHold.Reason
	}
	return "overloaded"
}

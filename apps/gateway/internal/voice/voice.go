// Package voice implements voice-session admission for provider live voice.
//
// A voice session is a long-lived, interactive, bidirectional-audio appointment
// on one provider account through one browser/audio environment. Unlike jobs,
// sessions hold EXCLUSIVE resources for their whole lifetime:
//
//   - one provider-account lease (identity_ref): at most one live session per
//     (tenant, target, identity_ref);
//   - one browser/audio environment lease (instance_ref): at most one live
//     session per browser instance, because the virtual microphone and the
//     speaker monitor are instance-wide devices.
//
// Both exclusivities are enforced by shared, atomic reservations — partial
// UNIQUE indexes in SQLite/Postgres, mutex-serialized checks in memory — so
// multiple gateway replicas can admit against the same store without a
// process-local authority deciding. A request that cannot claim its leases is
// QUEUED (no leases held) and is promoted by Claim when the resources free up.
// Every lease carries an expiry; the sweeper terminates sessions whose lease
// lapsed (crash recovery) so a dead replica cannot pin an account forever.
package voice

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is a voice session's lifecycle state.
type Status string

const (
	// StatusQueued: the session exists but holds NO leases; it waits for an
	// account + browser environment to free up.
	StatusQueued Status = "queued"
	// StatusConnecting: leases are held; the media and control channels are
	// being established and the provider voice UI is being activated.
	StatusConnecting Status = "connecting"
	// StatusConnected: bidirectional audio is live.
	StatusConnected Status = "connected"
	// StatusTerminated: terminal — explicit termination, disconnect, lease
	// expiry, or error. Leases are released.
	StatusTerminated Status = "terminated"
)

// Active reports whether the status holds leases.
func (s Status) Active() bool {
	return s == StatusQueued || s == StatusConnecting || s == StatusConnected
}

// Session mode: "live" is a two-way voice session holding exclusive leases;
// "utterance" is the separately selectable audio-in/text-out mode backed by a
// transcription-style job. A live session is NEVER substituted with an
// utterance job — the mode is chosen explicitly by the caller and stored on
// the record.
const (
	ModeLive      = "live"
	ModeUtterance = "utterance"
)

// Session is one voice-session record. IdentityRef and InstanceRef are empty
// while queued (no leases held); JobID is set only for utterance-mode
// sessions, whose lifecycle is the backing transcription job's.
type Session struct {
	ID           string    `json:"session_id"`
	TenantID     string    `json:"tenant_id"`
	AppID        string    `json:"app_id"`
	Target       string    `json:"target"`
	Mode         string    `json:"mode"`
	JobID        string    `json:"job_id,omitempty"`
	Status       Status    `json:"status"`
	Muted        bool      `json:"muted"`
	IdentityRef  string    `json:"identity_ref,omitempty"`
	InstanceRef  string    `json:"instance_ref,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	LeaseExpires time.Time `json:"lease_expires_at,omitempty"`
	TerminatedAt time.Time `json:"terminated_at,omitempty"`

	// Internal lease state (P5.8). json:"-" keeps it out of every response: a
	// Helper Node id, the lease generation and the teardown hold are never
	// client-visible (TestSessionJSONCarriesNoInternalLeaseState).
	//
	// NodeID and LeaseGeneration identify the Helper Node that currently hosts
	// the session's media (empty and 0 for primary-hosted sessions); the
	// generation grows on every BindNode, so a previous holder's writes fail.
	// MediaLeaseExpires is the node's own lease, renewed by that node alone.
	// TerminatingUntil is set while a terminated session still holds its
	// account and environment so provider teardown can finish (BeginTerminate).
	NodeID            string    `json:"-"`
	LeaseGeneration   uint64    `json:"-"`
	MediaLeaseExpires time.Time `json:"-"`
	TerminatingUntil  time.Time `json:"-"`
}

// holdsLease reports whether the status owns the account and environment leases.
func (s Status) holdsLease() bool { return s == StatusConnecting || s == StatusConnected }

// TerminatingHoldMax bounds the terminating hold (the provider-deactivation
// timeout): a terminated session never pins its account or environment longer.
const TerminatingHoldMax = 45 * time.Second

// LeaseFence names the holder of a node-bound media lease: the Helper Node and
// the generation BindNode handed it. The zero fence names a primary-hosted
// session (no node, generation 0).
type LeaseFence struct {
	NodeID     string
	Generation uint64
}

func (f LeaseFence) valid() bool {
	return validNodeID(f.NodeID) && f.Generation > 0 && f.Generation <= maxBoundGeneration
}

func validNodeID(id string) bool {
	return id != "" && len(id) <= 128 && !strings.Contains(id, "|")
}

// checkFence reports why f is not the fence of s (nil when it is).
func (s Session) checkFence(f LeaseFence) error {
	switch {
	case s.NodeID != f.NodeID:
		return ErrNodeMismatch
	case s.LeaseGeneration != f.Generation:
		return ErrStaleGeneration
	}
	return nil
}

// fenceMiss explains why a fenced write matched no row.
func fenceMiss(s Session, found bool, f LeaseFence) error {
	if !found {
		return ErrNotFound
	}
	if err := s.checkFence(f); err != nil {
		return err
	}
	return ErrConflict
}

// holdMiss explains why ReleaseHold matched no row: a live session's leases are
// never released here (ErrConflict); an already released or never held
// termination is an idempotent ack (nil).
func holdMiss(s Session, found bool, f LeaseFence) error {
	if !found {
		return ErrNotFound
	}
	if err := s.checkFence(f); err != nil {
		return err
	}
	if s.Status != StatusTerminated {
		return ErrConflict
	}
	return nil
}

// visible is the externally reported record: a terminated session reports no
// leases even while its termination hold keeps them reserved.
func visible(s Session) Session {
	if s.Status == StatusTerminated {
		s.IdentityRef, s.InstanceRef = "", ""
	}
	return s
}

// ReserveRequest is one admission attempt.
type ReserveRequest struct {
	SessionID string
	TenantID  string
	AppID     string
	Target    string
	// Mode selects live (default) or utterance. Utterance sessions never
	// claim leases and carry JobID.
	Mode  string
	JobID string
	// Placements are the server-resolved (provider account, browser
	// environment) pairs this session may hold, most preferred first. The
	// caller derives them from topology (an authenticated context and the
	// instance that actually hosts it) — never from client input. Each pair
	// is attempted atomically; the first free pair wins. Empty means the
	// session queues until a pair is named via Claim.
	Placements []Placement
	// MaxActive caps lease-holding live sessions for the tenant and MaxQueued
	// caps its queued live sessions (0 = unlimited). Both are enforced inside
	// the admission transaction, so concurrent creates and replicas cannot
	// overshoot them.
	MaxActive int
	MaxQueued int
	LeaseTTL  time.Duration
	Now       time.Time
}

// Placement is one provider-account + browser-environment pair.
type Placement struct {
	Identity string
	Instance string
}

// ClaimRequest promotes one queued session.
type ClaimRequest struct {
	TenantID, SessionID string
	Placements          []Placement
	MaxActive           int
	LeaseTTL            time.Duration
	Now                 time.Time
}

func (p Placement) clean() (Placement, bool) {
	p.Identity, p.Instance = strings.TrimSpace(p.Identity), strings.TrimSpace(p.Instance)
	return p, p.Identity != "" && p.Instance != ""
}

// ErrConflict is returned/checked when a lease claim lost a race. Callers
// retry with the next candidate; Reserve hides this internally.
var ErrConflict = errors.New("voice: lease conflict")

// ErrNotFound is returned when a session ID does not exist for the scope.
var ErrNotFound = errors.New("voice: session not found")

// ErrQueueFull is returned by Reserve when the tenant's queued-session budget
// is spent.
var ErrQueueFull = errors.New("voice: queue budget reached")

// Store is the shared voice-session store. All methods are tenant-scoped by
// the Session's TenantID; callers must pass the trusted gateway tenant.
type Store interface {
	Ready(ctx context.Context) error

	// Reserve atomically admits a session: it tries the identity and instance
	// candidates in order and, on the first successful combination, persists
	// the session as StatusConnecting with leases held. When no candidate
	// combination is free it persists StatusQueued (no leases). The returned
	// Session is the persisted record.
	Reserve(ctx context.Context, req ReserveRequest) (Session, error)

	// Claim atomically promotes one QUEUED session to StatusConnecting by
	// claiming the first free (identity, instance) combination from the
	// candidates, exactly like Reserve's claim step. It fails with
	// ErrConflict when the session is no longer queued (the caller re-reads
	// to see the truth).
	Claim(ctx context.Context, req ClaimRequest) (Session, error)

	// Get returns one session (tenant-scoped).
	Get(ctx context.Context, tenantID, sessionID string) (Session, bool, error)

	// List returns the tenant's sessions (optionally filtered), newest first.
	List(ctx context.Context, tenantID string, target string, limit int) ([]Session, error)

	// Transition CAS-advances a session's status. It fails with ErrConflict
	// when the session is not in from (the caller re-reads to see the truth).
	Transition(ctx context.Context, tenantID, sessionID string, from, to Status, now time.Time, lastError string) error

	// SetMuted updates the mute flag on a non-terminated session.
	SetMuted(ctx context.Context, tenantID, sessionID string, muted bool, now time.Time) error

	// Terminate terminates a session regardless of state (idempotent) and
	// releases its leases.
	Terminate(ctx context.Context, tenantID, sessionID string, now time.Time, reason string) error

	// BeginTerminate terminates like Terminate (the session reads as
	// terminated at once) but keeps the account and environment leases held
	// until ReleaseHold acks provider teardown or hold (clamped to
	// TerminatingHoldMax) passes, so no successor can claim them while the
	// provider voice session is still being deactivated. A queued or already
	// terminated session holds nothing; hold <= 0 is Terminate. Idempotent.
	BeginTerminate(ctx context.Context, tenantID, sessionID string, now time.Time, reason string, hold time.Duration) error

	// ReleaseHold acks provider teardown: it frees the leases a BeginTerminate
	// kept. The fence must be the session's (the zero fence for primary-hosted
	// sessions), so a replaced node cannot release the hold. Idempotent; a live
	// session is ErrConflict.
	ReleaseHold(ctx context.Context, tenantID, sessionID string, fence LeaseFence, now time.Time) error

	// BindNode hands a live session's media lease to a Helper Node: it records
	// the node, grows the lease generation (the previous holder's fence stops
	// matching) and opens a media lease of mediaTTL. It returns the updated
	// session so the caller learns the generation. ErrConflict when the session
	// is not live or its lease lapsed.
	BindNode(ctx context.Context, tenantID, sessionID, nodeID string, mediaTTL time.Duration, now time.Time) (Session, error)

	// RenewMediaLease extends the node-bound media lease. It fails with
	// ErrNodeMismatch / ErrStaleGeneration for a stale holder and with
	// ErrConflict once the media lease lapsed (only the sweeper ends it).
	RenewMediaLease(ctx context.Context, tenantID, sessionID string, fence LeaseFence, until time.Time, now time.Time) error

	// CommitTransition is Transition fenced by the node-bound lease: a stale
	// holder's commit is rejected before it can change the session.
	CommitTransition(ctx context.Context, tenantID, sessionID string, fence LeaseFence, from, to Status, now time.Time, lastError string) error

	// RenewLease extends a live session's lease expiry.
	RenewLease(ctx context.Context, tenantID, sessionID string, until time.Time, now time.Time) error

	// SweepExpired terminates every session (any tenant) whose lease lapsed
	// (or whose node-bound media lease lapsed) and whose status is active,
	// marking lastError. Returns the swept IDs. It also frees terminating holds
	// that passed their deadline (not reported). It must be safe to run from any
	// replica (idempotent, CAS-guarded).
	SweepExpired(ctx context.Context, now time.Time) ([]string, error)

	// TenantCounts returns the tenant's live-mode (leased, queued) session
	// counts; utterance sessions hold no resources and are not counted.
	TenantCounts(ctx context.Context, tenantID string) (leased, queued int, err error)

	// GlobalSessionCounts returns (active, queued) across ALL tenants for
	// the unauthenticated metrics endpoint (which must never split by
	// tenant). Active counts lease-holding sessions only; queued counts
	// StatusQueued rows.
	GlobalSessionCounts(ctx context.Context) (active int, queued int, err error)

	// ListLeaseHolders lists, across ALL tenants, the live-mode sessions that
	// reserve a browser environment at now: live ones and terminated ones still
	// inside their terminating hold. Unlike Get it keeps InstanceRef for a held
	// termination. It is the truth the job consumer checks before it drives a
	// browser (see LaneProbe).
	ListLeaseHolders(ctx context.Context, now time.Time) ([]LeaseHolder, error)
}

// LeaseHolder is the part of a session that tells which browser environment it
// still reserves.
type LeaseHolder struct {
	SessionID   string
	TenantID    string
	Target      string
	InstanceRef string
}

// SessionID generates a session identifier with the vault-prefixed, sortable
// shape used across the gateway (voice_<timestamp>_<entropy>).

// NewMemoryStore builds a process-local Store. All admission checks run under
// one mutex, which is the single-replica equivalent of the partial unique
// indexes the SQL stores enforce.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

// MemoryStore is a mutex-guarded in-memory Store. Sessions vanish on restart
// by design: memory stores are for tests and single-process development.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func (m *MemoryStore) Ready(context.Context) error { return nil }

func (m *MemoryStore) Reserve(ctx context.Context, req ReserveRequest) (Session, error) {
	if req.SessionID == "" || req.TenantID == "" || req.Target == "" {
		return Session{}, errors.New("voice: session_id, tenant_id and target are required")
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions == nil {
		m.sessions = map[string]*Session{}
	}
	if _, exists := m.sessions[req.SessionID]; exists {
		return Session{}, ErrConflict
	}
	mode := req.Mode
	if strings.TrimSpace(mode) == "" {
		mode = ModeLive
	}
	identity, instance := "", ""
	if mode == ModeLive {
		leased, queued := m.counts(req.TenantID)
		if req.MaxActive <= 0 || leased < req.MaxActive {
			active := m.activeIndex(now)
			for _, p := range req.Placements {
				p, ok := p.clean()
				if !ok || !placementFree(active, req.TenantID, req.Target, p) {
					continue
				}
				identity, instance = p.Identity, p.Instance
				break
			}
		}
		if identity == "" && req.MaxQueued > 0 && queued >= req.MaxQueued {
			return Session{}, ErrQueueFull
		}
	}
	status := StatusQueued
	if identity != "" {
		status = StatusConnecting
	}
	session := &Session{
		ID:           req.SessionID,
		TenantID:     req.TenantID,
		AppID:        req.AppID,
		Target:       req.Target,
		Mode:         mode,
		JobID:        req.JobID,
		Status:       status,
		IdentityRef:  identity,
		InstanceRef:  instance,
		CreatedAt:    now,
		UpdatedAt:    now,
		LeaseExpires: now.Add(req.LeaseTTL),
	}
	m.sessions[req.SessionID] = session
	return *session, nil
}

func (m *MemoryStore) Claim(ctx context.Context, req ClaimRequest) (Session, error) {
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[req.SessionID]
	if !ok || s.TenantID != req.TenantID {
		return Session{}, ErrNotFound
	}
	if s.Status != StatusQueued {
		return Session{}, ErrConflict
	}
	if leased, _ := m.counts(req.TenantID); req.MaxActive > 0 && leased >= req.MaxActive {
		return Session{}, ErrConflict
	}
	active := m.activeIndex(now)
	for _, p := range req.Placements {
		p, ok := p.clean()
		if !ok || !placementFree(active, req.TenantID, s.Target, p) {
			continue
		}
		s.Status = StatusConnecting
		s.IdentityRef = p.Identity
		s.InstanceRef = p.Instance
		s.LeaseExpires = now.Add(req.LeaseTTL)
		s.UpdatedAt = now
		return *s, nil
	}
	return Session{}, ErrConflict
}

func placementFree(active map[string]struct{}, tenantID, target string, p Placement) bool {
	_, accountTaken := active[leaseKey(tenantID, target, p.Identity)]
	_, instanceTaken := active[instanceKey(p.Instance)]
	return !accountTaken && !instanceTaken
}

// counts reports the tenant's live-mode leased and queued sessions. Callers
// hold m.mu.
func (m *MemoryStore) counts(tenantID string) (leased, queued int) {
	for _, s := range m.sessions {
		if s.TenantID != tenantID || s.Mode == ModeUtterance {
			continue
		}
		switch s.Status {
		case StatusConnecting, StatusConnected:
			leased++
		case StatusQueued:
			queued++
		}
	}
	return leased, queued
}

// leaseKey / instanceKey are also the SQL stores' unique-index keys.
func leaseKey(tenantID, target, identity string) string {
	return tenantID + "\x00" + target + "\x00" + identity
}

// instanceKey is deliberately NOT tenant-scoped: the virtual microphone and
// speaker monitor are properties of the physical browser environment, so two
// tenants can never hold the same one at once.
func instanceKey(instance string) string {
	return "instance\x00" + instance
}

// activeIndex lists the leases that are taken at now: live sessions' and
// terminated sessions' unexpired termination holds.
func (m *MemoryStore) activeIndex(now time.Time) map[string]struct{} {
	active := map[string]struct{}{}
	for _, s := range m.sessions {
		if s.Status == StatusTerminated && !s.TerminatingUntil.After(now) {
			continue
		}
		if s.IdentityRef != "" {
			active[leaseKey(s.TenantID, s.Target, s.IdentityRef)] = struct{}{}
		}
		if s.InstanceRef != "" {
			active[instanceKey(s.InstanceRef)] = struct{}{}
		}
	}
	return active
}

func (m *MemoryStore) Get(_ context.Context, tenantID, sessionID string) (Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.TenantID != tenantID {
		return Session{}, false, nil
	}
	return visible(*s), true, nil
}

func (m *MemoryStore) List(_ context.Context, tenantID, target string, limit int) ([]Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.TenantID != tenantID {
			continue
		}
		if target != "" && s.Target != target {
			continue
		}
		out = append(out, visible(*s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemoryStore) Transition(_ context.Context, tenantID, sessionID string, from, to Status, now time.Time, lastError string) error {
	return m.transition(tenantID, sessionID, nil, from, to, now, lastError)
}

func (m *MemoryStore) CommitTransition(_ context.Context, tenantID, sessionID string, fence LeaseFence, from, to Status, now time.Time, lastError string) error {
	if !fence.valid() {
		return ErrBadBinding
	}
	return m.transition(tenantID, sessionID, &fence, from, to, now, lastError)
}

func (m *MemoryStore) transition(tenantID, sessionID string, fence *LeaseFence, from, to Status, now time.Time, lastError string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.TenantID != tenantID {
		return ErrNotFound
	}
	if fence != nil {
		if err := s.checkFence(*fence); err != nil {
			return err
		}
	}
	if s.Status != from {
		return ErrConflict
	}
	s.Status = to
	s.UpdatedAt = now
	s.LastError = lastError
	if to == StatusTerminated {
		s.TerminatedAt = now
		s.IdentityRef = ""
		s.InstanceRef = ""
	}
	return nil
}

func (m *MemoryStore) SetMuted(_ context.Context, tenantID, sessionID string, muted bool, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.TenantID != tenantID {
		return ErrNotFound
	}
	if s.Status == StatusTerminated {
		return ErrConflict
	}
	s.Muted = muted
	s.UpdatedAt = now
	return nil
}

func (m *MemoryStore) Terminate(_ context.Context, tenantID, sessionID string, now time.Time, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.TenantID != tenantID {
		return ErrNotFound
	}
	if s.Status == StatusTerminated {
		return nil // idempotent
	}
	s.Status = StatusTerminated
	s.TerminatedAt = now
	s.UpdatedAt = now
	s.LastError = reason
	s.IdentityRef = ""
	s.InstanceRef = ""
	return nil
}

func (m *MemoryStore) BeginTerminate(ctx context.Context, tenantID, sessionID string, now time.Time, reason string, hold time.Duration) error {
	if hold <= 0 {
		return m.Terminate(ctx, tenantID, sessionID, now, reason)
	}
	hold = min(hold, TerminatingHoldMax)
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.TenantID != tenantID {
		return ErrNotFound
	}
	if s.Status == StatusTerminated {
		return nil // idempotent; an existing hold is kept
	}
	held := s.Status.holdsLease()
	s.Status = StatusTerminated
	s.TerminatedAt = now
	s.UpdatedAt = now
	s.LastError = reason
	if held {
		s.TerminatingUntil = now.Add(hold) // the leases stay reserved until then
	} else {
		s.IdentityRef = ""
		s.InstanceRef = ""
	}
	return nil
}

func (m *MemoryStore) ReleaseHold(_ context.Context, tenantID, sessionID string, fence LeaseFence, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.TenantID != tenantID {
		return ErrNotFound
	}
	if s.Status != StatusTerminated || s.checkFence(fence) != nil {
		return holdMiss(*s, true, fence)
	}
	s.IdentityRef, s.InstanceRef, s.TerminatingUntil = "", "", time.Time{}
	s.UpdatedAt = now
	return nil
}

func (m *MemoryStore) BindNode(_ context.Context, tenantID, sessionID, nodeID string, mediaTTL time.Duration, now time.Time) (Session, error) {
	if !validNodeID(nodeID) || mediaTTL <= 0 {
		return Session{}, ErrBadBinding
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.TenantID != tenantID {
		return Session{}, ErrNotFound
	}
	if !s.Status.holdsLease() || !s.LeaseExpires.After(now) {
		return Session{}, ErrConflict
	}
	if s.LeaseGeneration >= maxBoundGeneration {
		return Session{}, ErrBadBinding
	}
	s.NodeID = nodeID
	s.LeaseGeneration++
	s.MediaLeaseExpires = now.Add(mediaTTL)
	s.UpdatedAt = now
	return *s, nil
}

func (m *MemoryStore) RenewMediaLease(_ context.Context, tenantID, sessionID string, fence LeaseFence, until time.Time, now time.Time) error {
	if !fence.valid() {
		return ErrBadBinding
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.TenantID != tenantID {
		return ErrNotFound
	}
	if err := s.checkFence(fence); err != nil {
		return err
	}
	if !s.Status.holdsLease() || !s.MediaLeaseExpires.After(now) {
		return ErrConflict
	}
	s.MediaLeaseExpires = until
	s.UpdatedAt = now
	return nil
}

func (m *MemoryStore) RenewLease(_ context.Context, tenantID, sessionID string, until time.Time, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.TenantID != tenantID {
		return ErrNotFound
	}
	if s.Status == StatusTerminated || s.Status == StatusQueued || !s.LeaseExpires.After(now) {
		return ErrConflict
	}
	s.LeaseExpires = until
	s.UpdatedAt = now
	return nil
}

func (m *MemoryStore) SweepExpired(_ context.Context, now time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var swept []string
	for _, s := range m.sessions {
		if s.Status == StatusTerminated {
			if !s.TerminatingUntil.IsZero() && !s.TerminatingUntil.After(now) {
				s.IdentityRef, s.InstanceRef, s.TerminatingUntil = "", "", time.Time{} // hold elapsed
			}
			continue
		}
		if s.Status == StatusQueued {
			continue // queued sessions hold no lease and cannot expire
		}
		reason := "lease_expired"
		if !s.LeaseExpires.IsZero() && s.LeaseExpires.After(now) {
			if s.NodeID == "" || s.MediaLeaseExpires.After(now) {
				continue
			}
			reason = "media_lease_expired" // the hosting node stopped renewing
		}
		s.Status = StatusTerminated
		s.TerminatedAt = now
		s.UpdatedAt = now
		s.LastError = reason
		s.IdentityRef = ""
		s.InstanceRef = ""
		swept = append(swept, s.ID)
	}
	return swept, nil
}

func (m *MemoryStore) ListLeaseHolders(_ context.Context, now time.Time) ([]LeaseHolder, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []LeaseHolder
	for _, s := range m.sessions {
		if s.Mode == ModeUtterance || s.InstanceRef == "" {
			continue
		}
		if s.Status == StatusTerminated && !s.TerminatingUntil.After(now) {
			continue // the hold elapsed; the refs just have not been cleaned yet
		}
		out = append(out, LeaseHolder{SessionID: s.ID, TenantID: s.TenantID, Target: s.Target, InstanceRef: s.InstanceRef})
	}
	return out, nil
}

func (m *MemoryStore) GlobalSessionCounts(_ context.Context) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	active, queued := 0, 0
	for _, s := range m.sessions {
		switch s.Status {
		case StatusConnecting, StatusConnected:
			active++
		case StatusQueued:
			queued++
		}
	}
	return active, queued, nil
}

func (m *MemoryStore) TenantCounts(_ context.Context, tenantID string) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	leased, queued := m.counts(tenantID)
	return leased, queued, nil
}

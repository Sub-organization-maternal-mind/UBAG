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
	// IdentityCandidates are the provider accounts to try claiming, most
	// preferred first (the caller derives them from topology). Each is
	// attempted atomically; the first claim wins. Empty means the caller has
	// no account preference and the session queues until one is named via
	// Claim.
	IdentityCandidates []string
	// InstanceCandidates are the browser/audio environments to try claiming.
	InstanceCandidates []string
	LeaseTTL           time.Duration
	Now                time.Time
}

// ErrConflict is returned/checked when a lease claim lost a race. Callers
// retry with the next candidate; Reserve hides this internally.
var ErrConflict = errors.New("voice: lease conflict")

// ErrNotFound is returned when a session ID does not exist for the scope.
var ErrNotFound = errors.New("voice: session not found")

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
	Claim(ctx context.Context, tenantID, sessionID string, identityCandidates, instanceCandidates []string, leaseTTL time.Duration, now time.Time) (Session, error)

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

	// RenewLease extends a live session's lease expiry.
	RenewLease(ctx context.Context, tenantID, sessionID string, until time.Time, now time.Time) error

	// SweepExpired terminates every session (any tenant) whose lease lapsed
	// and whose status is active, marking lastError. Returns the swept IDs.
	// It must be safe to run from any replica (idempotent, CAS-guarded).
	SweepExpired(ctx context.Context, now time.Time) ([]string, error)

	// ActiveCount counts active (lease-holding or queued) sessions for the
	// tenant, optionally per target (empty target = all).
	ActiveCount(ctx context.Context, tenantID, target string) (int, error)

	// GlobalSessionCounts returns (active, queued) across ALL tenants for
	// the unauthenticated metrics endpoint (which must never split by
	// tenant). Active counts lease-holding sessions only; queued counts
	// StatusQueued rows.
	GlobalSessionCounts(ctx context.Context) (active int, queued int, err error)
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
	active := m.activeIndex()
	identity, instance := "", ""
	for _, candID := range req.IdentityCandidates {
		candID = strings.TrimSpace(candID)
		if candID == "" {
			continue
		}
		if _, taken := active[leaseKey(req.TenantID, req.Target, candID)]; taken {
			continue
		}
		identity = candID
		break
	}
	if identity != "" {
		for _, candInst := range req.InstanceCandidates {
			candInst = strings.TrimSpace(candInst)
			if candInst == "" {
				continue
			}
			if _, taken := active[instanceKey(req.TenantID, candInst)]; taken {
				continue
			}
			instance = candInst
			break
		}
		if instance == "" {
			identity = "" // cannot hold a half reservation: account without its environment
		}
	}
	status := StatusQueued
	if identity != "" {
		status = StatusConnecting
	}
	mode := req.Mode
	if strings.TrimSpace(mode) == "" {
		mode = ModeLive
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

func (m *MemoryStore) Claim(ctx context.Context, tenantID, sessionID string, identityCandidates, instanceCandidates []string, leaseTTL time.Duration, now time.Time) (Session, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.TenantID != tenantID {
		return Session{}, ErrNotFound
	}
	if s.Status != StatusQueued {
		return Session{}, ErrConflict
	}
	active := m.activeIndex()
	for _, candID := range identityCandidates {
		candID = strings.TrimSpace(candID)
		if candID == "" {
			continue
		}
		if _, taken := active[leaseKey(tenantID, s.Target, candID)]; taken {
			continue
		}
		for _, candInst := range instanceCandidates {
			candInst = strings.TrimSpace(candInst)
			if candInst == "" {
				continue
			}
			if _, taken := active[instanceKey(tenantID, candInst)]; taken {
				continue
			}
			s.Status = StatusConnecting
			s.IdentityRef = candID
			s.InstanceRef = candInst
			s.LeaseExpires = now.Add(leaseTTL)
			s.UpdatedAt = now
			return *s, nil
		}
	}
	return Session{}, ErrConflict
}

// leaseKey / instanceKey are also the SQL stores' unique-index keys.
func leaseKey(tenantID, target, identity string) string {
	return tenantID + "\x00" + target + "\x00" + identity
}

func instanceKey(tenantID, instance string) string {
	return tenantID + "\x00" + instance
}

func (m *MemoryStore) activeIndex() map[string]struct{} {
	active := map[string]struct{}{}
	for _, s := range m.sessions {
		if s.Status == StatusTerminated {
			continue
		}
		if s.IdentityRef != "" {
			active[leaseKey(s.TenantID, s.Target, s.IdentityRef)] = struct{}{}
		}
		if s.InstanceRef != "" {
			active[instanceKey(s.TenantID, s.InstanceRef)] = struct{}{}
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
	return *s, true, nil
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
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemoryStore) Transition(_ context.Context, tenantID, sessionID string, from, to Status, now time.Time, lastError string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.TenantID != tenantID {
		return ErrNotFound
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

func (m *MemoryStore) RenewLease(_ context.Context, tenantID, sessionID string, until time.Time, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok || s.TenantID != tenantID {
		return ErrNotFound
	}
	if s.Status == StatusTerminated {
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
			continue
		}
		if s.Status == StatusQueued {
			continue // queued sessions hold no lease and cannot expire
		}
		if !s.LeaseExpires.IsZero() && s.LeaseExpires.After(now) {
			continue
		}
		s.Status = StatusTerminated
		s.TerminatedAt = now
		s.UpdatedAt = now
		s.LastError = "lease_expired"
		s.IdentityRef = ""
		s.InstanceRef = ""
		swept = append(swept, s.ID)
	}
	return swept, nil
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

func (m *MemoryStore) ActiveCount(_ context.Context, tenantID, target string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, s := range m.sessions {
		if s.TenantID != tenantID || s.Status == StatusTerminated {
			continue
		}
		if target != "" && s.Target != target {
			continue
		}
		count++
	}
	return count, nil
}

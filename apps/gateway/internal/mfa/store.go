package mfa

import (
	"context"
	"fmt"
	"sync"
	"time"

	gocrypto "github.com/ubag/ubag/apps/gateway/internal/crypto"
)

// Enrollment stores an MFA enrollment for one user in one tenant.
type Enrollment struct {
	TenantID      string
	UserID        string
	Secret        string    // plaintext TOTP secret (base32); production would AES-GCM wrap
	RecoveryHashes []string // argon2id hash of each recovery code
	UsedCounters  map[uint64]struct{} // counters already consumed (replay protection)
	CreatedAt     time.Time
}

// Store persists and retrieves MFA enrollments.
type Store interface {
	// Enroll creates or replaces enrollment for (tenantID, userID).
	Enroll(ctx context.Context, e Enrollment) error
	// Get returns the enrollment for (tenantID, userID). ok=false if none.
	Get(ctx context.Context, tenantID, userID string) (Enrollment, bool, error)
	// MarkCounterUsed atomically checks and records that a TOTP counter step has
	// been used so the same code cannot be replayed within the same 30-second
	// window. Returns (true, nil) when the counter was newly marked (first use)
	// and (false, nil) when it was already consumed (replay attack).
	MarkCounterUsed(ctx context.Context, tenantID, userID string, counter uint64) (bool, error)
	// ConsumeRecovery checks if code matches any unused recovery hash and marks
	// it used. Returns true if a match was found.
	ConsumeRecovery(ctx context.Context, tenantID, userID, code string) (bool, error)
}

// MemoryStore is an in-memory MFA store suitable for tests and single-node
// deployments where durability is not required.
type MemoryStore struct {
	mu   sync.Mutex
	data map[string]*Enrollment // key = tenantID + "\x00" + userID
}

// NewMemoryStore creates a new, empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{data: make(map[string]*Enrollment)}
}

func enrollmentKey(tenantID, userID string) string {
	return tenantID + "\x00" + userID
}

// Enroll creates or replaces the enrollment for the given user.
func (m *MemoryStore) Enroll(_ context.Context, e Enrollment) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := e
	copy.UsedCounters = make(map[uint64]struct{})
	copy.RecoveryHashes = append([]string(nil), e.RecoveryHashes...)
	m.data[enrollmentKey(e.TenantID, e.UserID)] = &copy
	return nil
}

// Get returns the enrollment for the given (tenantID, userID) pair.
func (m *MemoryStore) Get(_ context.Context, tenantID, userID string) (Enrollment, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.data[enrollmentKey(tenantID, userID)]
	if !ok {
		return Enrollment{}, false, nil
	}
	// Return a shallow copy so callers cannot mutate the internal state.
	out := *e
	out.RecoveryHashes = append([]string(nil), e.RecoveryHashes...)
	out.UsedCounters = nil // don't expose internal map; MarkCounterUsed handles this
	return out, true, nil
}

// MarkCounterUsed atomically checks and records a TOTP counter (replay
// protection). Returns (true, nil) on first use, (false, nil) if already used.
func (m *MemoryStore) MarkCounterUsed(_ context.Context, tenantID, userID string, counter uint64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.data[enrollmentKey(tenantID, userID)]
	if !ok {
		return false, fmt.Errorf("mfa: enrollment not found for %s/%s", tenantID, userID)
	}
	if e.UsedCounters == nil {
		e.UsedCounters = make(map[uint64]struct{})
	}
	if _, exists := e.UsedCounters[counter]; exists {
		return false, nil // already used — replay attack
	}
	e.UsedCounters[counter] = struct{}{}
	return true, nil // newly marked
}

// ConsumeRecovery checks whether code matches any remaining (unused) recovery
// hash and, if so, removes that hash from the list. Returns true on match.
func (m *MemoryStore) ConsumeRecovery(_ context.Context, tenantID, userID, code string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.data[enrollmentKey(tenantID, userID)]
	if !ok {
		return false, nil
	}
	for i, hash := range e.RecoveryHashes {
		if gocrypto.VerifyPassword(hash, code) {
			// Remove the consumed hash so it cannot be used again.
			e.RecoveryHashes = append(e.RecoveryHashes[:i], e.RecoveryHashes[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

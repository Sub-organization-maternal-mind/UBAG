package jitadmin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Store persists JIT elevation grants.
type Store interface {
	// Create persists a new grant and returns it with a generated ID.
	Create(ctx context.Context, g Grant) (Grant, error)
	// Get returns the grant by ID.
	Get(ctx context.Context, id string) (Grant, error)
	// Approve sets Approved=true, ApprovedBy, and ApprovedAt.
	Approve(ctx context.Context, id, approverSubject string, now time.Time) (Grant, error)
	// ActiveGrants returns all non-revoked, approved, unexpired grants for actor+tenantID.
	ActiveGrants(ctx context.Context, actor, tenantID string, now time.Time) ([]Grant, error)
	// Revoke marks a grant as revoked.
	Revoke(ctx context.Context, id string) error
}

// grantID generates a cryptographically random grant ID prefixed with "jit_"
// followed by 24 hex chars (12 random bytes). Using crypto/rand ensures two
// concurrent requests cannot collide on ID even within the same nanosecond.
func grantID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic("jitadmin: entropy failure")
	}
	return "jit_" + hex.EncodeToString(b)
}

// MemoryStore is the in-memory JIT grant store. It is safe for concurrent use.
type MemoryStore struct {
	mu     sync.Mutex
	grants map[string]Grant
}

// NewMemoryStore returns a new, empty in-memory grant store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{grants: make(map[string]Grant)}
}

// Create persists a new grant. It assigns an ID, sets CreatedAt and ExpiresAt
// if not already set, and stores the grant.
func (m *MemoryStore) Create(_ context.Context, g Grant) (Grant, error) {
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now()
	}
	if g.ExpiresAt.IsZero() {
		g.ExpiresAt = g.CreatedAt.Add(g.TTL)
	}
	g.ID = grantID()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.grants[g.ID] = g
	return g, nil
}

// Get returns the grant with the given ID or ErrGrantNotFound.
func (m *MemoryStore) Get(_ context.Context, id string) (Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.grants[id]
	if !ok {
		return Grant{}, ErrGrantNotFound
	}
	return g, nil
}

// Approve marks the grant as approved by the given approver at the given time.
// Returns ErrGrantNotFound if the ID is unknown, an error if the grant is
// already revoked, or ErrGrantExpired if the grant's TTL has passed.
func (m *MemoryStore) Approve(_ context.Context, id, approverSubject string, now time.Time) (Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.grants[id]
	if !ok {
		return Grant{}, ErrGrantNotFound
	}
	if g.Revoked {
		return Grant{}, errors.New("jitadmin: cannot approve a revoked grant")
	}
	if !now.Before(g.ExpiresAt) {
		return Grant{}, ErrGrantExpired
	}
	g.Approved = true
	g.ApprovedBy = approverSubject
	t := now
	g.ApprovedAt = &t
	m.grants[id] = g
	return g, nil
}

// ActiveGrants returns all approved, unexpired, non-revoked grants for the
// given actor and tenantID at the given point in time.
func (m *MemoryStore) ActiveGrants(_ context.Context, actor, tenantID string, now time.Time) ([]Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Grant
	for _, g := range m.grants {
		if g.Actor == actor && g.TenantID == tenantID && g.IsActive(now) {
			out = append(out, g)
		}
	}
	return out, nil
}

// Revoke marks the grant as revoked. Returns ErrGrantNotFound if the ID is unknown.
func (m *MemoryStore) Revoke(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.grants[id]
	if !ok {
		return ErrGrantNotFound
	}
	g.Revoked = true
	m.grants[id] = g
	return nil
}

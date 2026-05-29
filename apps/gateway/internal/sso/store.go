package sso

import (
	"context"
	"sync"
	"time"
)

// StoredOIDC is an OIDC config persisted under a tenant scope.
type StoredOIDC struct {
	TenantID  string
	Config    OIDCConfig
	UpdatedAt time.Time
}

// StoredSAML is a SAML config persisted under a tenant scope.
type StoredSAML struct {
	TenantID  string
	Config    SAMLConfig
	UpdatedAt time.Time
}

// ConfigStore persists OIDC and SAML SSO configuration scoped by tenant id. It
// only ever stores public / non-secret configuration. For OIDC the client
// secret is represented by OIDCConfig.ClientSecretRef (a reference id), never
// the plaintext secret.
type ConfigStore interface {
	Ready(ctx context.Context) error

	GetOIDC(ctx context.Context, tenantID string) (OIDCConfig, bool, error)
	SetOIDC(ctx context.Context, tenantID string, cfg OIDCConfig) error
	ListOIDC(ctx context.Context) ([]StoredOIDC, error)

	GetSAML(ctx context.Context, tenantID string) (SAMLConfig, bool, error)
	SetSAML(ctx context.Context, tenantID string, cfg SAMLConfig) error
	ListSAML(ctx context.Context) ([]StoredSAML, error)
}

// MemoryStore is an in-memory ConfigStore, primarily for tests and single-node
// development. It is safe for concurrent use.
type MemoryStore struct {
	mu   sync.RWMutex
	now  func() time.Time
	oidc map[string]StoredOIDC
	saml map[string]StoredSAML
}

// NewMemoryStore returns an empty in-memory ConfigStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		now:  time.Now,
		oidc: map[string]StoredOIDC{},
		saml: map[string]StoredSAML{},
	}
}

func (m *MemoryStore) Ready(context.Context) error { return nil }

func (m *MemoryStore) GetOIDC(_ context.Context, tenantID string) (OIDCConfig, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	record, ok := m.oidc[tenantID]
	if !ok {
		return OIDCConfig{}, false, nil
	}
	return record.Config, true, nil
}

func (m *MemoryStore) SetOIDC(_ context.Context, tenantID string, cfg OIDCConfig) error {
	if err := validateTenant(tenantID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.oidc[tenantID] = StoredOIDC{TenantID: tenantID, Config: cfg, UpdatedAt: m.now().UTC()}
	return nil
}

func (m *MemoryStore) ListOIDC(_ context.Context) ([]StoredOIDC, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]StoredOIDC, 0, len(m.oidc))
	for _, record := range m.oidc {
		out = append(out, record)
	}
	return out, nil
}

func (m *MemoryStore) GetSAML(_ context.Context, tenantID string) (SAMLConfig, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	record, ok := m.saml[tenantID]
	if !ok {
		return SAMLConfig{}, false, nil
	}
	return record.Config, true, nil
}

func (m *MemoryStore) SetSAML(_ context.Context, tenantID string, cfg SAMLConfig) error {
	if err := validateTenant(tenantID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saml[tenantID] = StoredSAML{TenantID: tenantID, Config: cfg, UpdatedAt: m.now().UTC()}
	return nil
}

func (m *MemoryStore) ListSAML(_ context.Context) ([]StoredSAML, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]StoredSAML, 0, len(m.saml))
	for _, record := range m.saml {
		out = append(out, record)
	}
	return out, nil
}

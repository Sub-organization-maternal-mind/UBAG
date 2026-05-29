package workflow

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store implementation guarded by a mutex. It is
// used for tests and single-process deployments.
type MemoryStore struct {
	mu          sync.Mutex
	now         func() time.Time
	definitions map[string]Definition
	runs        map[string]Run
	defOrder    []string
	runOrder    []string
	runIdem     map[string]string
}

// NewMemoryStore constructs an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		now:         time.Now,
		definitions: map[string]Definition{},
		runs:        map[string]Run{},
		runIdem:     map[string]string{},
	}
}

func (m *MemoryStore) CreateDefinition(_ context.Context, def Definition) (Definition, error) {
	if err := validateDefinition(def); err != nil {
		return Definition{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if def.ID == "" {
		def.ID = newID("wfd")
	} else if existing, ok := m.definitions[def.ID]; ok {
		if existing.TenantID != def.TenantID || existing.AppID != def.AppID {
			return Definition{}, ErrScope
		}
		return cloneDefinition(existing), nil
	}
	if def.CreatedAt.IsZero() {
		def.CreatedAt = m.now().UTC()
	} else {
		def.CreatedAt = def.CreatedAt.UTC()
	}
	stored := cloneDefinition(def)
	m.definitions[stored.ID] = stored
	m.defOrder = append(m.defOrder, stored.ID)
	return cloneDefinition(stored), nil
}

func (m *MemoryStore) GetDefinition(_ context.Context, tenantID string, appID string, id string) (Definition, bool, error) {
	if err := validateScope(tenantID, appID); err != nil {
		return Definition{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	def, ok := m.definitions[id]
	if !ok || def.TenantID != tenantID || def.AppID != appID {
		return Definition{}, false, nil
	}
	return cloneDefinition(def), true, nil
}

func (m *MemoryStore) ListDefinitions(_ context.Context, tenantID string, appID string, limit int) ([]Definition, error) {
	if err := validateScope(tenantID, appID); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []Definition{}
	for _, id := range m.defOrder {
		def := m.definitions[id]
		if def.TenantID != tenantID || def.AppID != appID {
			continue
		}
		result = append(result, cloneDefinition(def))
	}
	sort.SliceStable(result, func(l, r int) bool {
		if result[l].CreatedAt.Equal(result[r].CreatedAt) {
			return result[l].ID < result[r].ID
		}
		return result[l].CreatedAt.Before(result[r].CreatedAt)
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (m *MemoryStore) CreateRun(_ context.Context, run Run) (Run, error) {
	if err := validateRun(run); err != nil {
		return Run{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if key := idemKey(run.TenantID, run.AppID, run.IdempotencyKey); key != "" {
		if id, ok := m.runIdem[key]; ok {
			return cloneRun(m.runs[id]), nil
		}
	}
	if run.ID == "" {
		run.ID = newID("wfr")
	} else if _, ok := m.runs[run.ID]; ok {
		return Run{}, ErrInvalidRun
	}
	now := m.now().UTC()
	if run.State == "" {
		run.State = StatePending
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	} else {
		run.CreatedAt = run.CreatedAt.UTC()
	}
	run.UpdatedAt = now
	stored := cloneRun(run)
	m.runs[stored.ID] = stored
	m.runOrder = append(m.runOrder, stored.ID)
	if key := idemKey(run.TenantID, run.AppID, run.IdempotencyKey); key != "" {
		m.runIdem[key] = stored.ID
	}
	return cloneRun(stored), nil
}

func (m *MemoryStore) GetRun(_ context.Context, tenantID string, appID string, id string) (Run, bool, error) {
	if err := validateScope(tenantID, appID); err != nil {
		return Run{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[id]
	if !ok || run.TenantID != tenantID || run.AppID != appID {
		return Run{}, false, nil
	}
	return cloneRun(run), true, nil
}

func (m *MemoryStore) ListRuns(_ context.Context, tenantID string, appID string, limit int) ([]Run, error) {
	if err := validateScope(tenantID, appID); err != nil {
		return nil, err
	}
	limit = normalizeLimit(limit)
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []Run{}
	for _, id := range m.runOrder {
		run := m.runs[id]
		if run.TenantID != tenantID || run.AppID != appID {
			continue
		}
		result = append(result, cloneRun(run))
	}
	sort.SliceStable(result, func(l, r int) bool {
		if result[l].CreatedAt.Equal(result[r].CreatedAt) {
			return result[l].ID < result[r].ID
		}
		return result[l].CreatedAt.Before(result[r].CreatedAt)
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (m *MemoryStore) UpdateRun(_ context.Context, run Run) error {
	if err := validateRun(run); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.runs[run.ID]
	if !ok {
		return ErrNotFound
	}
	if existing.TenantID != run.TenantID || existing.AppID != run.AppID {
		return ErrScope
	}
	run.CreatedAt = existing.CreatedAt
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = m.now().UTC()
	} else {
		run.UpdatedAt = run.UpdatedAt.UTC()
	}
	m.runs[run.ID] = cloneRun(run)
	return nil
}

func idemKey(tenantID string, appID string, key string) string {
	if key == "" {
		return ""
	}
	return tenantID + "\x00" + appID + "\x00" + key
}

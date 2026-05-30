// Package topology provides read-only observability over the v2.1 multi-tab
// browser engine: the Browser -> ProviderContext -> Tab hierarchy persisted in
// the gateway_browser_* tables, plus aggregate topology summaries.
//
// It is a control-plane / observability surface only: every operation is
// read-only and tenant-scoped (WHERE tenant_id = $1) so one tenant can never
// observe another's browser topology (INV-5). The worker owns the live engine
// and writes these tables; the gateway only reads them.
//
// Credential references are never surfaced: the provider-context storage state
// URI (which can point at a stored login/credential blob) is redacted to a
// boolean HasStorageState and the raw URI is intentionally absent from the
// exported ProviderContext type.
//
// Three backends mirror the gateway's other read stores: an in-memory store
// (default / tests), a SQLite store, and a Postgres store.
package topology

import (
	"context"
	"sort"
	"sync"
	"time"
)

// BrowserInstance is a single browser process owned by a worker.
type BrowserInstance struct {
	InstanceID     string     `json:"instance_id"`
	WorkerID       string     `json:"worker_id"`
	TenantID       string     `json:"tenant_id"`
	Engine         string     `json:"engine"`
	RemoteEndpoint string     `json:"remote_endpoint,omitempty"`
	State          string     `json:"state"`
	ContextCount   int        `json:"context_count"`
	TabCount       int        `json:"tab_count"`
	RSSBytes       *int64     `json:"rss_bytes,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	RecycleAt      *time.Time `json:"recycle_at,omitempty"`
}

// ProviderContext is an isolated browser context bound to a provider target and
// an identity. The storage-state URI is deliberately redacted to HasStorageState.
type ProviderContext struct {
	ContextID         string     `json:"context_id"`
	InstanceID        string     `json:"instance_id"`
	TenantID          string     `json:"tenant_id"`
	TargetID          string     `json:"target_id"`
	IdentityRef       string     `json:"identity_ref"`
	LoginState        string     `json:"login_state"`
	ConversationModel string     `json:"conversation_model"`
	FingerprintID     string     `json:"fingerprint_id,omitempty"`
	ProxyID           string     `json:"proxy_id,omitempty"`
	HasStorageState   bool       `json:"has_storage_state"`
	MaxTabs           int        `json:"max_tabs"`
	CreatedAt         time.Time  `json:"created_at"`
	LastHealthAt      *time.Time `json:"last_health_at,omitempty"`
	RecycleAt         *time.Time `json:"recycle_at,omitempty"`
}

// BrowserTab is a single channel tab within a provider context.
type BrowserTab struct {
	TabID          string     `json:"tab_id"`
	ContextID      string     `json:"context_id"`
	State          string     `json:"state"`
	ConversationID string     `json:"conversation_id,omitempty"`
	CurrentJobID   string     `json:"current_job_id,omitempty"`
	JobsCompleted  int        `json:"jobs_completed"`
	RSSBytes       *int64     `json:"rss_bytes,omitempty"`
	LastHealthAt   *time.Time `json:"last_health_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	RecycleAt      *time.Time `json:"recycle_at,omitempty"`
}

// InstanceFilter constrains a ListInstances query. TenantID is required for
// tenant isolation.
type InstanceFilter struct {
	TenantID string
	State    string
	Limit    int
}

// ContextFilter constrains a ListContexts query.
type ContextFilter struct {
	TenantID   string
	InstanceID string
	Limit      int
}

// TabFilter constrains a ListTabs query.
type TabFilter struct {
	TenantID  string
	ContextID string
	State     string
	Limit     int
}

// Summary is an aggregate topology snapshot for a single tenant.
type Summary struct {
	TenantID             string         `json:"tenant_id"`
	InstancesByState     map[string]int `json:"instances_by_state"`
	ContextsByLoginState map[string]int `json:"contexts_by_login_state"`
	TabsByState          map[string]int `json:"tabs_by_state"`
	TotalInstances       int            `json:"total_instances"`
	TotalContexts        int            `json:"total_contexts"`
	TotalTabs            int            `json:"total_tabs"`
}

// Store is the read-only, tenant-scoped topology store.
type Store interface {
	Ready(ctx context.Context) error
	ListInstances(ctx context.Context, filter InstanceFilter) ([]BrowserInstance, error)
	ListContexts(ctx context.Context, filter ContextFilter) ([]ProviderContext, error)
	ListTabs(ctx context.Context, filter TabFilter) ([]BrowserTab, error)
	Summary(ctx context.Context, tenantID string) (Summary, error)
}

func newSummary(tenantID string) Summary {
	return Summary{
		TenantID:             tenantID,
		InstancesByState:     map[string]int{},
		ContextsByLoginState: map[string]int{},
		TabsByState:          map[string]int{},
	}
}

// MemoryStore is an in-memory Store for embedded/default use and tests.
type MemoryStore struct {
	mu        sync.RWMutex
	instances []BrowserInstance
	contexts  []ProviderContext
	tabs      []memoryTab
}

type memoryTab struct {
	tab      BrowserTab
	tenantID string
}

// NewMemoryStore returns an empty in-memory topology store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (m *MemoryStore) Ready(context.Context) error { return nil }

// AddInstance appends a browser instance.
func (m *MemoryStore) AddInstance(instance BrowserInstance) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.instances = append(m.instances, instance)
}

// AddContext appends a provider context.
func (m *MemoryStore) AddContext(context ProviderContext) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.contexts = append(m.contexts, context)
}

// AddTab appends a tab. Its tenant is resolved from its parent context so tab
// queries stay tenant-scoped; tabs whose context is unknown are dropped.
func (m *MemoryStore) AddTab(tab BrowserTab) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tenantID := ""
	for _, ctx := range m.contexts {
		if ctx.ContextID == tab.ContextID {
			tenantID = ctx.TenantID
			break
		}
	}
	m.tabs = append(m.tabs, memoryTab{tab: tab, tenantID: tenantID})
}

func (m *MemoryStore) ListInstances(_ context.Context, filter InstanceFilter) ([]BrowserInstance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]BrowserInstance, 0)
	for _, instance := range m.instances {
		if instance.TenantID != filter.TenantID {
			continue
		}
		if filter.State != "" && instance.State != filter.State {
			continue
		}
		out = append(out, instance)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out, nil
}

func (m *MemoryStore) ListContexts(_ context.Context, filter ContextFilter) ([]ProviderContext, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ProviderContext, 0)
	for _, ctx := range m.contexts {
		if ctx.TenantID != filter.TenantID {
			continue
		}
		if filter.InstanceID != "" && ctx.InstanceID != filter.InstanceID {
			continue
		}
		out = append(out, ctx)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ContextID < out[j].ContextID })
	return out, nil
}

func (m *MemoryStore) ListTabs(_ context.Context, filter TabFilter) ([]BrowserTab, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]BrowserTab, 0)
	for _, row := range m.tabs {
		if row.tenantID != filter.TenantID {
			continue
		}
		if filter.ContextID != "" && row.tab.ContextID != filter.ContextID {
			continue
		}
		if filter.State != "" && row.tab.State != filter.State {
			continue
		}
		out = append(out, row.tab)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TabID < out[j].TabID })
	return out, nil
}

func (m *MemoryStore) Summary(_ context.Context, tenantID string) (Summary, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	summary := newSummary(tenantID)
	for _, instance := range m.instances {
		if instance.TenantID != tenantID {
			continue
		}
		summary.InstancesByState[instance.State]++
		summary.TotalInstances++
	}
	for _, ctx := range m.contexts {
		if ctx.TenantID != tenantID {
			continue
		}
		summary.ContextsByLoginState[ctx.LoginState]++
		summary.TotalContexts++
	}
	for _, row := range m.tabs {
		if row.tenantID != tenantID {
			continue
		}
		summary.TabsByState[row.tab.State]++
		summary.TotalTabs++
	}
	return summary, nil
}

// Package conversations implements the UBAG gateway's conversation-affinity
// subsystem. It durably binds a caller-owned conversation key, scoped to
// (tenant_id, app_id, target), to a provider chat thread so that jobs sharing a
// key resume the same provider chat and the end user keeps their context.
//
// ProviderThreadRef is a provider chat URL ONLY — never cookies, storage state,
// credentials, or noVNC URLs. Resuming a chat is a navigation inside an
// already user-authenticated session, which keeps the subsystem within the
// safe-mode product constraint (user-owned sessions only).
//
// Three store backends mirror the gateway's alerts/session store conventions:
// an in-memory store (default / tests), a SQLite store, and a Postgres store.
// The store is upsert-by-key: the engine retries an interaction up to three
// times, so a naive append would duplicate a binding.
package conversations

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// Conversation binding states.
const (
	StateActive = "active"
	StateBroken = "broken"
)

// Key identifies one conversation binding.
type Key struct {
	TenantID        string
	AppID           string
	Target          string
	ConversationKey string
}

// Conversation is a durable binding from a caller conversation key to a
// provider chat thread. ProviderThreadRef is a chat URL only — never cookies,
// storage state, or noVNC URLs.
type Conversation struct {
	TenantID          string    `json:"tenant_id"`
	AppID             string    `json:"app_id"`
	Target            string    `json:"target"`
	ConversationKey   string    `json:"conversation_key"`
	ProviderThreadRef string    `json:"provider_thread_ref,omitempty"`
	State             string    `json:"state"`
	CreatedAt         time.Time `json:"created_at"`
	LastUsedAt        time.Time `json:"last_used_at"`
	LastJobID         string    `json:"last_job_id,omitempty"`
}

// Filter constrains a List query.
type Filter struct {
	TenantID string
	AppID    string // optional; empty means any app
	Target   string // optional; empty means any target
	Limit    int    // 0 means no limit
}

// Store persists conversation bindings and exposes resolve/bind lifecycle.
type Store interface {
	Ready(ctx context.Context) error
	// Resolve returns the binding for key scoped to its tenant. A missing key
	// is never an error (found=false, err=nil).
	Resolve(ctx context.Context, key Key) (Conversation, bool, error)
	// Bind upserts by Key. Re-binding an existing key overwrites
	// ProviderThreadRef, sets State=active, and refreshes LastUsedAt.
	Bind(ctx context.Context, conv Conversation) (Conversation, error)
	// MarkBroken transitions the binding for key to State=broken, stamping
	// LastUsedAt from at. A missing key is never an error (found=false).
	MarkBroken(ctx context.Context, key Key, at time.Time) (Conversation, bool, error)
	// Touch records that key was used by jobID at at without changing the
	// binding's thread ref or state. A missing key is a no-op, never an error.
	Touch(ctx context.Context, key Key, jobID string, at time.Time) error
	// List returns bindings matching filter ordered by LastUsedAt descending
	// (most recently used first).
	List(ctx context.Context, filter Filter) ([]Conversation, error)
}

// ---------------------------------------------------------------------------
// Helpers shared across store backends.
// ---------------------------------------------------------------------------

// keyOf extracts the identity Key of a Conversation.
func keyOf(conv Conversation) Key {
	return Key{
		TenantID:        conv.TenantID,
		AppID:           conv.AppID,
		Target:          conv.Target,
		ConversationKey: conv.ConversationKey,
	}
}

// matchesKey reports whether conv is identified by key.
func matchesKey(conv Conversation, key Key) bool {
	return conv.TenantID == key.TenantID &&
		conv.AppID == key.AppID &&
		conv.Target == key.Target &&
		conv.ConversationKey == key.ConversationKey
}

// prepareBind normalises an incoming binding prior to an upsert. A Bind always
// records an active thread, so State is forced to StateActive.
func prepareBind(conv *Conversation) {
	conv.TenantID = strings.TrimSpace(conv.TenantID)
	conv.AppID = strings.TrimSpace(conv.AppID)
	conv.Target = strings.TrimSpace(conv.Target)
	conv.ConversationKey = strings.TrimSpace(conv.ConversationKey)
	conv.ProviderThreadRef = strings.TrimSpace(conv.ProviderThreadRef)
	conv.LastJobID = strings.TrimSpace(conv.LastJobID)
	conv.State = StateActive
	if conv.CreatedAt.IsZero() {
		conv.CreatedAt = time.Now()
	}
	conv.CreatedAt = conv.CreatedAt.UTC().Truncate(time.Microsecond)
	if conv.LastUsedAt.IsZero() {
		conv.LastUsedAt = conv.CreatedAt
	}
	conv.LastUsedAt = conv.LastUsedAt.UTC().Truncate(time.Microsecond)
}

// ---------------------------------------------------------------------------
// MemoryStore
// ---------------------------------------------------------------------------

// MemoryStore is an in-memory Store, primarily for development and tests.
type MemoryStore struct {
	mu       sync.Mutex
	byTenant map[string][]Conversation
}

// NewMemoryStore returns an empty in-memory conversation store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byTenant: make(map[string][]Conversation)}
}

func (m *MemoryStore) Ready(context.Context) error { return nil }

func (m *MemoryStore) Resolve(_ context.Context, key Key) (Conversation, bool, error) {
	if m == nil {
		return Conversation{}, false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.byTenant[key.TenantID] {
		if matchesKey(existing, key) {
			return existing, true, nil
		}
	}
	return Conversation{}, false, nil
}

func (m *MemoryStore) Bind(_ context.Context, conv Conversation) (Conversation, error) {
	if m == nil {
		return Conversation{}, fmt.Errorf("conversations: store is not configured")
	}
	prepareBind(&conv)
	m.mu.Lock()
	defer m.mu.Unlock()

	key := keyOf(conv)
	chain := m.byTenant[conv.TenantID]
	for i := range chain {
		if matchesKey(chain[i], key) {
			// Upsert: preserve the original creation time, overwrite the rest.
			conv.CreatedAt = chain[i].CreatedAt
			chain[i] = conv
			return conv, nil
		}
	}
	m.byTenant[conv.TenantID] = append(chain, conv)
	return conv, nil
}

func (m *MemoryStore) MarkBroken(_ context.Context, key Key, at time.Time) (Conversation, bool, error) {
	if m == nil {
		return Conversation{}, false, nil
	}
	at = at.UTC().Truncate(time.Microsecond)
	m.mu.Lock()
	defer m.mu.Unlock()
	chain := m.byTenant[key.TenantID]
	for i := range chain {
		if matchesKey(chain[i], key) {
			chain[i].State = StateBroken
			if !at.IsZero() {
				chain[i].LastUsedAt = at
			}
			return chain[i], true, nil
		}
	}
	return Conversation{}, false, nil
}

func (m *MemoryStore) Touch(_ context.Context, key Key, jobID string, at time.Time) error {
	if m == nil {
		return nil
	}
	jobID = strings.TrimSpace(jobID)
	at = at.UTC().Truncate(time.Microsecond)
	m.mu.Lock()
	defer m.mu.Unlock()
	chain := m.byTenant[key.TenantID]
	for i := range chain {
		if matchesKey(chain[i], key) {
			if jobID != "" {
				chain[i].LastJobID = jobID
			}
			if !at.IsZero() {
				chain[i].LastUsedAt = at
			}
			return nil
		}
	}
	// A missing key is a no-op, never an error.
	return nil
}

func (m *MemoryStore) List(_ context.Context, filter Filter) ([]Conversation, error) {
	if m == nil {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	chain := m.byTenant[filter.TenantID]
	out := make([]Conversation, 0, len(chain))
	for _, existing := range chain {
		if filter.AppID != "" && existing.AppID != filter.AppID {
			continue
		}
		if filter.Target != "" && existing.Target != filter.Target {
			continue
		}
		out = append(out, existing)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastUsedAt.After(out[j].LastUsedAt) })
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Manager
// ---------------------------------------------------------------------------

// Manager wraps a Store with a normalised logger and a store-kind label for the
// readiness/config surface. Its hot-path methods (Resolve, Bind, MarkBroken,
// Touch) are nil-safe so ingestion paths can call them unconditionally when the
// conversations feature is disabled (nil Manager).
type Manager struct {
	store     Store
	logger    *slog.Logger
	storeKind string
}

// NewManager constructs a Manager over store. A nil logger falls back to
// slog.Default. storeKind is a secret-free label (memory/sqlite/postgres) for
// the config summary.
func NewManager(store Store, logger *slog.Logger, storeKind string) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		store:     store,
		logger:    logger,
		storeKind: storeKind,
	}
}

// Ready verifies the underlying store is reachable.
func (m *Manager) Ready(ctx context.Context) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("conversations: manager is not configured")
	}
	return m.store.Ready(ctx)
}

// StoreKind returns the secret-free store-kind label for the config surface.
func (m *Manager) StoreKind() string {
	if m == nil {
		return ""
	}
	return m.storeKind
}

// Resolve returns the binding for key. It is nil-safe: a nil manager reports no
// binding without error so callers can resolve unconditionally.
func (m *Manager) Resolve(ctx context.Context, key Key) (Conversation, bool, error) {
	if m == nil || m.store == nil {
		return Conversation{}, false, nil
	}
	return m.store.Resolve(ctx, key)
}

// Bind upserts a binding by Key. A nil manager is a no-op returning a zero
// Conversation without error.
func (m *Manager) Bind(ctx context.Context, conv Conversation) (Conversation, error) {
	if m == nil || m.store == nil {
		return Conversation{}, nil
	}
	return m.store.Bind(ctx, conv)
}

// MarkBroken transitions a binding to broken. A nil manager reports no binding
// without error.
func (m *Manager) MarkBroken(ctx context.Context, key Key, at time.Time) (Conversation, bool, error) {
	if m == nil || m.store == nil {
		return Conversation{}, false, nil
	}
	return m.store.MarkBroken(ctx, key, at)
}

// Touch records that key was used by jobID at at. A nil manager is a no-op.
func (m *Manager) Touch(ctx context.Context, key Key, jobID string, at time.Time) error {
	if m == nil || m.store == nil {
		return nil
	}
	return m.store.Touch(ctx, key, jobID, at)
}

// List returns bindings matching filter. Unlike the hot-path methods, List is
// an API surface: a nil manager is a misconfiguration and returns an error.
func (m *Manager) List(ctx context.Context, filter Filter) ([]Conversation, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("conversations: manager is not configured")
	}
	return m.store.List(ctx, filter)
}

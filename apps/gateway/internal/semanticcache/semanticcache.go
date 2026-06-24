// Package semanticcache implements the §17 semantic response cache:
//
//   - L0 exact path: SHA-256 hash of (target, command_type, app, locale, input)
//   - L1 vector path: cosine similarity ≥ 0.97 (embedding stub; integrated when
//     pgvector is available)
//
// The two tiers share a single Store interface so callers are unaware of which
// tier served a hit.
package semanticcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// ErrNotFound is returned when no matching cache entry exists.
var ErrNotFound = errors.New("semanticcache: entry not found")

// CacheKey identifies a cache entry. All fields are case-sensitive.
type CacheKey struct {
	Target      string
	CommandType string
	AppID       string
	TenantID    string
	Locale      string
}

// Entry is a stored cache result.
type Entry struct {
	Key         string
	Output      any
	CacheSource string   // "exact" | "vector" | ""
	Tags        []string // invalidation tags
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// Store is the cache persistence interface.
type Store interface {
	// Get retrieves an entry for the given key and input. Returns (entry, true, nil)
	// on hit, (zero, false, nil) on miss, (zero, false, err) on error.
	Get(ctx context.Context, key CacheKey, input []byte) (Entry, bool, error)
	// Put stores an entry.
	Put(ctx context.Context, key CacheKey, input []byte, entry Entry) error
	// InvalidateByTag removes all entries tagged with the given tag.
	InvalidateByTag(ctx context.Context, tenantID, tag string) (int, error)
	// Purge removes all entries for a tenant.
	Purge(ctx context.Context, tenantID string) (int, error)
}

// buildExactKey returns the SHA-256 hex key for the (key, input) tuple.
func buildExactKey(key CacheKey, input []byte) string {
	h := sha256.New()
	writeField(h, key.TenantID)
	writeField(h, key.AppID)
	writeField(h, key.Target)
	writeField(h, key.CommandType)
	writeField(h, key.Locale)
	h.Write(input)
	return hex.EncodeToString(h.Sum(nil))
}

func writeField(h interface{ Write([]byte) (int, error) }, s string) {
	var prefix [8]byte
	n := uint64(len(s))
	for i := range prefix {
		prefix[i] = byte(n >> (8 * uint(i)))
	}
	h.Write(prefix[:])
	h.Write([]byte(s))
}

// ─────────────────────────────────────────────────────────────────────────────
// MemoryStore
// ─────────────────────────────────────────────────────────────────────────────

type storedEntry struct {
	entry    Entry
	exactKey string
	tenantID string
}

// MemoryStore is an in-memory semantic cache using exact SHA-256 matching.
// Vector similarity is not implemented here; a future pgvector backend will
// satisfy that tier.
type MemoryStore struct {
	mu      sync.RWMutex
	entries map[string]storedEntry // keyed by exactKey
	now     func() time.Time
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		entries: make(map[string]storedEntry),
		now:     time.Now,
	}
}

func (m *MemoryStore) Get(_ context.Context, key CacheKey, input []byte) (Entry, bool, error) {
	exactKey := buildExactKey(key, input)
	m.mu.RLock()
	stored, ok := m.entries[exactKey]
	m.mu.RUnlock()
	if !ok {
		return Entry{}, false, nil
	}
	if !stored.entry.ExpiresAt.IsZero() && m.now().After(stored.entry.ExpiresAt) {
		// Expired — evict lazily.
		m.mu.Lock()
		delete(m.entries, exactKey)
		m.mu.Unlock()
		return Entry{}, false, nil
	}
	e := stored.entry
	e.CacheSource = "exact"
	return e, true, nil
}

func (m *MemoryStore) Put(_ context.Context, key CacheKey, input []byte, entry Entry) error {
	exactKey := buildExactKey(key, input)
	entry.Key = exactKey
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = m.now().UTC()
	}
	m.mu.Lock()
	m.entries[exactKey] = storedEntry{
		entry:    entry,
		exactKey: exactKey,
		tenantID: key.TenantID,
	}
	m.mu.Unlock()
	return nil
}

func (m *MemoryStore) InvalidateByTag(_ context.Context, tenantID, tag string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for k, stored := range m.entries {
		if stored.tenantID != tenantID {
			continue
		}
		for _, t := range stored.entry.Tags {
			if t == tag {
				delete(m.entries, k)
				removed++
				break
			}
		}
	}
	return removed, nil
}

func (m *MemoryStore) Purge(_ context.Context, tenantID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for k, stored := range m.entries {
		if stored.tenantID == tenantID {
			delete(m.entries, k)
			removed++
		}
	}
	return removed, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers for the HTTP cache status endpoint
// ─────────────────────────────────────────────────────────────────────────────

// Snapshot returns a slice of all current entries (for debug/admin views).
// This is intentionally separate from Store to avoid polluting the interface.
func (m *MemoryStore) Snapshot() []Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Entry, 0, len(m.entries))
	for _, stored := range m.entries {
		out = append(out, stored.entry)
	}
	return out
}

// MarshalOutputJSON serialises any output value to JSON for storage.
func MarshalOutputJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

// UnmarshalOutputJSON deserialises stored JSON back into a map.
func UnmarshalOutputJSON(data []byte) (any, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return v, nil
}

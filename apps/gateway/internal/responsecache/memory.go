package responsecache

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store backed by a mutex-guarded map. Expired
// entries are evicted lazily on access. Hit and miss counters are tracked per
// scope. It is safe for concurrent use.
type MemoryStore struct {
	mu     sync.Mutex
	now    func() time.Time
	items  map[scopeKey]Entry
	hits   map[scope]int
	misses map[scope]int
}

type scope struct {
	tenantID string
	appID    string
}

type scopeKey struct {
	tenantID string
	appID    string
	key      string
}

// NewMemoryStore returns an empty in-memory store using time.Now as its clock.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		now:    time.Now,
		items:  make(map[scopeKey]Entry),
		hits:   make(map[scope]int),
		misses: make(map[scope]int),
	}
}

// WithClock overrides the clock used for lazy expiry checks. It returns the
// receiver for chaining and is primarily intended for deterministic tests.
func (s *MemoryStore) WithClock(now func() time.Time) *MemoryStore {
	if now != nil {
		s.now = now
	}
	return s
}

func (s *MemoryStore) Get(ctx context.Context, tenantID string, appID string, key string) (Entry, bool, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := scope{tenantID: tenantID, appID: appID}
	id := scopeKey{tenantID: tenantID, appID: appID, key: key}
	entry, ok := s.items[id]
	if !ok || s.isExpired(entry) {
		if ok {
			delete(s.items, id)
		}
		s.misses[sc]++
		return Entry{}, false, nil
	}
	s.hits[sc]++
	return cloneEntry(entry), true, nil
}

func (s *MemoryStore) Set(ctx context.Context, entry Entry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := cloneEntry(entry)
	stored.CreatedAt = stored.CreatedAt.UTC()
	stored.ExpiresAt = stored.ExpiresAt.UTC()
	s.items[scopeKey{tenantID: entry.TenantID, appID: entry.AppID, key: entry.Key}] = stored
	return nil
}

func (s *MemoryStore) Delete(ctx context.Context, tenantID string, appID string, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, scopeKey{tenantID: tenantID, appID: appID, key: key})
	return nil
}

func (s *MemoryStore) Purge(ctx context.Context, tenantID string, appID string) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for id := range s.items {
		if id.tenantID == tenantID && id.appID == appID {
			delete(s.items, id)
			removed++
		}
	}
	return removed, nil
}

func (s *MemoryStore) List(ctx context.Context, tenantID string, appID string, limit int) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	limit = normalizeLimit(limit)
	entries := make([]Entry, 0)
	for id, entry := range s.items {
		if id.tenantID != tenantID || id.appID != appID {
			continue
		}
		if s.isExpired(entry) {
			delete(s.items, id)
			continue
		}
		entries = append(entries, cloneEntry(entry))
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].Key < entries[j].Key
		}
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func (s *MemoryStore) Stats(ctx context.Context, tenantID string, appID string) (Stats, error) {
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := scope{tenantID: tenantID, appID: appID}
	count := 0
	for id, entry := range s.items {
		if id.tenantID != tenantID || id.appID != appID {
			continue
		}
		if s.isExpired(entry) {
			delete(s.items, id)
			continue
		}
		count++
	}
	return Stats{Entries: count, Hits: s.hits[sc], Misses: s.misses[sc]}, nil
}

func (s *MemoryStore) isExpired(entry Entry) bool {
	if entry.ExpiresAt.IsZero() {
		return false
	}
	return !entry.ExpiresAt.After(s.now().UTC())
}

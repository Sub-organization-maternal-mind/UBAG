package responsecache

import (
	"context"
	"time"
)

// LookupRequest describes a cache read for a potential job. When PrivacyMode is
// true the lookup always reports a miss and never touches the store.
type LookupRequest struct {
	TenantID    string
	AppID       string
	Target      string
	Command     string
	Input       []byte
	PrivacyMode bool
}

// StoreRequest describes a cache write for a completed job result. When
// PrivacyMode is true the store is a no-op so privacy data is never persisted.
type StoreRequest struct {
	TenantID    string
	AppID       string
	Target      string
	Command     string
	Input       []byte
	Value       []byte
	PrivacyMode bool
}

// Cache orchestrates a Store with a TTL and a privacy bypass. A disabled cache
// or a privacy-mode request short-circuits to a miss/no-op.
type Cache struct {
	store   Store
	ttl     time.Duration
	enabled bool
	now     func() time.Time
}

// Options configures a Cache.
type Options struct {
	TTL     time.Duration
	Enabled bool
	// Now overrides the clock used to compute CreatedAt/ExpiresAt. Defaults to
	// time.Now when nil. Inject a fixed clock for deterministic TTL tests.
	Now func() time.Time
}

// New returns a Cache wrapping store with the given options.
func New(store Store, opts Options) *Cache {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	ttl := opts.TTL
	if ttl < 0 {
		ttl = 0
	}
	return &Cache{store: store, ttl: ttl, enabled: opts.Enabled, now: now}
}

// Enabled reports whether the cache will read or write entries.
func (c *Cache) Enabled() bool {
	return c != nil && c.enabled && c.store != nil
}

// TTL returns the configured entry lifetime.
func (c *Cache) TTL() time.Duration {
	if c == nil {
		return 0
	}
	return c.ttl
}

// Key returns the deterministic cache key for a lookup request without touching
// the store. Useful for logging or pre-computing keys.
func (c *Cache) Key(req LookupRequest) string {
	return BuildKey(req.TenantID, req.AppID, req.Target, req.Command, req.Input)
}

// Lookup attempts to retrieve a cached entry. It returns a miss (ok=false, no
// error) when the cache is disabled or the request is privacy mode, ensuring
// privacy data is never read from cache.
func (c *Cache) Lookup(ctx context.Context, req LookupRequest) (Entry, bool, error) {
	if !c.Enabled() || req.PrivacyMode {
		return Entry{}, false, nil
	}
	key := BuildKey(req.TenantID, req.AppID, req.Target, req.Command, req.Input)
	return c.store.Get(ctx, req.TenantID, req.AppID, key)
}

// Store persists a result for future lookups. It is a no-op when the cache is
// disabled or the request is privacy mode, ensuring privacy data is never
// written to cache.
func (c *Cache) Store(ctx context.Context, req StoreRequest) error {
	if !c.Enabled() || req.PrivacyMode {
		return nil
	}
	now := c.now().UTC()
	entry := Entry{
		Key:       BuildKey(req.TenantID, req.AppID, req.Target, req.Command, req.Input),
		TenantID:  req.TenantID,
		AppID:     req.AppID,
		Target:    req.Target,
		Command:   req.Command,
		InputHash: HashInput(req.Input),
		Value:     req.Value,
		CreatedAt: now,
	}
	if c.ttl > 0 {
		entry.ExpiresAt = now.Add(c.ttl)
	}
	return c.store.Set(ctx, entry)
}

// Stats returns occupancy and access counters for a scope.
func (c *Cache) Stats(ctx context.Context, tenantID string, appID string) (Stats, error) {
	if c == nil || c.store == nil {
		return Stats{}, nil
	}
	return c.store.Stats(ctx, tenantID, appID)
}

// List returns the most recent entries for a scope.
func (c *Cache) List(ctx context.Context, tenantID string, appID string, limit int) ([]Entry, error) {
	if c == nil || c.store == nil {
		return nil, nil
	}
	return c.store.List(ctx, tenantID, appID, limit)
}

// Purge removes all entries for a scope and returns the count removed.
func (c *Cache) Purge(ctx context.Context, tenantID string, appID string) (int, error) {
	if c == nil || c.store == nil {
		return 0, nil
	}
	return c.store.Purge(ctx, tenantID, appID)
}

// Package responsecache provides a scoped, TTL'd cache used to short-circuit
// repeated identical jobs. Entries are keyed by a deterministic hash of the
// (tenant, app, target, command, input) tuple. The cache enforces a strict
// privacy bypass so that jobs flagged as privacy mode are never read from or
// written to the cache.
package responsecache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// ErrNotFound is returned by stores when a scoped entry does not exist.
var ErrNotFound = errors.New("cache entry not found")

// Entry is a single cached response scoped to a tenant and app.
type Entry struct {
	Key       string
	TenantID  string
	AppID     string
	Target    string
	Command   string
	InputHash string
	Value     []byte
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Stats summarizes cache occupancy and access counters for a scope.
type Stats struct {
	Entries int
	Hits    int
	Misses  int
}

// Store is the persistence contract for cached entries. All methods are scoped
// by tenant and app so that callers cannot read across tenant boundaries.
type Store interface {
	Get(ctx context.Context, tenantID string, appID string, key string) (Entry, bool, error)
	Set(ctx context.Context, entry Entry) error
	Delete(ctx context.Context, tenantID string, appID string, key string) error
	Purge(ctx context.Context, tenantID string, appID string) (int, error)
	List(ctx context.Context, tenantID string, appID string, limit int) ([]Entry, error)
	Stats(ctx context.Context, tenantID string, appID string) (Stats, error)
}

// BuildKey returns a deterministic sha256 hex key for the given scope tuple and
// opaque input. Identical inputs always produce identical keys, which lets the
// cache detect repeated jobs.
func BuildKey(tenantID string, appID string, target string, command string, input []byte) string {
	hasher := sha256.New()
	writeField(hasher, tenantID)
	writeField(hasher, appID)
	writeField(hasher, target)
	writeField(hasher, command)
	hasher.Write(input)
	return hex.EncodeToString(hasher.Sum(nil))
}

// HashInput returns the sha256 hex digest of an opaque input payload. It is used
// to populate Entry.InputHash without retaining the raw input.
func HashInput(input []byte) string {
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])
}

// writeField appends a length-prefixed field to the hash so that distinct field
// boundaries cannot collide (e.g. "ab"+"c" vs "a"+"bc").
func writeField(hasher interface{ Write([]byte) (int, error) }, value string) {
	var prefix [8]byte
	length := uint64(len(value))
	for i := 0; i < 8; i++ {
		prefix[i] = byte(length >> (8 * uint(i)))
	}
	hasher.Write(prefix[:])
	hasher.Write([]byte(value))
}

func cloneBytes(input []byte) []byte {
	if input == nil {
		return nil
	}
	output := make([]byte, len(input))
	copy(output, input)
	return output
}

func cloneEntry(entry Entry) Entry {
	entry.Value = cloneBytes(entry.Value)
	return entry
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 100
	}
	if limit > 1000 {
		return 1000
	}
	return limit
}

// Package audit provides a Merkle-chained, tamper-evident audit record store
// for the UBAG gateway. Records are appended per tenant and linked into a hash
// chain: each record stores the hash of the previous record for the same
// tenant plus its own hash computed over a canonical encoding of its fields.
// This makes silent insertion, deletion, or mutation of historical records
// detectable on export.
//
// Three backends mirror the gateway's webhook store conventions: an in-memory
// store (default / tests), a SQLite store, and a Postgres store.
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// GenesisHash is the prev_hash value used for the first record of a tenant's
// chain.
const GenesisHash = ""

// recordSeparator delimits canonical fields when computing a record hash so
// that field boundaries cannot be ambiguously shifted by adversarial content.
const recordSeparator = 0x1e

// Record is a single audit log entry in a tenant's hash chain.
type Record struct {
	ID         string         `json:"id"`
	Seq        int64          `json:"seq"`
	TenantID   string         `json:"tenant_id"`
	AppID      string         `json:"app_id"`
	Actor      string         `json:"actor"`
	Action     string         `json:"action"`
	Resource   string         `json:"resource"`
	Outcome    string         `json:"outcome"`
	OccurredAt time.Time      `json:"occurred_at"`
	Attributes map[string]any `json:"attributes,omitempty"`
	PrevHash   string         `json:"prev_hash"`
	RecordHash string         `json:"record_hash"`

	// attributesJSON is the exact canonical attributes encoding used to compute
	// RecordHash. It is populated on Append and on List so that VerifyChain can
	// recompute the hash from the persisted bytes without re-marshalling drift.
	attributesJSON string
}

// Filter constrains a List query.
type Filter struct {
	TenantID string
	Since    time.Time // inclusive lower bound on OccurredAt; zero means unbounded
	Until    time.Time // exclusive upper bound on OccurredAt; zero means unbounded
	Limit    int       // 0 means no limit
}

// Store persists audit records and exposes chain inspection.
type Store interface {
	Ready(ctx context.Context) error
	// Append links rec onto its tenant's chain and persists it. The caller
	// supplies TenantID/AppID/Actor/Action/Resource/Outcome/OccurredAt and
	// Attributes; Seq, PrevHash, RecordHash and ID are assigned by the store.
	Append(ctx context.Context, rec Record) (Record, error)
	// List returns records matching filter ordered by Seq ascending.
	List(ctx context.Context, filter Filter) ([]Record, error)
	// Head returns the most recent record hash and seq for a tenant. When the
	// tenant has no records it returns (GenesisHash, 0, nil).
	Head(ctx context.Context, tenantID string) (string, int64, error)
}

// canonicalAttributes returns the deterministic JSON encoding of attrs.
// encoding/json sorts map keys, so the output is stable for equal maps.
func canonicalAttributes(attrs map[string]any) (string, error) {
	if len(attrs) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(attrs)
	if err != nil {
		return "", fmt.Errorf("audit: marshal attributes: %w", err)
	}
	return string(encoded), nil
}

// canonicalTime renders t as a microsecond-precision UTC RFC3339 string. The
// fixed precision survives a round trip through both Postgres TIMESTAMPTZ and
// SQLite TEXT so the hash recomputes identically on read.
func canonicalTime(t time.Time) string {
	return t.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z07:00")
}

// computeHash derives a record hash over the canonical field encoding.
func computeHash(tenantID, appID, actor, action, resource, outcome, occurredAt, attributesJSON, prevHash string) string {
	hasher := sha256.New()
	for _, field := range []string{tenantID, appID, actor, action, resource, outcome, occurredAt, attributesJSON, prevHash} {
		hasher.Write([]byte(field))
		hasher.Write([]byte{recordSeparator})
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func stableID(prefix string, parts ...any) string {
	sum := sha256.Sum256([]byte(fmt.Sprint(parts...)))
	return prefix + "_" + hex.EncodeToString(sum[:])[:24]
}

// decodeAttributes parses a persisted canonical attributes JSON object back
// into a map. It returns nil on malformed input; VerifyChain relies on the
// stored attributesJSON bytes, not this decoded view, so a decode miss never
// affects chain validation.
func decodeAttributes(encoded string) map[string]any {
	var attrs map[string]any
	if err := json.Unmarshal([]byte(encoded), &attrs); err != nil {
		return nil
	}
	return attrs
}

// prepare normalises an incoming record, computes its canonical attributes
// encoding, and returns the canonical occurred-at string.
func prepare(rec *Record) (string, error) {
	if rec.OccurredAt.IsZero() {
		rec.OccurredAt = time.Now()
	}
	rec.OccurredAt = rec.OccurredAt.UTC().Truncate(time.Microsecond)
	attributesJSON, err := canonicalAttributes(rec.Attributes)
	if err != nil {
		return "", err
	}
	rec.attributesJSON = attributesJSON
	return canonicalTime(rec.OccurredAt), nil
}

// canonicalAttributesFor returns the canonical attributes encoding for a record,
// preferring the persisted bytes captured on Append/List.
func (r Record) canonicalAttributesFor() (string, error) {
	if r.attributesJSON != "" {
		return r.attributesJSON, nil
	}
	return canonicalAttributes(r.Attributes)
}

// VerifyChain validates that records (ordered by Seq ascending) form an intact
// hash chain: every record's hash recomputes from its canonical fields and each
// record's PrevHash links to the previous returned record's RecordHash. It is
// safe to call on a contiguous window of a tenant's chain.
func VerifyChain(records []Record) bool {
	for i, rec := range records {
		attributesJSON, err := rec.canonicalAttributesFor()
		if err != nil {
			return false
		}
		want := computeHash(rec.TenantID, rec.AppID, rec.Actor, rec.Action, rec.Resource, rec.Outcome, canonicalTime(rec.OccurredAt), attributesJSON, rec.PrevHash)
		if want != rec.RecordHash {
			return false
		}
		if i > 0 && rec.PrevHash != records[i-1].RecordHash {
			return false
		}
	}
	return true
}

// MemoryStore is an in-memory Store, primarily for development and tests.
type MemoryStore struct {
	mu      sync.Mutex
	byTenant map[string][]Record
}

// NewMemoryStore returns an empty in-memory audit store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byTenant: make(map[string][]Record)}
}

func (m *MemoryStore) Ready(context.Context) error { return nil }

func (m *MemoryStore) Append(_ context.Context, rec Record) (Record, error) {
	occurredAt, err := prepare(&rec)
	if err != nil {
		return Record{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	chain := m.byTenant[rec.TenantID]
	prevHash := GenesisHash
	if len(chain) > 0 {
		prevHash = chain[len(chain)-1].RecordHash
	}
	rec.Seq = int64(len(chain)) + 1
	rec.PrevHash = prevHash
	rec.RecordHash = computeHash(rec.TenantID, rec.AppID, rec.Actor, rec.Action, rec.Resource, rec.Outcome, occurredAt, rec.attributesJSON, prevHash)
	rec.ID = stableID("audit", rec.TenantID, rec.Seq, rec.RecordHash)

	m.byTenant[rec.TenantID] = append(chain, rec)
	return rec, nil
}

func (m *MemoryStore) List(_ context.Context, filter Filter) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	chain := m.byTenant[filter.TenantID]
	out := make([]Record, 0, len(chain))
	for _, rec := range chain {
		if !filter.Since.IsZero() && rec.OccurredAt.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && !rec.OccurredAt.Before(filter.Until) {
			continue
		}
		out = append(out, rec)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (m *MemoryStore) Head(_ context.Context, tenantID string) (string, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	chain := m.byTenant[tenantID]
	if len(chain) == 0 {
		return GenesisHash, 0, nil
	}
	last := chain[len(chain)-1]
	return last.RecordHash, last.Seq, nil
}

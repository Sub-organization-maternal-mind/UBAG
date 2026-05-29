package siem

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// sinkConfigTimeLayout stores timestamps as fixed-width millisecond RFC3339
// UTC strings so lexical ordering matches chronological ordering in SQLite.
const sinkConfigTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// SinkConfig is non-secret, tenant-scoped persisted configuration for a sink.
// It deliberately has NO field for raw secret material: only a SecretRef (an
// opaque reference id resolved elsewhere) is stored, so secrets are never
// persisted by a ConfigStore.
type SinkConfig struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`              // "file" | "http" | "syslog"
	Target    string    `json:"target"`            // file path, URL, or host:port
	Network   string    `json:"network,omitempty"` // syslog only: "udp" | "tcp"
	SecretRef string    `json:"secret_ref,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ConfigStore persists non-secret sink configuration scoped by tenant.
type ConfigStore interface {
	Ready(ctx context.Context) error
	Put(ctx context.Context, config SinkConfig) (SinkConfig, error)
	Get(ctx context.Context, tenantID string, id string) (SinkConfig, bool, error)
	List(ctx context.Context, tenantID string) ([]SinkConfig, error)
	Delete(ctx context.Context, tenantID string, id string) (bool, error)
}

func validateSinkConfig(config SinkConfig) error {
	if strings.TrimSpace(config.TenantID) == "" {
		return fmt.Errorf("siem: sink config tenant id is required")
	}
	switch strings.TrimSpace(strings.ToLower(config.Kind)) {
	case "file", "http", "syslog":
	default:
		return fmt.Errorf("siem: unsupported sink kind %q", config.Kind)
	}
	if strings.TrimSpace(config.Target) == "" {
		return fmt.Errorf("siem: sink config target is required")
	}
	return nil
}

func normalizeSinkConfig(config SinkConfig, now time.Time) SinkConfig {
	config.TenantID = strings.TrimSpace(config.TenantID)
	config.Name = strings.TrimSpace(config.Name)
	config.Kind = strings.TrimSpace(strings.ToLower(config.Kind))
	config.Target = strings.TrimSpace(config.Target)
	config.Network = strings.TrimSpace(strings.ToLower(config.Network))
	config.SecretRef = strings.TrimSpace(config.SecretRef)
	if config.ID == "" {
		config.ID = stableID("siemsink", config.TenantID, config.Kind, config.Target, config.Name)
	}
	if config.CreatedAt.IsZero() {
		config.CreatedAt = now
	} else {
		config.CreatedAt = config.CreatedAt.UTC()
	}
	config.UpdatedAt = now
	return config
}

func stableID(prefix string, parts ...string) string {
	sum := sha256.Sum256([]byte(fmt.Sprint(parts)))
	return prefix + "_" + hex.EncodeToString(sum[:])[:24]
}

// MemoryStore is an in-memory ConfigStore, primarily for tests and the
// single-node default deployment.
type MemoryStore struct {
	mu      sync.Mutex
	now     func() time.Time
	configs map[string]SinkConfig // key: tenantID + "\x00" + id
}

// NewMemoryStore constructs an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{now: time.Now, configs: map[string]SinkConfig{}}
}

func memoryKey(tenantID string, id string) string {
	return tenantID + "\x00" + id
}

// Ready implements ConfigStore.
func (m *MemoryStore) Ready(context.Context) error { return nil }

// Put implements ConfigStore.
func (m *MemoryStore) Put(_ context.Context, config SinkConfig) (SinkConfig, error) {
	if err := validateSinkConfig(config); err != nil {
		return SinkConfig{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	if config.ID != "" {
		if existing, ok := m.configs[memoryKey(config.TenantID, config.ID)]; ok {
			config.CreatedAt = existing.CreatedAt
		}
	}
	config = normalizeSinkConfig(config, now)
	m.configs[memoryKey(config.TenantID, config.ID)] = config
	return config, nil
}

// Get implements ConfigStore.
func (m *MemoryStore) Get(_ context.Context, tenantID string, id string) (SinkConfig, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	config, ok := m.configs[memoryKey(tenantID, id)]
	if !ok {
		return SinkConfig{}, false, nil
	}
	return config, true, nil
}

// List implements ConfigStore.
func (m *MemoryStore) List(_ context.Context, tenantID string) ([]SinkConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	output := []SinkConfig{}
	for _, config := range m.configs {
		if config.TenantID == tenantID {
			output = append(output, config)
		}
	}
	sort.Slice(output, func(i, j int) bool { return output[i].ID < output[j].ID })
	return output, nil
}

// Delete implements ConfigStore.
func (m *MemoryStore) Delete(_ context.Context, tenantID string, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := memoryKey(tenantID, id)
	if _, ok := m.configs[key]; !ok {
		return false, nil
	}
	delete(m.configs, key)
	return true, nil
}

// SQLiteStore is a ConfigStore backed by SQLite via database/sql. It owns its
// schema (CREATE TABLE IF NOT EXISTS) and stores only non-secret fields.
type SQLiteStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewSQLiteStore constructs a SQLiteStore over db.
func NewSQLiteStore(db *sql.DB) *SQLiteStore {
	return &SQLiteStore{db: db, now: time.Now}
}

const sqliteCreateTable = `
CREATE TABLE IF NOT EXISTS gateway_siem_sink_configs (
	id TEXT PRIMARY KEY,
	tenant_id TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL,
	target TEXT NOT NULL,
	network TEXT NOT NULL DEFAULT '',
	secret_ref TEXT NOT NULL DEFAULT '',
	enabled INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`

// Ready ensures the connection is usable and the schema exists.
func (s *SQLiteStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("siem: sqlite config store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, sqliteCreateTable)
	return err
}

// Put implements ConfigStore.
func (s *SQLiteStore) Put(ctx context.Context, config SinkConfig) (SinkConfig, error) {
	if err := validateSinkConfig(config); err != nil {
		return SinkConfig{}, err
	}
	if err := s.Ready(ctx); err != nil {
		return SinkConfig{}, err
	}
	// Truncate to the storage layout's millisecond precision so the returned
	// value matches what a subsequent Get observes.
	now := s.now().UTC().Truncate(time.Millisecond)
	if config.ID == "" {
		config.ID = stableID("siemsink", strings.TrimSpace(config.TenantID), strings.ToLower(strings.TrimSpace(config.Kind)), strings.TrimSpace(config.Target), strings.TrimSpace(config.Name))
	}
	if existing, ok, err := s.Get(ctx, strings.TrimSpace(config.TenantID), config.ID); err != nil {
		return SinkConfig{}, err
	} else if ok {
		config.CreatedAt = existing.CreatedAt
	}
	config = normalizeSinkConfig(config, now)
	enabled := 0
	if config.Enabled {
		enabled = 1
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO gateway_siem_sink_configs (
	id, tenant_id, name, kind, target, network, secret_ref, enabled, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
	tenant_id = excluded.tenant_id,
	name = excluded.name,
	kind = excluded.kind,
	target = excluded.target,
	network = excluded.network,
	secret_ref = excluded.secret_ref,
	enabled = excluded.enabled,
	updated_at = excluded.updated_at`,
		config.ID, config.TenantID, config.Name, config.Kind, config.Target, config.Network,
		config.SecretRef, enabled, formatSinkConfigTime(config.CreatedAt), formatSinkConfigTime(config.UpdatedAt))
	if err != nil {
		return SinkConfig{}, err
	}
	return config, nil
}

const selectSinkConfigColumns = `id, tenant_id, name, kind, target, network, secret_ref, enabled, created_at, updated_at`

// Get implements ConfigStore.
func (s *SQLiteStore) Get(ctx context.Context, tenantID string, id string) (SinkConfig, bool, error) {
	if s == nil || s.db == nil {
		return SinkConfig{}, false, fmt.Errorf("siem: sqlite config store is not configured")
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+selectSinkConfigColumns+` FROM gateway_siem_sink_configs WHERE tenant_id = ? AND id = ?`, tenantID, id)
	config, err := scanSinkConfig(row)
	if err == sql.ErrNoRows {
		return SinkConfig{}, false, nil
	}
	if err != nil {
		return SinkConfig{}, false, err
	}
	return config, true, nil
}

// List implements ConfigStore.
func (s *SQLiteStore) List(ctx context.Context, tenantID string) ([]SinkConfig, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("siem: sqlite config store is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+selectSinkConfigColumns+` FROM gateway_siem_sink_configs WHERE tenant_id = ? ORDER BY id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	output := []SinkConfig{}
	for rows.Next() {
		config, err := scanSinkConfig(rows)
		if err != nil {
			return nil, err
		}
		output = append(output, config)
	}
	return output, rows.Err()
}

// Delete implements ConfigStore.
func (s *SQLiteStore) Delete(ctx context.Context, tenantID string, id string) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("siem: sqlite config store is not configured")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM gateway_siem_sink_configs WHERE tenant_id = ? AND id = ?`, tenantID, id)
	if err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSinkConfig(scanner rowScanner) (SinkConfig, error) {
	var (
		config    SinkConfig
		enabled   int
		createdAt string
		updatedAt string
	)
	if err := scanner.Scan(
		&config.ID, &config.TenantID, &config.Name, &config.Kind, &config.Target,
		&config.Network, &config.SecretRef, &enabled, &createdAt, &updatedAt,
	); err != nil {
		return SinkConfig{}, err
	}
	config.Enabled = enabled != 0
	config.CreatedAt = parseSinkConfigTime(createdAt)
	config.UpdatedAt = parseSinkConfigTime(updatedAt)
	return config, nil
}

func formatSinkConfigTime(t time.Time) string {
	return t.UTC().Format(sinkConfigTimeLayout)
}

func parseSinkConfigTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{sinkConfigTimeLayout, time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

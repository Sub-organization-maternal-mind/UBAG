package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// StoreKind identifies the backing store type.
type StoreKind string

const (
	StoreKindSQLite   StoreKind = "sqlite"
	StoreKindPostgres StoreKind = "postgres"
)

// ComponentEntry describes one backed-up component.
type ComponentEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`       // relative path inside the backup
	Checksum  string `json:"checksum"`   // hex SHA-256 of the file
	SizeBytes int64  `json:"size_bytes"`
}

// Manifest is the top-level backup descriptor written as manifest.json.
type Manifest struct {
	CreatedAt  time.Time        `json:"created_at"`
	Profile    string           `json:"profile"`
	StoreKind  StoreKind        `json:"store_kind"`
	Components []ComponentEntry `json:"components"`
}

// Write serialises the manifest to <dir>/manifest.json.
func (m *Manifest) Write(dir string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("backup: marshal manifest: %w", err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("backup: write manifest.json: %w", err)
	}
	return nil
}

// ReadManifest reads and parses the manifest.json from dir.
func ReadManifest(dir string) (*Manifest, error) {
	path := filepath.Join(dir, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("backup: read manifest.json: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("backup: parse manifest.json: %w", err)
	}
	return &m, nil
}

// Verify checks that every component file exists in dir and its SHA-256
// matches the stored checksum. Returns a non-nil error listing all mismatches.
func (m *Manifest) Verify(dir string) error {
	var errs []string
	for _, c := range m.Components {
		fullPath := filepath.Join(dir, c.Path)
		got, _, err := checksumFile(fullPath)
		if err != nil {
			errs = append(errs, fmt.Sprintf("component %q: read error: %v", c.Name, err))
			continue
		}
		if got != c.Checksum {
			errs = append(errs, fmt.Sprintf("component %q: checksum mismatch (want %s, got %s)", c.Name, c.Checksum, got))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("backup: verify failed:\n  %s", joinLines(errs))
	}
	return nil
}

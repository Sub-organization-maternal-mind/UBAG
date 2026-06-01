package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEdgeInit_CreatesConfigFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

	if err := EdgeInit(); err != nil {
		t.Fatalf("EdgeInit returned error: %v", err)
	}

	cfgPath := filepath.Join(tmp, ".ubag", "config.json")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config.json not created: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config.json is not valid JSON: %v", err)
	}
	if cfg.BaseURL != "http://localhost:8080" {
		t.Errorf("BaseURL = %q, want http://localhost:8080", cfg.BaseURL)
	}
	if cfg.APIVersion != DefaultAPIVersion {
		t.Errorf("APIVersion = %q, want %q", cfg.APIVersion, DefaultAPIVersion)
	}

	envPath := filepath.Join(tmp, ".ubag", ".env.example")
	envData, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf(".env.example not created: %v", err)
	}
	if !strings.Contains(string(envData), "UBAG_PROFILE=edge") {
		t.Errorf(".env.example missing UBAG_PROFILE=edge")
	}
}

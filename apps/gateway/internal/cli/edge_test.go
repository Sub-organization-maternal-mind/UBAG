package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEdgeInit_CreatesConfigFiles(t *testing.T) {
	// Override config path so EdgeInit writes to temp dir.
	tmp := t.TempDir()
	// Patch HOME so os.UserHomeDir returns tmp.
	t.Setenv("USERPROFILE", tmp) // Windows
	t.Setenv("HOME", tmp)        // Unix

	if err := EdgeInit(); err != nil {
		t.Fatalf("EdgeInit returned error: %v", err)
	}

	cfgPath := filepath.Join(tmp, ".ubag", "config.json")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config.json not created: %v", err)
	}

	envPath := filepath.Join(tmp, ".ubag", ".env.example")
	if _, err := os.Stat(envPath); err != nil {
		t.Fatalf(".env.example not created: %v", err)
	}
}

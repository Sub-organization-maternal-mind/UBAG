package serve

import (
	"context"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/pat"
)

func TestPATDisabledByDefault(t *testing.T) {
	t.Setenv("UBAG_PAT_ENABLED", "")
	out, err := newEnterpriseStoresFromEnv(context.Background(), "memory", nil)
	if err != nil {
		t.Fatalf("newEnterpriseStoresFromEnv: %v", err)
	}
	if out.pat != nil {
		t.Fatalf("PAT store should be nil (disabled) by default, got %T", out.pat)
	}
}

func TestPATEnabledMemoryStore(t *testing.T) {
	t.Setenv("UBAG_PAT_ENABLED", "true")
	t.Setenv("UBAG_PAT_DEFAULT_TTL_MS", "")
	out, err := newEnterpriseStoresFromEnv(context.Background(), "memory", nil)
	if err != nil {
		t.Fatalf("newEnterpriseStoresFromEnv: %v", err)
	}
	if _, ok := out.pat.(*pat.MemoryStore); !ok {
		t.Fatalf("expected *pat.MemoryStore when enabled with memory kind, got %T", out.pat)
	}
	if out.patDefaultTTL != 0 {
		t.Fatalf("default PAT TTL should be 0 (no expiry), got %v", out.patDefaultTTL)
	}
}

func TestPATDefaultTTLFromEnv(t *testing.T) {
	t.Setenv("UBAG_PAT_ENABLED", "true")
	t.Setenv("UBAG_PAT_DEFAULT_TTL_MS", "3600000") // 1h
	out, err := newEnterpriseStoresFromEnv(context.Background(), "memory", nil)
	if err != nil {
		t.Fatalf("newEnterpriseStoresFromEnv: %v", err)
	}
	if out.patDefaultTTL != time.Hour {
		t.Fatalf("PAT TTL = %v, want 1h", out.patDefaultTTL)
	}
}

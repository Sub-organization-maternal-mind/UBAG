package resilience

import (
	"fmt"
	"testing"
)

// TestRegistryBounded verifies the lazy breaker registry evicts its
// oldest-created entries past the bound, so unvalidated target strings cannot
// grow it without limit.
func TestRegistryBounded(t *testing.T) {
	registry := NewRegistry(DefaultConfig())

	const total = defaultMaxBreakers + 50
	for index := 0; index < total; index++ {
		registry.Get(KindWebhook, fmt.Sprintf("https://hooks.example.test/%d", index))
	}

	snapshot := registry.Snapshot()
	if len(snapshot) > defaultMaxBreakers {
		t.Fatalf("registry not bounded: %d breakers > %d", len(snapshot), defaultMaxBreakers)
	}
}

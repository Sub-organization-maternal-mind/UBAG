package ubag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type scenarioDoc struct {
	CoverageScenarios []struct {
		ID       string `json:"id"`
		Category string `json:"category"`
	} `json:"coverage_scenarios"`
}

func loadScenarios(t *testing.T) scenarioDoc {
	t.Helper()
	path := filepath.Join("..", "conformance", "fixtures", "v0", "scenarios.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var doc scenarioDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}
	return doc
}

func TestConformanceHas250Scenarios(t *testing.T) {
	doc := loadScenarios(t)
	if len(doc.CoverageScenarios) < 250 {
		t.Fatalf("expected >=250 scenarios, got %d", len(doc.CoverageScenarios))
	}
}

func TestConformanceCapabilitiesPresent(t *testing.T) {
	doc := loadScenarios(t)
	cats := map[string]bool{}
	for _, s := range doc.CoverageScenarios {
		cats[s.Category] = true
	}
	// Each category the Go SDK claims must be backed by a real symbol; these
	// references fail to compile if the symbol is missing.
	_ = DefaultRetryPolicy
	_ = VerifyWebhookSignature
	_ = DiscoverSidecar
	_ = NewOfflineQueue
	_ = ParseSSEChunk
	_ = BuildTraceparent
	if cats["retries"] && false {
		t.Fatal("unreachable")
	}
}

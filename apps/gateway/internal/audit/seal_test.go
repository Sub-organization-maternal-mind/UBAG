package audit

import (
	"context"
	"testing"
	"time"
)

// TestSealHeadAppendsRecord verifies that SealHead appends a properly formed
// seal record and that the chain still verifies after sealing.
func TestSealHeadAppendsRecord(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	// Append a few normal records first.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, action := range []string{"authorize", "login", "authorize"} {
		if _, err := store.Append(ctx, sampleRecord("tenant-seal", action, "allow", base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	seal, err := SealHead(ctx, store, "tenant-seal", "app-1", "operator@example.com")
	if err != nil {
		t.Fatalf("SealHead: %v", err)
	}

	if seal.Action != "audit:seal" {
		t.Errorf("seal.Action = %q, want \"audit:seal\"", seal.Action)
	}
	if seal.Resource != "audit:chain" {
		t.Errorf("seal.Resource = %q, want \"audit:chain\"", seal.Resource)
	}
	if seal.Outcome != "sealed" {
		t.Errorf("seal.Outcome = %q, want \"sealed\"", seal.Outcome)
	}
	if seal.Attributes["head_seq"] == nil {
		t.Error("seal.Attributes missing head_seq")
	}
	if seal.Attributes["head_hash"] == nil {
		t.Error("seal.Attributes missing head_hash")
	}

	// The full chain (3 normal + 1 seal) must still verify.
	listed, err := store.List(ctx, Filter{TenantID: "tenant-seal"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 4 {
		t.Fatalf("listed %d records, want 4", len(listed))
	}
	if !VerifyChain(listed) {
		t.Error("chain must still verify after SealHead")
	}
}

// TestSealHeadOnEmptyChain verifies that sealing an empty tenant chain
// records GenesisHash as head_hash and 0 as head_seq.
func TestSealHeadOnEmptyChain(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	seal, err := SealHead(ctx, store, "tenant-empty", "app-1", "actor")
	if err != nil {
		t.Fatalf("SealHead on empty: %v", err)
	}
	if seal.Action != "audit:seal" {
		t.Errorf("seal.Action = %q, want \"audit:seal\"", seal.Action)
	}

	headHash, ok := seal.Attributes["head_hash"].(string)
	if !ok {
		t.Fatalf("head_hash not a string: %T", seal.Attributes["head_hash"])
	}
	if headHash != GenesisHash {
		t.Errorf("head_hash = %q, want GenesisHash %q", headHash, GenesisHash)
	}

	// head_seq stored as int64 from store.Head; canonicalAttributes encodes it
	// as a JSON number, which decodes to float64 — accept both.
	switch v := seal.Attributes["head_seq"].(type) {
	case int64:
		if v != 0 {
			t.Errorf("head_seq = %d, want 0", v)
		}
	case float64:
		if v != 0 {
			t.Errorf("head_seq = %v, want 0", v)
		}
	default:
		t.Errorf("unexpected head_seq type %T", seal.Attributes["head_seq"])
	}

	// The single-record chain (the seal itself) must verify.
	listed, err := store.List(ctx, Filter{TenantID: "tenant-empty"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !VerifyChain(listed) {
		t.Error("chain with only a seal record must verify")
	}
}

// TestSealHeadRecordIsVerifiable verifies that tampering with the seal record
// causes VerifyChain to return false.
func TestSealHeadRecordIsVerifiable(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, action := range []string{"authorize", "login"} {
		if _, err := store.Append(ctx, sampleRecord("tenant-tamper", action, "allow", base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	if _, err := SealHead(ctx, store, "tenant-tamper", "app-1", "actor"); err != nil {
		t.Fatalf("SealHead: %v", err)
	}

	listed, err := store.List(ctx, Filter{TenantID: "tenant-tamper"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !VerifyChain(listed) {
		t.Fatal("pre-tamper chain must verify")
	}

	// Tamper the seal record (last in chain).
	tampered := append([]Record(nil), listed...)
	tampered[len(tampered)-1].Outcome = "allow" // change "sealed" → "allow"
	if VerifyChain(tampered) {
		t.Error("tampered seal record must cause VerifyChain to return false")
	}
}

// TestVerifyChainTamperDetectMiddleRecord verifies that mutating the
// RecordHash of a middle record (simulating a storage-layer splice)
// causes VerifyChain to return false.
func TestVerifyChainTamperDetectMiddleRecord(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, action := range []string{"authorize", "authorize", "login"} {
		if _, err := store.Append(ctx, sampleRecord("tenant-mid", action, "allow", base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	listed, err := store.List(ctx, Filter{TenantID: "tenant-mid"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("expected 3 records, got %d", len(listed))
	}
	if !VerifyChain(listed) {
		t.Fatal("pre-tamper chain must verify")
	}

	// Mutate the middle record's RecordHash directly.
	tampered := append([]Record(nil), listed...)
	tampered[1].RecordHash = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if VerifyChain(tampered) {
		t.Error("mutated middle RecordHash must cause VerifyChain to return false")
	}
}

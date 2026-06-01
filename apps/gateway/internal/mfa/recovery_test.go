package mfa

import (
	"context"
	"testing"

	gocrypto "github.com/ubag/ubag/apps/gateway/internal/crypto"
)

// TestGenerateRecoveryCodes verifies that 8 unique codes are generated and
// each matches its hash via crypto.VerifyPassword.
func TestGenerateRecoveryCodes(t *testing.T) {
	codes, hashes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}

	if len(codes) != recoveryCodeCount {
		t.Errorf("expected %d codes, got %d", recoveryCodeCount, len(codes))
	}
	if len(hashes) != recoveryCodeCount {
		t.Errorf("expected %d hashes, got %d", recoveryCodeCount, len(hashes))
	}

	// Each code must be unique.
	seen := make(map[string]struct{})
	for _, c := range codes {
		if _, dup := seen[c]; dup {
			t.Errorf("duplicate recovery code: %s", c)
		}
		seen[c] = struct{}{}
	}

	// Each code must verify against its hash.
	for i, c := range codes {
		if !gocrypto.VerifyPassword(hashes[i], c) {
			t.Errorf("code[%d] does not match its hash", i)
		}
	}

	// Each code must have the right length.
	for i, c := range codes {
		if len(c) != recoveryCodeLength {
			t.Errorf("code[%d] has length %d, want %d", i, len(c), recoveryCodeLength)
		}
	}
}

// TestRecoveryCodeSingleUse verifies that ConsumeRecovery returns true on first
// call and false on a second call with the same code.
func TestRecoveryCodeSingleUse(t *testing.T) {
	codes, hashes, err := GenerateRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}

	store := NewMemoryStore()
	ctx := context.Background()

	err = store.Enroll(ctx, Enrollment{
		TenantID:       "t1",
		UserID:         "u1",
		Secret:         "JBSWY3DPEHPK3PXP", // arbitrary valid base32
		RecoveryHashes: hashes,
	})
	if err != nil {
		t.Fatal(err)
	}

	// First use must succeed.
	ok, err := store.ConsumeRecovery(ctx, "t1", "u1", codes[0])
	if err != nil || !ok {
		t.Fatalf("first ConsumeRecovery should succeed: ok=%v err=%v", ok, err)
	}

	// Second use of the same code must fail.
	ok, err = store.ConsumeRecovery(ctx, "t1", "u1", codes[0])
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("second ConsumeRecovery with same code should return false")
	}

	// Other codes must still work.
	ok, err = store.ConsumeRecovery(ctx, "t1", "u1", codes[1])
	if err != nil || !ok {
		t.Fatalf("second recovery code should still work: ok=%v err=%v", ok, err)
	}
}

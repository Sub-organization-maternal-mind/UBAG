package mfa

import (
	"context"
	"testing"
	"time"
)

// TestEnrollVerifyRoundtrip is a full integration test of the Enroll → VerifyCode
// flow with a deterministic clock.
func TestEnrollVerifyRoundtrip(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	now := time.Unix(1_700_000_000, 0)

	result, err := Enroll(ctx, store, EnrollRequest{
		TenantID: "tenant_acme",
		UserID:   "user_alice",
		Issuer:   "UBAG",
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	if result.Secret == "" {
		t.Error("expected non-empty secret")
	}
	if result.OTPAuthURI == "" {
		t.Error("expected non-empty OTPAuthURI")
	}
	if len(result.RecoveryCodes) != recoveryCodeCount {
		t.Errorf("expected %d recovery codes, got %d", recoveryCodeCount, len(result.RecoveryCodes))
	}

	// Generate the TOTP code for "now" using the returned secret.
	code, err := TOTP(result.Secret, now)
	if err != nil {
		t.Fatalf("TOTP: %v", err)
	}

	// VerifyCode with TOTP must succeed.
	ok, err := VerifyCode(ctx, store, "tenant_acme", "user_alice", code, now)
	if err != nil {
		t.Fatalf("VerifyCode TOTP: %v", err)
	}
	if !ok {
		t.Error("TOTP VerifyCode should return true after enrollment")
	}

	// VerifyCode with a recovery code must succeed.
	ok, err = VerifyCode(ctx, store, "tenant_acme", "user_alice", result.RecoveryCodes[0], now)
	if err != nil {
		t.Fatalf("VerifyCode recovery: %v", err)
	}
	if !ok {
		t.Error("recovery code VerifyCode should return true after enrollment")
	}

	// Recovery code must be single-use.
	ok, err = VerifyCode(ctx, store, "tenant_acme", "user_alice", result.RecoveryCodes[0], now)
	if err != nil {
		t.Fatalf("unexpected error on second recovery: %v", err)
	}
	if ok {
		t.Error("second use of same recovery code should return false")
	}

	// Non-enrolled user must return false.
	ok, err = VerifyCode(ctx, store, "tenant_acme", "user_bob", code, now)
	if err != nil {
		t.Fatalf("unexpected error for unknown user: %v", err)
	}
	if ok {
		t.Error("unknown user should not verify")
	}
}

// TestServiceVerify exercises the Service wrapper with an injected clock.
func TestServiceVerify(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	fixedTime := time.Unix(1_700_001_000, 0)
	svc := &Service{
		Store: store,
		Clock: func() time.Time { return fixedTime },
	}

	result, err := svc.Enroll(ctx, EnrollRequest{
		TenantID: "t1",
		UserID:   "u1",
		Issuer:   "UBAG",
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	code, err := TOTP(result.Secret, fixedTime)
	if err != nil {
		t.Fatalf("TOTP: %v", err)
	}

	ok, err := svc.Verify(ctx, "t1", "u1", code)
	if err != nil || !ok {
		t.Errorf("Service.Verify should return true: ok=%v err=%v", ok, err)
	}
}

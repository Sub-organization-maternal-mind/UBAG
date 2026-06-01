package mfa

import (
	"testing"
	"time"
)

// TestTOTPRFC6238Vectors verifies the SHA1 test vectors from RFC 6238 Appendix B.
// The seed "12345678901234567890" is the raw ASCII key (NOT base32-encoded).
// We call hotpRaw directly so we can pass the raw key bytes.
func TestTOTPRFC6238Vectors(t *testing.T) {
	seed := []byte("12345678901234567890")

	vectors := []struct {
		unixTime int64
		want     string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
	}

	for _, v := range vectors {
		counter := uint64(v.unixTime / TOTP_Period)
		got, err := hotpRaw(seed, counter)
		if err != nil {
			t.Fatalf("hotpRaw(t=%d) error: %v", v.unixTime, err)
		}
		if got != v.want {
			t.Errorf("hotpRaw(t=%d): got %q, want %q", v.unixTime, got, v.want)
		}
	}
}

// TestVerifySkewWindow checks that codes at t-30, t, t+30 are accepted but
// t-60 (two steps back) is rejected.
func TestVerifySkewWindow(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}

	base := time.Unix(1_700_000_000, 0) // arbitrary stable reference

	// t+0 must pass
	code, err := TOTP(secret, base)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := Verify(secret, code, base)
	if err != nil || !ok {
		t.Errorf("code at t+0 should verify: ok=%v err=%v", ok, err)
	}

	// code at t-30 (one step back) must pass when verified at t
	prev, err := TOTP(secret, base.Add(-TOTP_Period*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ok, err = Verify(secret, prev, base)
	if err != nil || !ok {
		t.Errorf("code at t-30 should verify with ±1 skew: ok=%v err=%v", ok, err)
	}

	// code at t+30 (one step ahead) must pass when verified at t
	next, err := TOTP(secret, base.Add(TOTP_Period*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ok, err = Verify(secret, next, base)
	if err != nil || !ok {
		t.Errorf("code at t+30 should verify with ±1 skew: ok=%v err=%v", ok, err)
	}

	// code at t-60 (two steps back) must NOT pass
	old, err := TOTP(secret, base.Add(-2*TOTP_Period*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ok, err = Verify(secret, old, base)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if ok {
		t.Errorf("code at t-60 should NOT verify (outside ±1 window)")
	}
}

// TestVerifyReplayRejected ensures the same code is rejected on a second call
// within the same TOTP step (replay protection via MemoryStore).
func TestVerifyReplayRejected(t *testing.T) {
	store := NewMemoryStore()
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}

	now := time.Unix(1_700_000_000, 0)

	// Enroll a user.
	ctx := t.Context()
	err = store.Enroll(ctx, Enrollment{
		TenantID: "t1",
		UserID:   "u1",
		Secret:   secret,
		CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	code, err := TOTP(secret, now)
	if err != nil {
		t.Fatal(err)
	}

	// First verification: must succeed.
	ok, err := VerifyCode(ctx, store, "t1", "u1", code, now)
	if err != nil || !ok {
		t.Fatalf("first verify should succeed: ok=%v err=%v", ok, err)
	}

	// Second verification with same code: must be rejected (replay).
	ok, err = VerifyCode(ctx, store, "t1", "u1", code, now)
	if err != nil {
		t.Fatalf("unexpected error on replay: %v", err)
	}
	if ok {
		t.Error("replay of the same code should be rejected")
	}
}

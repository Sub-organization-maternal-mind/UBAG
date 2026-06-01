package crypto

import (
	"bytes"
	"testing"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword(hash, "correct-horse-battery-staple") {
		t.Error("VerifyPassword returned false for correct password")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Error("VerifyPassword returned true for wrong password")
	}
}

func TestHashPasswordDifferentSalts(t *testing.T) {
	h1, _ := HashPassword("password")
	h2, _ := HashPassword("password")
	if h1 == h2 {
		t.Error("two hashes of the same password must differ (different salts)")
	}
}

func TestVerifyPasswordInvalidHash(t *testing.T) {
	if VerifyPassword("not-a-hash", "password") {
		t.Error("VerifyPassword must return false for garbage hash")
	}
	if VerifyPassword("", "password") {
		t.Error("VerifyPassword must return false for empty hash")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 1)
	}
	plain := []byte("top secret payload")

	ct, err := SealWithKEK(plain, kek)
	if err != nil {
		t.Fatalf("SealWithKEK: %v", err)
	}
	if bytes.Equal(ct, plain) {
		t.Error("ciphertext must not equal plaintext")
	}

	got, err := OpenWithKEK(ct, kek)
	if err != nil {
		t.Fatalf("OpenWithKEK: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Errorf("OpenWithKEK: got %q, want %q", got, plain)
	}
}

func TestSealProducesUniqueNonces(t *testing.T) {
	kek := bytes.Repeat([]byte{0xAB}, 32)
	plain := []byte("data")
	ct1, _ := SealWithKEK(plain, kek)
	ct2, _ := SealWithKEK(plain, kek)
	if bytes.Equal(ct1, ct2) {
		t.Error("two seals of the same plaintext must produce different ciphertexts (different nonces)")
	}
}

func TestOpenWithWrongKey(t *testing.T) {
	kek1 := bytes.Repeat([]byte{0x01}, 32)
	kek2 := bytes.Repeat([]byte{0x02}, 32)
	ct, _ := SealWithKEK([]byte("secret"), kek1)
	if _, err := OpenWithKEK(ct, kek2); err == nil {
		t.Error("OpenWithKEK must fail with the wrong key")
	}
}

func TestOpenTamperedCiphertext(t *testing.T) {
	kek := bytes.Repeat([]byte{0x01}, 32)
	ct, _ := SealWithKEK([]byte("secret"), kek)
	ct[len(ct)-1] ^= 0xFF // flip last byte
	if _, err := OpenWithKEK(ct, kek); err == nil {
		t.Error("OpenWithKEK must fail for tampered ciphertext")
	}
}

func TestOpenTooShort(t *testing.T) {
	kek := bytes.Repeat([]byte{0x01}, 32)
	if _, err := OpenWithKEK([]byte("short"), kek); err == nil {
		t.Error("OpenWithKEK must fail for too-short ciphertext")
	}
}

func TestSealWrongKeySize(t *testing.T) {
	if _, err := SealWithKEK([]byte("data"), []byte("short-key")); err == nil {
		t.Error("SealWithKEK must fail for wrong key size")
	}
}

func TestLoadKEKMissing(t *testing.T) {
	t.Setenv("UBAG_MASTER_KEK_HEX", "")
	if _, err := LoadKEK(); err == nil {
		t.Error("LoadKEK must fail when env var is missing")
	}
}

func TestLoadKEKValid(t *testing.T) {
	// 64 hex chars = 32 bytes
	t.Setenv("UBAG_MASTER_KEK_HEX", "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	kek, err := LoadKEK()
	if err != nil {
		t.Fatalf("LoadKEK: %v", err)
	}
	if len(kek) != 32 {
		t.Errorf("kek len = %d, want 32", len(kek))
	}
}

func TestLoadKEKBadHex(t *testing.T) {
	t.Setenv("UBAG_MASTER_KEK_HEX", "not-hex")
	if _, err := LoadKEK(); err == nil {
		t.Error("LoadKEK must fail for non-hex value")
	}
}

func TestLoadKEKWrongLength(t *testing.T) {
	t.Setenv("UBAG_MASTER_KEK_HEX", "0102030405060708") // only 8 bytes
	if _, err := LoadKEK(); err == nil {
		t.Error("LoadKEK must fail for wrong length")
	}
}
